package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"fuckyourcli/agui"
	"fuckyourcli/config"
	"fuckyourcli/modes"
	"fuckyourcli/providers"
	"fuckyourcli/types"
)

func TestAutoCompactDecision(t *testing.T) {
	cases := []struct {
		name       string
		cur        int
		threshold  int
		suppressed bool
		wantRun    bool
		wantReset  bool
	}{
		{"below threshold", 100, 1000, false, false, true},
		{"below threshold resets suppression", 100, 1000, true, false, true},
		{"at threshold", 1000, 1000, false, true, false},
		{"above threshold", 2000, 1000, false, true, false},
		{"above threshold suppressed", 2000, 1000, true, false, false},
		{"zero threshold below default", 100, 0, false, false, true},
		{"zero threshold above default", 40000, 0, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run, reset := autoCompactDecision(c.cur, c.threshold, c.suppressed)
			if run != c.wantRun || reset != c.wantReset {
				t.Fatalf("autoCompactDecision(%d,%d,%v) = (%v,%v), want (%v,%v)",
					c.cur, c.threshold, c.suppressed, run, reset, c.wantRun, c.wantReset)
			}
		})
	}
}

func TestCompactContextNoopWhenNothingToCompact(t *testing.T) {
	s := &ChatSession{ChatHistory: []types.Message{{Role: "system", Content: "sys"}}}
	before, after, err := compactContext(s, providers.NewSilentOutputSink())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if before != after {
		t.Errorf("before=%d after=%d, want equal", before, after)
	}
	if len(s.ChatHistory) != 1 {
		t.Errorf("history mutated: len=%d", len(s.ChatHistory))
	}
}

func TestMaybeAutoCompactDisabledIsNoop(t *testing.T) {
	// The default configuration has auto-compaction disabled.
	s := &ChatSession{ChatHistory: []types.Message{{Role: "system", Content: "sys"}}}
	maybeAutoCompact(s, providers.NewSilentOutputSink())
	if len(s.ChatHistory) != 1 {
		t.Errorf("history mutated while disabled: len=%d", len(s.ChatHistory))
	}
}

func compactionTestConfig(t *testing.T, enabled, midTurn bool, threshold int) config.Config {
	t.Helper()
	cfg := config.Config{Provider: config.ProviderOpenAICompatible, Model: "test",
		CompactionEnabled: enabled, CompactionMidTurn: midTurn, CompactionThreshold: threshold}
	temperatureConfigLoader(t)(cfg)
	return config.Get()
}

func compactionCall(id string) types.ToolCall {
	return types.ToolCall{ID: id, Function: types.ToolCallFunction{Name: "ls", Arguments: map[string]interface{}{"path": "."}}}
}

func turnCompactionHistory() []types.Message {
	return []types.Message{
		{Role: "system", Content: "permanent instructions <project_context>repo rules</project_context>"},
		{Role: "user", Content: "old task"},
		{Role: "assistant", Content: strings.Repeat("old work ", 3000)},
		{Role: "user", Content: "implement exactly this\nkeep this request verbatim"},
		{Role: "assistant", ToolCalls: []types.ToolCall{compactionCall("a"), compactionCall("b")},
			ReasoningContent: "reasoning", ReasoningSignature: "signature", ReasoningItem: `[{"encrypted_content":"opaque"}]`, ReasoningModel: "test"},
		{Role: "tool", ToolCallID: "a", Name: "ls", Content: "first result"},
		{Role: "tool", ToolCallID: "b", Name: "ls", Content: "second result"},
	}
}

func TestTurnCompactionRetainsRequestInstructionsAndCompleteBatch(t *testing.T) {
	compactionTestConfig(t, true, true, 8000)
	history := turnCompactionHistory()
	for _, oneShot := range []bool{false, true} {
		t.Run(map[bool]string{false: "persistent", true: "one-shot"}[oneShot], func(t *testing.T) {
			s := &ChatSession{ChatHistory: history, CurrentMode: &modes.DebugMode}
			if oneShot {
				s.CurrentMode = &modes.PlanMode
				s.turnModeOverride = &modes.DebugMode
			}
			ctx := newGenerationContext(s)
			defer finishGeneration(s)
			before, after, err := compactContextWith(ctx, s, true, 8000, func(got context.Context, messages []types.Message, _ *ChatSession) (string, error) {
				if got != ctx || s.IsGenerating.Load() != 1 {
					t.Fatal("compaction must share the live turn context")
				}
				if len(messages) != 2 || len(messages[1].ToolCalls) != 0 || strings.Contains(messages[1].Content, "implement exactly this") {
					t.Fatal("retained request must not be summarized or replayed as live tools")
				}
				return "## Goal\nContinue implementation.", nil
			})
			if err != nil || after >= before {
				t.Fatalf("compaction: %d -> %d, %v", before, after, err)
			}
			if !reflect.DeepEqual(s.ChatHistory[0], history[0]) || !reflect.DeepEqual(s.ChatHistory[len(s.ChatHistory)-4:], history[3:]) {
				t.Fatalf("request, reasoning or batch changed: %+v", s.ChatHistory)
			}
			if !strings.Contains(s.ChatHistory[2].Content, modes.DebugMode.GetInstructions(s.subagentsEnabled())) {
				t.Fatal("active mode instructions missing")
			}
			n := len(s.ChatHistory)
			restoreModeInstructions(s, config.ProviderOpenAICompatible)
			if len(s.ChatHistory) != n {
				t.Fatal("mode instructions duplicated")
			}
		})
	}
}

func TestTurnCompactionSummarizesOversizedActiveBatchWhole(t *testing.T) {
	compactionTestConfig(t, true, true, 1000)
	history := turnCompactionHistory()
	// Distinct targets: identical calls are deduplicated in the transcript and
	// this test is about the oversized batch being summarized as a whole.
	history[4].ToolCalls[1].Function.Arguments = map[string]interface{}{"path": "sub"}
	history[5].Content = strings.Repeat("huge output ", 1000)
	s := &ChatSession{ChatHistory: history}
	_, _, err := compactContextWith(context.Background(), s, true, 1000, func(_ context.Context, messages []types.Message, _ *ChatSession) (string, error) {
		if !strings.Contains(messages[1].Content, "huge output") || !strings.Contains(messages[1].Content, "second result") {
			t.Fatal("the entire oversized batch must be summarized")
		}
		return "batch completed", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ChatHistory) != 3 || !reflect.DeepEqual(s.ChatHistory[2], history[3]) {
		t.Fatalf("oversized active turn not compacted: %+v", s.ChatHistory)
	}
}

func TestCompactionRejectsPendingOrMismatchedTools(t *testing.T) {
	compactionTestConfig(t, true, true, 1000)
	for _, variant := range []string{"pending", "partial", "wrong-id", "orphan"} {
		t.Run(variant, func(t *testing.T) {
			history := turnCompactionHistory()
			switch variant {
			case "pending":
				history = history[:5]
			case "partial":
				history = history[:6]
			case "wrong-id":
				history[6].ToolCallID = "wrong"
			case "orphan":
				history = append(history, types.Message{Role: "tool", Content: "orphan"})
			}
			s := &ChatSession{ChatHistory: history}
			_, _, err := compactContextWith(context.Background(), s, true, 1000, func(context.Context, []types.Message, *ChatSession) (string, error) {
				t.Fatal("must not request a summary with pending tools")
				return "", nil
			})
			if err == nil || !reflect.DeepEqual(history, s.ChatHistory) {
				t.Fatal("unsafe history accepted or mutated")
			}
		})
	}
}

func TestCompactionFailuresKeepHistoryAndModeFlag(t *testing.T) {
	compactionTestConfig(t, true, true, 1000)
	for _, kind := range []string{"empty", "error", "cancel", "no reduction"} {
		t.Run(kind, func(t *testing.T) {
			history := turnCompactionHistory()
			s := &ChatSession{ChatHistory: history, persistentModeInjected: "debug"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, _, err := compactContextWith(ctx, s, true, 1000, func(context.Context, []types.Message, *ChatSession) (string, error) {
				switch kind {
				case "empty":
					return "  \n", nil
				case "error":
					return "", errors.New("provider failure")
				case "cancel":
					cancel()
					return "partial", nil
				default:
					return strings.Repeat("large summary ", 10000), nil
				}
			})
			if err == nil || !reflect.DeepEqual(history, s.ChatHistory) || s.persistentModeInjected != "debug" {
				t.Fatal("failed summary changed session state")
			}
		})
	}
}

func TestValidateCompactionResponse(t *testing.T) {
	for _, reason := range []string{"", "stop", "end_turn", "stop_sequence", "STOP", "length", "max_tokens", "MAX_TOKENS", "max_output_tokens", "model_context_window_exceeded", "incomplete", "content_filter", "refusal", "tool_calls", "unknown"} {
		_, err := validateCompactionResponse(context.Background(), &types.StreamResult{Content: "summary", FinishReason: reason})
		wantOK := reason == "" || reason == "stop" || reason == "end_turn" || reason == "stop_sequence" || reason == "STOP"
		if (err == nil) != wantOK {
			t.Errorf("reason %q: %v", reason, err)
		}
	}
	for _, reason := range []string{"", "stop"} {
		for _, result := range []*types.StreamResult{nil, {Content: "partial", Interrupted: true, FinishReason: reason}, {Content: " ", FinishReason: reason}, {Content: "x", FinishReason: reason, ToolCalls: []types.ToolCall{compactionCall("a")}}} {
			if _, err := validateCompactionResponse(context.Background(), result); err == nil {
				t.Errorf("invalid summary accepted: %+v", result)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := validateCompactionResponse(ctx, &types.StreamResult{Content: "summary", FinishReason: reason}); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled summary accepted: %v", err)
		}
	}
}

type compactionProviderFunc func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error)

func (f compactionProviderFunc) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	return f(ctx, messages, tools, sink)
}

func TestMidTurnCompactionAfterCompletedToolsIsOptIn(t *testing.T) {
	for _, flags := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		t.Run(map[bool]string{false: "auto off", true: "auto on"}[flags[0]]+"/"+map[bool]string{false: "mid off", true: "mid on"}[flags[1]], func(t *testing.T) {
			cfg := compactionTestConfig(t, flags[0], flags[1], 1000)
			s := &ChatSession{RestrictedRoot: t.TempDir(), ChatHistory: []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "continue task verbatim"}}}
			requests, summaries := 0, 0
			p := compactionProviderFunc(func(ctx context.Context, messages []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
				requests++
				if requests == 1 {
					return &types.StreamResult{Content: strings.Repeat("reasoning ", 600), FinishReason: "tool_calls", ToolCalls: []types.ToolCall{compactionCall("a"), compactionCall("b")}}, nil
				}
				if requests != 2 || ctx.Err() != nil {
					t.Fatal("unexpected follow-up")
				}
				return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
			})
			streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, func(ctx context.Context, _ []types.Message, session *ChatSession) (string, error) {
				summaries++
				if len(pendingToolCalls(session.ChatHistory)) != 0 || session.ChatHistory[len(session.ChatHistory)-2].Role != "tool" || session.ChatHistory[len(session.ChatHistory)-1].Role != "tool" || ctx != s.runContext() {
					t.Fatal("compaction split a batch or lost cancellation")
				}
				return "work in progress", nil
			})
			want := 0
			if flags[0] && flags[1] {
				want = 1
			}
			if requests != 2 || summaries != want {
				t.Fatalf("requests=%d summaries=%d want=%d", requests, summaries, want)
			}
			if s.ChatHistory[len(s.ChatHistory)-1].Content != "done" {
				t.Fatal("active turn did not continue")
			}
		})
	}
}

func TestMidTurnCompactionFailureAndAttemptGuards(t *testing.T) {
	for _, failure := range []bool{false, true} {
		cfg := compactionTestConfig(t, true, true, 1000)
		s := &ChatSession{RestrictedRoot: t.TempDir(), ChatHistory: []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}}
		requests, summaries := 0, 0
		p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			requests++
			if requests <= 5 {
				return &types.StreamResult{Content: strings.Repeat("work ", 1000), ToolCalls: []types.ToolCall{compactionCall("a")}}, nil
			}
			return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
		})
		streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (string, error) {
			summaries++
			if failure {
				return "", errors.New("failed")
			}
			return "small summary", nil
		})
		want := 2
		if failure {
			want = 1
		}
		if summaries != want || requests != 6 {
			t.Fatalf("failure=%v requests=%d summaries=%d", failure, requests, summaries)
		}
	}
}

func TestCompactionCancellationStopsFollowUp(t *testing.T) {
	cfg := compactionTestConfig(t, true, true, 1000)
	history := turnCompactionHistory()
	s := &ChatSession{ChatHistory: history}
	requests := 0
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		requests++
		return &types.StreamResult{Content: "must not run"}, nil
	})
	streamProviderLoop(s, streamOptions{}, cfg, p, func(ctx context.Context, _ []types.Message, _ *ChatSession) (string, error) {
		s.GenerationCancelMu.Lock()
		cancel := s.GenerationCancel
		s.GenerationCancelMu.Unlock()
		cancel()
		return "partial", nil
	})
	if requests != 0 || !reflect.DeepEqual(history, s.ChatHistory) || s.IsGenerating.Load() != 0 {
		t.Fatal("canceled compaction mutated history or continued sampling")
	}
}

func TestMidTurnCompactionWaitsForAGUIResume(t *testing.T) {
	cfg := compactionTestConfig(t, true, true, 1000)
	broker := interruptHarness(t, 5*time.Minute)
	var out strings.Builder
	sink := agui.NewSink(&out, nil, "compact-thread", "run")
	runCtx := agui.WithRun(context.Background(), sink, "compact-thread", "run")
	s := &ChatSession{RestrictedRoot: t.TempDir(), Sink: sink, RunCtx: runCtx, ChatHistory: []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "write"}}}
	requests, summaries := 0, 0
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		requests++
		return &types.StreamResult{Content: strings.Repeat("work ", 1000), ToolCalls: []types.ToolCall{writeCall("a", "a.txt", "a"), writeCall("b", "b.txt", "b")}}, nil
	})
	streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (string, error) {
		summaries++
		return "summary", nil
	})
	if requests != 1 || summaries != 0 || agui.RunInterrupted(runCtx) == nil || len(pendingToolCalls(s.ChatHistory)) != 2 {
		t.Fatal("pending AG-UI tools were compacted or sampled again")
	}
	// Resume the real pending batch, including its second approval boundary.
	for len(pendingToolCalls(s.ChatHistory)) > 0 {
		intr := agui.RunInterrupted(s.RunCtx).Interrupts[0]
		if err := broker.Resume("compact-thread", []agui.ResumeEntry{{InterruptID: intr.ID, Status: agui.ResumeStatusResolved, Payload: json.RawMessage(`{"approved":true}`)}}); err != nil {
			t.Fatal(err)
		}
		s.RunCtx = agui.WithRun(context.Background(), sink, "compact-thread", agui.NewID("run"))
		handleToolCallsWithSink(s, sink, pendingToolCalls(s.ChatHistory), true, false)
	}
	p = compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	})
	streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (string, error) {
		summaries++
		return "summary", nil
	})
	if summaries != 1 {
		t.Fatal("compaction did not run after completed resume")
	}
}

func TestAutoCompactIneffectiveSummaryStaysSuppressedUntilHeadroom(t *testing.T) {
	compactionTestConfig(t, true, true, 1000)
	s := &ChatSession{ChatHistory: turnCompactionHistory()}
	respond := func(context.Context, []types.Message, *ChatSession) (string, error) {
		return strings.Repeat("s", 2800), nil
	}
	maybeAutoCompactWith(context.Background(), s, providers.NewSilentOutputSink(), true, respond)
	if !s.autoCompactSuppressed {
		t.Fatal("insufficient headroom must suppress retries")
	}
	maybeAutoCompactWith(context.Background(), s, providers.NewSilentOutputSink(), true, respond)
	if !s.autoCompactSuppressed {
		t.Fatal("guard reset too early")
	}
	s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	maybeAutoCompactWith(context.Background(), s, providers.NewSilentOutputSink(), true, respond)
	if s.autoCompactSuppressed {
		t.Fatal("guard must reset below headroom target")
	}
}

func TestCompactedHistoryPersistence(t *testing.T) {
	compactionTestConfig(t, true, true, 8000)
	ws := testWorkspace(t)
	s := &ChatSession{ChatHistory: turnCompactionHistory()}
	_, _, err := compactContextWith(context.Background(), s, true, 8000, func(context.Context, []types.Message, *ChatSession) (string, error) {
		return "summary", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	saved, err := readSessionFile(ws, s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Messages, s.ChatHistory) {
		t.Fatal("compacted history did not survive persistence")
	}
}

func TestLegacyAutoCompactionStillRunsOnlyInitially(t *testing.T) {
	cfg := compactionTestConfig(t, true, false, 1000)
	s := &ChatSession{ChatHistory: turnCompactionHistory(), RestrictedRoot: t.TempDir(), CurrentMode: &modes.DebugMode}
	requests, summaries := 0, 0
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		requests++
		if requests == 1 {
			return &types.StreamResult{Content: strings.Repeat("work ", 1000), ToolCalls: []types.ToolCall{compactionCall("a")}}, nil
		}
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	})
	streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (string, error) {
		summaries++
		return "summary", nil
	})
	if summaries != 1 || requests != 2 || s.ChatHistory[3].Content != "Understood. I'm ready to continue from where we left off." {
		t.Fatal("default compaction behaviour changed")
	}
	if !isModeInstruction(s.ChatHistory[1]) {
		t.Fatal("mode instructions not restored before initial sampling")
	}
}

func TestCompactionValidatesRealProviderBeforeCommitting(t *testing.T) {
	for _, reason := range []string{"stop", "length", ""} {
		t.Run("finish="+reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request providers.OpenAIRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Tools) != 0 || len(request.Messages) != 2 || !strings.HasPrefix(request.Messages[0].Content, compactionPrompt) {
					t.Error("summary request inherited agent tools or instructions")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if reason == "" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary\"}}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary\"},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
				}
			}))
			defer server.Close()
			cfg := config.Config{Provider: config.ProviderOpenAICompatible, Model: "test",
				OpenAICompatibleProviders: []config.OpenAICompatibleProviderConfig{{Name: "test", Model: "test", BaseURL: server.URL}}}
			temperatureConfigLoader(t)(cfg)
			t.Setenv("FYC_CACHE_DIR", t.TempDir())
			history := turnCompactionHistory()
			s := &ChatSession{ChatHistory: history}
			_, _, err := compactContextWith(context.Background(), s, true, 8000, getCompactionResponse)
			if reason == "stop" || reason == "" {
				if err != nil || reflect.DeepEqual(s.ChatHistory, history) {
					t.Fatalf("valid provider summary rejected: %v", err)
				}
			} else if err == nil || !reflect.DeepEqual(s.ChatHistory, history) {
				t.Fatal("truncated/incomplete provider summary committed")
			}
		})
	}
}

func TestTurnCompactionKeepsLargeRequestAndDeveloperInstructions(t *testing.T) {
	compactionTestConfig(t, true, true, 1000)
	history := turnCompactionHistory()
	history[3].Content = strings.Repeat("user constraints ", 1000)
	developer := types.Message{Role: "developer", Content: "permanent developer instructions"}
	history = append([]types.Message{history[0], developer}, history[1:]...)
	s := &ChatSession{ChatHistory: history}
	_, _, err := compactContextWith(context.Background(), s, true, 1000, func(context.Context, []types.Message, *ChatSession) (string, error) {
		return "summary", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.ChatHistory[1], developer) || !reflect.DeepEqual(s.ChatHistory[3], history[4]) {
		t.Fatal("large user request or permanent instructions were summarized")
	}
	if _, err := compactionGroups(s.ChatHistory); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionLocalBackendsWithoutFinishReason(t *testing.T) {
	sse := func(events ...string) string {
		return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
	}
	backends := []struct {
		provider  config.ProviderType
		path      string
		stream    string
		truncated string
		failed    string
	}{
		{
			config.ProviderOpenAICompatible, "/chat/completions",
			sse(`{"choices":[{"delta":{"content":"summary"}}]}`, `[DONE]`),
			sse(`{"choices":[{"delta":{"content":"summary"},"finish_reason":"length"}]}`, `[DONE]`),
			"",
		},
		{
			config.ProviderOpenAIResponsesCompatible, "/responses",
			sse(`{"type":"response.output_text.delta","delta":"summary"}`, `[DONE]`),
			sse(`{"type":"response.output_text.delta","delta":"summary"}`, `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, `[DONE]`),
			sse(`{"type":"response.output_text.delta","delta":"summary"}`, `{"type":"response.failed","response":{"status":"failed"}}`, `[DONE]`),
		},
		{
			config.ProviderAnthropic, "/messages",
			sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"summary"}}`, `{"type":"message_stop"}`),
			sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"summary"}}`, `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, `{"type":"message_stop"}`),
			sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"summary"}}`, `{"type":"error","error":{"type":"api_error","message":"failed"}}`),
		},
	}
	for _, backend := range backends {
		for _, operation := range []string{"/compact", "auto", "mid-turn"} {
			for _, outcome := range []string{"summary", "empty", "truncated", "error"} {
				t.Run(string(backend.provider)+"/"+operation+"/"+outcome, func(t *testing.T) {
					requests := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests++
						if r.URL.Path != backend.path {
							t.Errorf("path = %q, want %q", r.URL.Path, backend.path)
						}
						var request map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Error(err)
						}
						if len(request["tools"]) != 0 {
							t.Error("summary request inherited agent tools")
						}
						stream := backend.stream
						switch outcome {
						case "empty":
							stream = strings.Replace(stream, "summary", " ", 1)
						case "truncated":
							stream = backend.truncated
						case "error":
							if backend.failed == "" {
								w.WriteHeader(http.StatusBadRequest)
								fmt.Fprint(w, "backend failed")
								return
							}
							stream = backend.failed
						}
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, stream)
					}))
					defer server.Close()
					cfg := temperatureConfig(backend.provider, "test", "default")
					cfg.Temperature = 0
					cfg.CompactionEnabled = true
					cfg.CompactionThreshold = 1000
					cfg.CompactionMidTurn = operation == "mid-turn"
					provider := config.OpenAICompatibleProviderConfig{Name: "test", Model: "test", BaseURL: server.URL}
					switch backend.provider {
					case config.ProviderOpenAICompatible:
						cfg.OpenAICompatibleProviders = []config.OpenAICompatibleProviderConfig{provider}
					case config.ProviderOpenAIResponsesCompatible:
						cfg.OpenAIResponsesCompatibleProviders = []config.OpenAICompatibleProviderConfig{provider}
					case config.ProviderAnthropic:
						cfg.AnthropicProviders = []config.OpenAICompatibleProviderConfig{provider}
					}
					temperatureConfigLoader(t)(cfg)
					t.Setenv("FYC_CACHE_DIR", t.TempDir())
					history := turnCompactionHistory()
					sink := &recordSink{}
					s := &ChatSession{ChatHistory: history, Sink: sink}
					if operation == "/compact" {
						if handled, modified := handleSlashCommand(operation, s); !handled || modified != "" {
							t.Fatal("/compact not handled")
						}
					} else if !maybeAutoCompactWith(context.Background(), s, sink, cfg.CompactionMidTurn, getCompactionResponse) {
						t.Fatal("automatic compaction not attempted")
					}
					if requests != 1 {
						t.Fatalf("summary requests = %d, want 1", requests)
					}
					if outcome == "summary" {
						if len(s.ChatHistory) < 2 || s.ChatHistory[1].Content != compactionSummaryPrefix+"summary" || strings.Contains(sink.text(), "failed") || strings.Contains(sink.text(), "Error during compaction") {
							t.Fatalf("summary without finish reason rejected: %s", sink.text())
						}
					} else if !reflect.DeepEqual(s.ChatHistory, history) {
						t.Fatal("invalid summary changed history")
					}
				})
			}
		}
	}
}
