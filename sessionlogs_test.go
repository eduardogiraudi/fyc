package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fuckyourcli/agui"
	"fuckyourcli/config"
	"fuckyourcli/logger"
	"fuckyourcli/providers"
	"fuckyourcli/store"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

func sessionEvents(t *testing.T, id string) []logger.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.CacheDir(), "logs", id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []logger.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e logger.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.SessionID != id {
			t.Fatalf("wrong conversation: %+v", e)
		}
		events = append(events, e)
	}
	return events
}

func TestSessionLogsFirstRequestToolLoopResumeAndNewChat(t *testing.T) {
	ws := testWorkspace(t)
	t.Setenv("FYC_LOG_DEBUG", "")
	cfg := compactionTestConfig(t, false, false, 0)
	s := &ChatSession{RestrictedRoot: t.TempDir(), ChatHistory: []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "first task"}}}
	requests := 0
	p := logger.Wrap(compactionProviderFunc(func(ctx context.Context, _ []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
		requests++
		if s.SessionID == "" || s.SessionCreatedAt.IsZero() || len(sessionEvents(t, s.SessionID)) != requests*2-1 {
			t.Fatal("conversation identity/start event missing before sampling")
		}
		if requests == 1 {
			return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall("a")}, FinishReason: "tool_calls"}, nil
		}
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	}), logger.Metadata{Provider: "test", Model: "model"})
	streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, getCompactionResponse)
	id := s.SessionID
	if !validSessionID(id) || requests != 2 || len(sessionEvents(t, id)) != 4 {
		t.Fatal("tool loop created separate conversations or lost requests")
	}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	resumed := &ChatSession{RestrictedRoot: s.RestrictedRoot}
	if err := resumeSession(resumed, ws, id); err != nil {
		t.Fatal(err)
	}
	ctx := newGenerationContext(resumed)
	p = logger.Wrap(compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return &types.StreamResult{Content: "another turn"}, nil
	}), logger.Metadata{Provider: "test"})
	_, err := p.StreamChat(ctx, resumed.ChatHistory, nil, nil)
	finishGeneration(resumed)
	if err != nil || resumed.SessionID != id || len(sessionEvents(t, id)) != 6 {
		t.Fatal("resume did not append to the same log set")
	}
	if err := startNewChat(resumed); err != nil {
		t.Fatal(err)
	}
	resumed.ChatHistory = append(resumed.ChatHistory, types.Message{Role: "user", Content: "new task"})
	ctx = newGenerationContext(resumed)
	_, err = p.StreamChat(ctx, nil, nil, nil)
	finishGeneration(resumed)
	if err != nil || resumed.SessionID == id || len(sessionEvents(t, resumed.SessionID)) != 2 || len(sessionEvents(t, id)) != 6 {
		t.Fatal("new chat reused the previous log set")
	}
	if err := deleteSession(ws, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.CacheDir(), "logs", id)); !os.IsNotExist(err) {
		t.Fatal("session deletion did not remove logs")
	}
}

func TestSessionPruningRemovesOnlyPrunedLogs(t *testing.T) {
	ws := testWorkspace(t)
	t.Setenv("FYC_LOG_DEBUG", "")
	idx := sessionIndex{Schema: sessionIndexSchema}
	for i := 0; i < 3; i++ {
		s := &ChatSession{ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
		ctx := sessionLogContext(context.Background(), s)
		p := logger.Wrap(compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			return &types.StreamResult{}, nil
		}), logger.Metadata{})
		if _, err := p.StreamChat(ctx, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := saveChatSession(s); err != nil {
			t.Fatal(err)
		}
		idx.Sessions = append(idx.Sessions, SessionMeta{ID: s.SessionID, UpdatedAt: time.Now().Add(time.Duration(i) * time.Hour)})
	}
	oldest := idx.Sessions[0].ID
	pruneSessions(ws, &idx, 2)
	if _, err := os.Stat(filepath.Join(store.CacheDir(), "logs", oldest)); !os.IsNotExist(err) {
		t.Fatal("pruned session logs survived")
	}
	for _, m := range idx.Sessions {
		if len(sessionEvents(t, m.ID)) != 2 {
			t.Fatal("prune removed another conversation")
		}
	}
}

func logTestBackend(t *testing.T) config.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	cfg := config.Config{Provider: config.ProviderOpenAICompatible, Model: "test", ActiveProviderName: "test",
		OpenAICompatibleProviders: []config.OpenAICompatibleProviderConfig{{Name: "test", Model: "test", BaseURL: server.URL}}}
	temperatureConfigLoader(t)(cfg)
	return config.Get()
}

func TestCompactionAndParallelSubagentsShareConversationLogs(t *testing.T) {
	testWorkspace(t)
	t.Setenv("FYC_LOG_DEBUG", "")
	logTestBackend(t)
	s := &ChatSession{RestrictedRoot: t.TempDir(), ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	ctx := newGenerationContext(s)
	defer finishGeneration(s)
	if res, err := getCompactionResponse(ctx, s.ChatHistory, s); err != nil || res.Summary != "done" {
		t.Fatalf("compaction: %q %v", res.Summary, err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"agent-a", "agent-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			result := tools.RunSubagent(ctx, tools.SubagentConfig{ID: id, Subdirectory: ".", Instructions: "complete task", MaxIterations: 1}, nil, s.RestrictedRoot, nil, nil)
			if result != "done" {
				t.Errorf("subagent: %s", result)
			}
		}(id)
	}
	wg.Wait()
	p, err := buildProvider(config.Get(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providers.WithLogging(p).StreamChat(ctx, s.ChatHistory, nil, providers.NewSilentOutputSink()); err != nil {
		t.Fatal(err)
	}
	roles := map[string]int{}
	agents := map[string]bool{}
	for _, e := range sessionEvents(t, s.SessionID) {
		if e.Type == "request_started" {
			roles[e.Role]++
			if e.Role == "subagent" {
				agents[e.AgentID] = true
			}
		}
	}
	if roles["compaction"] != 1 || roles["subagent"] != 2 || roles["main"] != 1 || !agents["agent-a"] || !agents["agent-b"] {
		t.Fatalf("scopes leaked between agents: %v %v", roles, agents)
	}
}

func bareLogRunner(root string, cfg config.Config, threadStore agui.ThreadStore) *aguiRunner {
	return &aguiRunner{base: &ChatSession{RestrictedRoot: root, ChatHistory: []types.Message{{Role: "system", Content: "sys"}}}, cfg: cfg, store: threadStore, sessions: map[string]*aguiThread{}}
}

func TestAGUILogIdentityRestartAndThreadIsolation(t *testing.T) {
	testWorkspace(t)
	t.Setenv("FYC_LOG_DEBUG", "")
	cfg := logTestBackend(t)
	root := t.TempDir()
	stateDir := t.TempDir()
	threadID := "../arbitrary/thread"
	runner := bareLogRunner(root, cfg, agui.NewFileStore(stateDir))
	if err := runner.Run(context.Background(), agui.RunRequest{ThreadID: threadID, Messages: []types.Message{{Role: "user", Content: "first"}}}, providers.NewSilentOutputSink()); err != nil {
		t.Fatal(err)
	}
	th, _ := runner.thread(threadID)
	id := th.session.SessionID
	restarted := bareLogRunner(root, cfg, agui.NewFileStore(stateDir))
	if err := restarted.Run(context.Background(), agui.RunRequest{ThreadID: threadID, Messages: []types.Message{{Role: "user", Content: "second"}}}, providers.NewSilentOutputSink()); err != nil {
		t.Fatal(err)
	}
	th, _ = restarted.thread(threadID)
	if th.session.SessionID != id || len(sessionEvents(t, id)) != 6 || len(th.session.ChatHistory) != 5 {
		t.Fatal("AG-UI restart lost conversation/log identity")
	}
	for _, other := range []string{"other", "a/../b", "b"} {
		if err := restarted.Run(context.Background(), agui.RunRequest{ThreadID: other, Messages: []types.Message{{Role: "user", Content: "task"}}}, providers.NewSilentOutputSink()); err != nil {
			t.Fatal(err)
		}
		otherThread, _ := restarted.thread(other)
		if otherThread.session.SessionID == id || len(sessionEvents(t, otherThread.session.SessionID)) != 3 {
			t.Fatal("AG-UI threads share a log set")
		}
	}
	if restarted.sessions["a/../b"].session.SessionID == restarted.sessions["b"].session.SessionID {
		t.Fatal("thread path normalization conflated distinct IDs")
	}
}

func TestAGUIResumeScopesPendingSubagentsBeforeNextGeneration(t *testing.T) {
	testWorkspace(t)
	t.Setenv("FYC_LOG_DEBUG", "")
	cfg := logTestBackend(t)
	root := t.TempDir()
	runner := bareLogRunner(root, cfg, agui.MemoryStore{})
	th, err := runner.thread("resume-thread")
	if err != nil {
		t.Fatal(err)
	}
	th.seeded = true
	th.session.ChatHistory = append(th.session.ChatHistory, types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "pending", Function: types.ToolCallFunction{
		Name: "spawn_subagents", Arguments: map[string]any{"agents": []any{map[string]any{"id": "pending-agent", "subdirectory": ".", "instructions": "complete task", "max_iterations": float64(1)}}},
	}}}})
	if err := runner.Run(context.Background(), agui.RunRequest{ThreadID: "resume-thread", Resume: true}, providers.NewSilentOutputSink()); err != nil {
		t.Fatal(err)
	}
	es := sessionEvents(t, th.session.SessionID)
	if len(es) != 6 || es[0].Role != "subagent" || es[0].AgentID != "pending-agent" || es[3].Role != "main" || len(pendingToolCalls(th.session.ChatHistory)) != 0 {
		t.Fatalf("resumed pending tool lost log scope: %+v", es)
	}
}
