package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/providers"
	"fuckyourcli/types"
)

// resendSink records spinner notices so a transparent resend stays observable.
type resendSink struct {
	retrySink
	spinners []string
}

func (s *resendSink) StartSpinner(label string) { s.spinners = append(s.spinners, label) }

func idleStreamFailure() error {
	return fmt.Errorf("chat completions stream interrupted: %w", providers.ErrStreamIdle)
}

// A request that fails before producing any output is resent automatically:
// nothing reached the user, no tool ran, so the retry cannot duplicate state.
func TestStreamResendRecoversFromSilentIdle(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	sink := &resendSink{}
	s := &ChatSession{Sink: sink, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	requests := 0
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		requests++
		if requests == 1 {
			return nil, idleStreamFailure()
		}
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	})

	streamProviderLoop(s, streamOptions{}, cfg, p, nil)

	if requests != 2 {
		t.Fatalf("want 2 sampling requests, got %d", requests)
	}
	if len(s.ChatHistory) != 2 || s.ChatHistory[1].Content != "done" {
		t.Fatalf("resend polluted history: %+v", s.ChatHistory)
	}
	if s.retryRequest != nil || strings.Contains(sink.text(), "/retry") {
		t.Fatal("a recovered request was left retryable")
	}
	if len(sink.spinners) == 0 || !strings.Contains(sink.spinners[0], "provider idle") {
		t.Fatalf("resend was not announced: %v", sink.spinners)
	}
}

// Once text is on screen the stream is partially consumed: replaying it would
// duplicate the answer, so the failure stays manual.
func TestStreamResendStopsOnceOutputIsVisible(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	sink := &resendSink{}
	s := &ChatSession{Sink: sink, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	requests := 0
	p := compactionProviderFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, out types.OutputSink) (*types.StreamResult, error) {
		requests++
		out.WriteText("partial")
		return nil, idleStreamFailure()
	})

	streamProviderLoop(s, streamOptions{}, cfg, p, nil)

	if requests != 1 {
		t.Fatalf("a partially consumed stream was replayed: %d requests", requests)
	}
	if !strings.Contains(sink.text(), "partial") || !strings.Contains(sink.text(), "/retry") {
		t.Fatalf("failure was not surfaced: %s", sink.text())
	}
	if s.retryRequest == nil {
		t.Fatal("manual retry was not offered")
	}
	if len(s.ChatHistory) != 1 {
		t.Fatalf("failed attempt leaked into history: %+v", s.ChatHistory)
	}
}

func TestStreamResendStopsAfterBudget(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	s := &ChatSession{Sink: &resendSink{}, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	requests := 0
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		requests++
		return nil, idleStreamFailure()
	})

	streamProviderLoop(s, streamOptions{}, cfg, p, nil)

	if requests != providers.MaxStreamResends+1 {
		t.Fatalf("want %d attempts, got %d", providers.MaxStreamResends+1, requests)
	}
	if s.retryRequest == nil {
		t.Fatal("exhausted resends did not offer /retry")
	}
}

func TestStreamResendAbortsOnCancellation(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	s := &ChatSession{Sink: &resendSink{}, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	requests := 0
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		requests++
		s.GenerationCancelMu.Lock()
		cancel := s.GenerationCancel
		s.GenerationCancelMu.Unlock()
		if cancel == nil {
			t.Fatal("turn cancel func missing")
		}
		cancel()
		return nil, idleStreamFailure()
	})

	streamProviderLoop(s, streamOptions{}, cfg, p, nil)

	if requests != 1 {
		t.Fatalf("a cancelled turn was resent: %d requests", requests)
	}
	if s.retryRequest != nil {
		t.Fatal("user cancellation became retryable")
	}
}

// End to end over the real provider client: a response body truncated before
// any event reaches the user is resent, and the recovered answer is the only
// one that lands in the history.
func TestStreamResendRecoversFromDroppedConnection(t *testing.T) {
	testWorkspace(t)
	t.Setenv("HOME", t.TempDir())
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Announce an event stream, then drop the TCP connection without a
			// terminal event (a tunnel dying mid-request).
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := temperatureConfig(config.ProviderOpenAICompatible, "test", "default")
	cfg.OpenAICompatibleProviders[0].BaseURL = server.URL
	temperatureConfigLoader(t)(cfg)

	sink := &resendSink{}
	s := &ChatSession{Sink: sink, ChatHistory: []types.Message{{Role: "system", Content: "sys"}}}
	if !s.ProcessInput("hello") {
		t.Fatal("turn did not run")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want 2 sampling requests, got %d", got)
	}
	if len(s.ChatHistory) != 3 || s.ChatHistory[2].Content != "recovered" {
		t.Fatalf("resend polluted history: %+v", s.ChatHistory)
	}
	if s.retryRequest != nil || strings.Contains(sink.text(), "Error:") {
		t.Fatalf("recovered turn reported a failure: %s", sink.text())
	}
}
