package logger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

type providerFunc func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error)

func (f providerFunc) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	return f(ctx, messages, tools, sink)
}

func setupLogs(t *testing.T) string {
	t.Helper()
	t.Setenv("FYC_CACHE_DIR", t.TempDir())
	t.Setenv("FYC_LOG_DEBUG", "")
	return getLogDir()
}

func events(t *testing.T, root, session string) []Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, session, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("unfinished JSONL record")
	}
	var out []Event
	s := bufio.NewScanner(strings.NewReader(string(data)))
	for s.Scan() {
		var e Event
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLogDirUsesFycNamespace(t *testing.T) {
	base := t.TempDir()
	t.Setenv("FYC_CACHE_DIR", "")
	t.Setenv("HOME", base)
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("LOCALAPPDATA", base)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := getLogDir(), filepath.Join(cache, "fyc", "logs"); got != want {
		t.Fatalf("getLogDir() = %q, want %q", got, want)
	}
	t.Setenv("FYC_CACHE_DIR", base)
	if got := getLogDir(); got != filepath.Join(base, "logs") {
		t.Fatalf("cache override ignored: %s", got)
	}
}

func TestConversationEventsAndDebugOff(t *testing.T) {
	root := setupLogs(t)
	want := &types.StreamResult{Content: "private response", ReasoningContent: "private reasoning", PromptTokens: 12,
		CompletionTokens: 7, CachedTokens: 4, FinishReason: "stop"}
	p := providerFunc(func(ctx context.Context, m []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
		if m[0].Content != "private prompt" || len(tools) != 1 {
			t.Fatal("wrapper changed input")
		}
		HTTPAttempt(ctx, 429, time.Second, false)
		HTTPAttempt(ctx, 200, 0, false)
		return want, nil
	})
	for i := 0; i < 2; i++ {
		// A fresh wrapper/context simulates resuming in a different process.
		got, err := Wrap(p, Metadata{Provider: "test", Model: "model"}).StreamChat(WithSession(context.Background(), "conversation"),
			[]types.Message{{Content: "private prompt"}}, []types.Tool{{}}, nil)
		if err != nil || got != want {
			t.Fatalf("provider result changed: %v", err)
		}
	}
	es := events(t, root, "conversation")
	if len(es) != 8 || es[0].RequestID == es[4].RequestID {
		t.Fatalf("requests not distinct: %+v", es)
	}
	for i, e := range es {
		if e.RequestID != es[i/4*4].RequestID || e.SessionID != "conversation" || e.Role != "main" || e.Schema != 1 || e.Timestamp.IsZero() {
			t.Fatalf("bad correlation: %+v", e)
		}
	}
	if es[3].PromptTokens != 12 || es[3].CompletionTokens != 7 || es[3].CachedTokens != 4 || es[3].FinishReason != "stop" {
		t.Fatalf("usage missing: %+v", es[3])
	}
	data, _ := os.ReadFile(filepath.Join(root, "conversation", "events.jsonl"))
	if strings.Contains(string(data), "private") {
		t.Fatal("content leaked into diagnostics")
	}
	if _, err := os.Stat(filepath.Join(root, "conversation", "responses")); !os.IsNotExist(err) {
		t.Fatal("debug dump written by default")
	}
	if runtime.GOOS != "windows" {
		for path, perm := range map[string]os.FileMode{
			root: 0o700, filepath.Join(root, "conversation"): 0o700, filepath.Join(root, "conversation", "events.jsonl"): 0o600,
		} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != perm {
				t.Fatalf("private permissions on %s: %v %v", path, info, err)
			}
		}
	}
}

func TestErrorsAndInterruptionWithoutSensitiveErrorText(t *testing.T) {
	root := setupLogs(t)
	for _, kind := range []string{"provider_error", "canceled", "deadline_exceeded", "interrupted"} {
		id := strings.ReplaceAll(kind, "_", "-")
		ctx := WithSession(context.Background(), id)
		var result *types.StreamResult
		var wantErr error
		switch kind {
		case "provider_error":
			wantErr = errors.New("secret body key=private")
		case "canceled":
			c, cancel := context.WithCancel(ctx)
			cancel()
			ctx, wantErr = c, context.Canceled
		case "deadline_exceeded":
			wantErr = fmt.Errorf("private URL: %w", context.DeadlineExceeded)
		case "interrupted":
			result = &types.StreamResult{Content: "partial private", Interrupted: true}
		}
		p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			return result, wantErr
		}), Metadata{Provider: "test"})
		got, err := p.StreamChat(ctx, nil, nil, nil)
		if got != result || err != wantErr {
			t.Fatal("logging changed failure semantics")
		}
		es := events(t, root, id)
		if len(es) != 2 || (kind != "interrupted" && (es[1].Type != "request_failed" || es[1].ErrorKind != kind)) || (kind == "interrupted" && !es[1].Interrupted) {
			t.Fatalf("bad failure event: %+v", es)
		}
		data, _ := os.ReadFile(filepath.Join(root, id, "events.jsonl"))
		if strings.Contains(string(data), "private") {
			t.Fatal("provider error contents leaked")
		}
	}
}

func TestDebugDumpRedactionAndUniqueNames(t *testing.T) {
	root := setupLogs(t)
	t.Setenv("FYC_LOG_DEBUG", "1")
	result := &types.StreamResult{Content: "quoted \"credential\"", ReasoningContent: "credential",
		ReasoningItem: "opaque", ReasoningSignature: "signature",
		ToolCalls: []types.ToolCall{{ThoughtSignature: "signature", Function: types.ToolCallFunction{Name: "sh", ArgumentsRaw: "credential",
			Arguments: map[string]any{"api_key": "other", "nested": map[string]any{"access_token": "other"}, "text": "credential"}}}}}
	p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return result, nil
	}), Metadata{Provider: "test"}, "credential")
	for i := 0; i < 3; i++ {
		if _, err := p.StreamChat(WithSession(context.Background(), "debug"), nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "debug", "responses")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 3 {
		t.Fatalf("dump collision: %v %v", files, err)
	}
	for i, e := range events(t, root, "debug") {
		if i%2 != 0 {
			continue
		}
		path := filepath.Join(dir, e.RequestID+".json")
		data, err := os.ReadFile(path)
		if err != nil || !json.Valid(data) {
			t.Fatalf("invalid dump: %v", err)
		}
		for _, secret := range []string{"credential", "other", "opaque", "signature"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("dump leaked %s", secret)
			}
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0o600 {
				t.Fatal("dump permissions")
			}
		}
	}
	if result.ToolCalls[0].ThoughtSignature != "signature" || result.ToolCalls[0].Function.Arguments["api_key"] != "other" {
		t.Fatal("redaction mutated result")
	}
}

func TestConcurrentAgentsAndSeparateConversations(t *testing.T) {
	root := setupLogs(t)
	p := Wrap(providerFunc(func(ctx context.Context, _ []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
		HTTPAttempt(ctx, 200, 0, false)
		return &types.StreamResult{FinishReason: "stop"}, nil
	}), Metadata{Provider: "test"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := WithAgent(WithSession(context.Background(), fmt.Sprintf("conversation-%d", i%2)), "subagent", fmt.Sprint(i))
			if _, err := p.StreamChat(ctx, nil, nil, nil); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for _, id := range []string{"conversation-0", "conversation-1"} {
		es := events(t, root, id)
		byRequest := map[string][]Event{}
		for _, e := range es {
			byRequest[e.RequestID] = append(byRequest[e.RequestID], e)
		}
		if len(es) != 30 || len(byRequest) != 10 {
			t.Fatalf("lost concurrent events: %d/%d", len(es), len(byRequest))
		}
		for _, es := range byRequest {
			if len(es) != 3 || es[0].Type != "request_started" || es[2].Type != "request_finished" || es[0].Role != "subagent" || es[0].AgentID != es[2].AgentID {
				t.Fatalf("mixed agents: %+v", es)
			}
		}
	}
}

func TestInvalidOrAbsentSessionIsNoop(t *testing.T) {
	root := setupLogs(t)
	calls := 0
	p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		calls++
		return nil, nil
	}), Metadata{})
	for _, id := range []string{"", "../outside", ".", "..", "/tmp", "a/b", "a\\b"} {
		_, _ = p.StreamChat(WithSession(context.Background(), id), nil, nil, nil)
	}
	_, _ = p.StreamChat(context.Background(), nil, nil, nil)
	if calls != 8 {
		t.Fatal("no-op skipped provider")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid context created logs")
	}
	if err := RemoveSession("../outside"); err == nil {
		t.Fatal("unsafe deletion allowed")
	}
}

func TestLoggingFailureIsNonFatal(t *testing.T) {
	root := setupLogs(t)
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var notices []string
	store.SetLogger(func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) })
	t.Cleanup(func() { store.SetLogger(nil) })
	want := &types.StreamResult{Content: "ok"}
	p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return want, nil
	}), Metadata{})
	got, err := p.StreamChat(WithSession(context.Background(), "failure"), nil, nil, nil)
	if err != nil || got != want || len(notices) != 1 {
		t.Fatalf("logging failure affected call or was silent: %v %v", err, notices)
	}
}

func TestAppendRepairsCrashTail(t *testing.T) {
	for _, tail := range []string{"partial", strings.Repeat("x", 9000), ""} {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		if err := os.WriteFile(path, []byte("{\"complete\":true}\n"+tail), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := appendLine(path, []byte("{\"next\":true}\n")); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "{\"complete\":true}\n{\"next\":true}\n" {
			t.Fatal("crash tail not repaired")
		}
	}
}

func TestWholeConversationRetentionAndDeletion(t *testing.T) {
	root := setupLogs(t)
	makeLog := func(id string, age time.Duration) string {
		t.Helper()
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	old := makeLog("old", 31*24*time.Hour)
	older := makeLog("older", time.Hour)
	fresh := makeLog("fresh", 0)
	live, err := begin(scope{session: "live", role: "main"}, Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	defer live.release()
	legacy := filepath.Join(root, "response_legacy.json")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "unrelated")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOldLogs(root, time.Now(), MaxLogAge, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired conversation not removed")
	}
	if err := cleanupOldLogs(root, time.Now(), 0, 1); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{older, fresh} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("size retention did not remove %s", dir)
		}
	}
	for _, path := range []string{live.dir(), unrelated, legacy} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retention removed protected/unrelated path: %s", path)
		}
	}
	if err := RemoveSession("live"); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("deleted active conversation: %v", err)
	}
	live.release()
	if err := RemoveSession("live"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live.dir()); !os.IsNotExist(err) {
		t.Fatal("conversation logs not deleted")
	}
}

func TestCrossProcessWriters(t *testing.T) {
	if os.Getenv("FYC_LOG_TEST_CHILD") == "1" {
		p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			return &types.StreamResult{Content: "ok"}, nil
		}), Metadata{Provider: "child"})
		for i := 0; i < 10; i++ {
			_, _ = p.StreamChat(WithSession(context.Background(), "shared"), nil, nil, nil)
		}
		return
	}
	root := setupLogs(t)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessWriters$")
			cmd.Env = append(os.Environ(), "FYC_LOG_TEST_CHILD=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %v %s", err, out)
			}
		}()
	}
	wg.Wait()
	if got := len(events(t, root, "shared")); got != 60 {
		t.Fatalf("lost cross-process events: %d", got)
	}
}
