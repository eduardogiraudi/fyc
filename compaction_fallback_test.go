package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/providers"
	"fuckyourcli/types"
)

// fakeChatServer is a Chat Completions endpoint whose behaviour is switchable
// per test: a summary, a truncated summary or an API failure.
type fakeChatServer struct {
	calls   atomic.Int32
	summary string
	mode    string // "" | "error" | "truncate"
}

func (f *fakeChatServer) handler(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	switch f.mode {
	case "error":
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"model not found","type":"invalid_request_error"}}`)
	case "truncate":
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", f.summary)
	}
}

// fallbackTestConfig installs an active provider that answers and a second entry
// used as the compactor. Both are OpenAI-compatible so the same fake backend
// covers every case.
func fallbackTestConfig(t *testing.T, compactor config.CompactionModelConfig, policy config.CompactionFailurePolicy) (ok, broken *fakeChatServer) {
	t.Helper()
	testWorkspace(t)
	ok = &fakeChatServer{summary: "## Goal\ncontinue"}
	broken = &fakeChatServer{mode: "error"}
	okSrv := httptest.NewServer(http.HandlerFunc(ok.handler))
	brokenSrv := httptest.NewServer(http.HandlerFunc(broken.handler))
	t.Cleanup(okSrv.Close)
	t.Cleanup(brokenSrv.Close)
	cfg := config.Config{
		Provider: config.ProviderOpenAICompatible, Model: "chat-model", ActiveProviderName: "Work",
		OpenAICompatibleProviders: []config.OpenAICompatibleProviderConfig{
			{Name: "Work", BaseURL: okSrv.URL, Model: "chat-model"},
			{Name: "Broken", BaseURL: brokenSrv.URL, Model: "broken-model"},
		},
		CompactionEnabled:   true,
		CompactionThreshold: 4000,
		CompactionModel:     compactor,
		CompactionOnFailure: string(policy),
	}
	temperatureConfigLoader(t)(cfg)
	return ok, broken
}

// compactableHistory is a history the planner accepts: a system prompt, a task
// and one completed exchange.
func compactableHistory() []types.Message {
	return []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: strings.Repeat("work ", 200)},
	}
}

func TestCompactionFallsBackToCurrentModelWithoutTouchingConfig(t *testing.T) {
	ok, broken := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionFallbackCurrent)
	before := config.Get()
	s := &ChatSession{ChatHistory: compactableHistory()}

	_, after, res, err := compactContextWith(context.Background(), s, false, 4000, getCompactionResponse)
	if err != nil {
		t.Fatalf("compaction: %v", err)
	}
	if !res.FellBack || broken.calls.Load() != 1 || ok.calls.Load() != 1 {
		t.Fatalf("fallback=%v broken=%d current=%d", res.FellBack, broken.calls.Load(), ok.calls.Load())
	}
	if !strings.Contains(res.Model, "chat-model") || len(res.Attempts) != 1 || !strings.Contains(res.Attempts[0].Model, "broken-model") {
		t.Fatalf("result trace = %+v", res)
	}
	if after <= 0 || !strings.Contains(s.ChatHistory[1].Content, "## Goal") {
		t.Fatalf("summary not committed: %+v", s.ChatHistory)
	}
	// The fallback must never rewrite the configuration.
	if !reflect.DeepEqual(before, config.Get()) {
		t.Fatalf("configuration changed by the compaction fallback: %+v", config.Get())
	}
}

func TestCompactionFallbackSkipsDuplicateTarget(t *testing.T) {
	// The configured compactor is the active entry and model: the fallback would
	// repeat the very same failing request, so it must not happen.
	ok, _ := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Work", Model: "chat-model"},
		config.CompactionFallbackCurrent)
	ok.mode = "error"
	s := &ChatSession{ChatHistory: compactableHistory()}

	history := append([]types.Message(nil), s.ChatHistory...)
	_, _, res, err := compactContextWith(context.Background(), s, false, 4000, getCompactionResponse)
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error = %v", err)
	}
	if ok.calls.Load() != 1 || res.FellBack {
		t.Fatalf("duplicate request: calls=%d fallback=%v", ok.calls.Load(), res.FellBack)
	}
	if !reflect.DeepEqual(history, s.ChatHistory) {
		t.Fatal("failed compaction changed the history")
	}
}

func TestCompactionAbortPolicyKeepsHistory(t *testing.T) {
	ok, broken := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionAbort)
	s := &ChatSession{ChatHistory: compactableHistory()}
	history := append([]types.Message(nil), s.ChatHistory...)

	_, _, res, err := compactContextWith(context.Background(), s, false, 4000, getCompactionResponse)
	if err == nil {
		t.Fatal("abort policy must surface the failure")
	}
	if ok.calls.Load() != 0 || broken.calls.Load() != 1 || res.FellBack {
		t.Fatalf("abort policy retried: current=%d broken=%d", ok.calls.Load(), broken.calls.Load())
	}
	if !reflect.DeepEqual(history, s.ChatHistory) {
		t.Fatal("abort policy must not touch the history")
	}
	if hint := compactionFailureHint(config.Get(), true); !strings.Contains(hint, "policy: abort") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestCompactionFallsBackWhenCompactorIsGone(t *testing.T) {
	ok, _ := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Deleted", Model: "gone"},
		config.CompactionFallbackCurrent)
	s := &ChatSession{ChatHistory: compactableHistory()}

	_, _, res, err := compactContextWith(context.Background(), s, false, 4000, getCompactionResponse)
	if err != nil {
		t.Fatalf("a removed compactor must fall back: %v", err)
	}
	if !res.FellBack || ok.calls.Load() != 1 {
		t.Fatalf("fallback=%v calls=%d", res.FellBack, ok.calls.Load())
	}
}

func TestCompactionFallsBackOnTruncatedSummary(t *testing.T) {
	ok, broken := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionFallbackCurrent)
	broken.mode = "truncate"
	s := &ChatSession{ChatHistory: compactableHistory()}

	if _, _, res, err := compactContextWith(context.Background(), s, false, 4000, getCompactionResponse); err != nil || !res.FellBack {
		t.Fatalf("truncated summary must fall back: %v %+v", err, res)
	}
	if ok.calls.Load() != 1 {
		t.Fatalf("current model requests = %d", ok.calls.Load())
	}
}

func TestCompactionNeverFallsBackAfterInterrupt(t *testing.T) {
	ok, broken := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionFallbackCurrent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &ChatSession{ChatHistory: compactableHistory()}

	if _, _, _, err := compactContextWith(ctx, s, false, 4000, getCompactionResponse); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if ok.calls.Load() != 0 || broken.calls.Load() != 0 {
		t.Fatalf("interrupted compaction reached the API: %d/%d", ok.calls.Load(), broken.calls.Load())
	}
}

func TestCompactionAutoSuppressesAfterFailedFallback(t *testing.T) {
	ok, _ := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionFallbackCurrent)
	ok.mode = "error"
	s := &ChatSession{ChatHistory: compactableHistory()}
	sink := providers.NewSilentOutputSink()

	// Force the threshold below the current size so the automatic path runs.
	loader := temperatureConfigLoader(t)
	cfg := config.Get()
	cfg.CompactionThreshold = 1
	loader(cfg)

	if !maybeAutoCompactWith(context.Background(), s, sink, false, getCompactionResponse) {
		t.Fatal("auto-compaction did not attempt the summary")
	}
	if !s.autoCompactSuppressed {
		t.Fatal("a failed compactor must latch the suppression")
	}
	if ok.calls.Load() != 1 {
		t.Fatalf("current model requests = %d", ok.calls.Load())
	}
}

// TestCompactionRetryableIdleTimeout guards the case the fallback exists for: a
// provider that stops writing cancels the response stream with a wrapped
// DeadlineExceeded while the turn context stays intact. Treating that as the
// user's interruption would skip the fallback exactly when it is needed.
func TestCompactionRetryableIdleTimeout(t *testing.T) {
	idle := fmt.Errorf("chat completions stream interrupted: provider stream idle for 4m0s: %w", context.DeadlineExceeded)
	if !compactionRetryable(context.Background(), idle) {
		t.Fatal("an idle timeout must fall back")
	}
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	if compactionRetryable(interrupted, fmt.Errorf("stream: %w", context.Canceled)) {
		t.Fatal("a user interrupt must never fall back")
	}
	if compactionRetryable(context.Background(), nil) {
		t.Fatal("no failure must not be retried")
	}
}

func compactionCancellationTestConfig(t *testing.T, cancel func()) (*fakeChatServer, *atomic.Int32) {
	t.Helper()
	_, primary := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionFallbackCurrent)
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		cancel()
		// Return immediately so server cleanup cannot wait on a cancelled client.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	cfg := config.Get()
	cfg.OpenAICompatibleProviders[0].BaseURL = server.URL
	temperatureConfigLoader(t)(cfg)
	return primary, calls
}

func TestCompactionFallbackCancellationKeepsErrorIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	primary, fallbackCalls := compactionCancellationTestConfig(t, cancel)
	s := &ChatSession{ChatHistory: compactableHistory()}
	history := append([]types.Message(nil), s.ChatHistory...)
	_, _, _, err := compactContextWith(ctx, s, false, 4000, getCompactionResponse)
	if primary.calls.Load() != 1 || fallbackCalls.Load() != 1 || ctx.Err() != context.Canceled {
		t.Fatalf("cancellation scenario not reached: primary=%d fallback=%d context=%v", primary.calls.Load(), fallbackCalls.Load(), ctx.Err())
	}
	if !reflect.DeepEqual(history, s.ChatHistory) {
		t.Fatal("cancellation changed the history")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestSlashCompactionFallbackCancellationReportsInterrupt(t *testing.T) {
	sink := &recordSink{}
	s := &ChatSession{ChatHistory: compactableHistory(), Sink: sink}
	var interrupted atomic.Bool
	primary, fallbackCalls := compactionCancellationTestConfig(t, func() {
		s.GenerationCancelMu.Lock()
		cancel := s.GenerationCancel
		s.GenerationCancelMu.Unlock()
		if cancel != nil {
			interrupted.Store(true)
			cancel()
		}
	})
	history := append([]types.Message(nil), s.ChatHistory...)
	if handled, _ := handleSlashCommand("/compact", s); !handled {
		t.Fatal("/compact was not handled")
	}
	if !interrupted.Load() || primary.calls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("cancellation scenario not reached: interrupted=%v primary=%d fallback=%d", interrupted.Load(), primary.calls.Load(), fallbackCalls.Load())
	}
	if !reflect.DeepEqual(history, s.ChatHistory) || s.IsGenerating.Load() != 0 {
		t.Fatal("history changed or generation was not cleaned up")
	}
	if !strings.Contains(sink.text(), "Compaction interrupted.") || strings.Contains(sink.text(), "Error during compaction") {
		t.Fatalf("expected an interruption notice, got: %s", sink.text())
	}
}

func TestAutoCompactionAbortReportsSuppression(t *testing.T) {
	current, primary := fallbackTestConfig(t,
		config.CompactionModelConfig{Kind: "openai", Provider: "Broken", Model: "broken-model"},
		config.CompactionAbort)
	cfg := config.Get()
	cfg.CompactionThreshold = 1
	temperatureConfigLoader(t)(cfg)
	sink := &recordSink{}
	s := &ChatSession{ChatHistory: compactableHistory(), Sink: sink}
	history := append([]types.Message(nil), s.ChatHistory...)
	if !maybeAutoCompactWith(context.Background(), s, sink, false, getCompactionResponse) {
		t.Fatal("first auto-compaction was not attempted")
	}
	if !s.autoCompactSuppressed || !reflect.DeepEqual(history, s.ChatHistory) {
		t.Fatal("expected suspended auto-compaction and unchanged history")
	}
	output := sink.text()
	if maybeAutoCompactWith(context.Background(), s, sink, false, getCompactionResponse) {
		t.Fatal("suspended auto-compaction was attempted again")
	}
	if primary.calls.Load() != 1 || current.calls.Load() != 0 || sink.text() != output {
		t.Fatalf("unexpected requests or output: primary=%d current=%d", primary.calls.Load(), current.calls.Load())
	}
	if !strings.Contains(output, "policy: abort") || !strings.Contains(output, "Suspended for this context.") {
		t.Fatalf("expected the abort policy and suspension notice, got: %s", output)
	}
}

func TestCompactionFailureHint(t *testing.T) {
	for _, tc := range []struct {
		policy config.CompactionFailurePolicy
		auto   bool
		want   string
	}{
		{config.CompactionFallbackCurrent, false, " History untouched."},
		{config.CompactionFallbackCurrent, true, " Suspended for this context."},
		{config.CompactionAbort, false, " Compaction interrupted, history untouched (policy: abort)."},
		{config.CompactionAbort, true, " Compaction interrupted, history untouched (policy: abort). Suspended for this context."},
	} {
		cfg := config.Config{CompactionOnFailure: string(tc.policy)}
		if got := compactionFailureHint(cfg, tc.auto); got != tc.want {
			t.Errorf("policy=%s auto=%v: hint = %q, want %q", tc.policy, tc.auto, got, tc.want)
		}
	}
}
