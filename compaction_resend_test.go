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
)

// A compaction request whose stream is cut before any output must be resent on
// the same compactor, like a chat turn, instead of failing the whole compaction.
func TestCompactionResendsTransientStreamFailure(t *testing.T) {
	testWorkspace(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			// Accepted stream closed before any terminal event: transient.
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"## Goal\\ncontinue\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	// The compactor is the active entry, so the fallback policy has nothing to
	// switch to: only the resend can make this compaction succeed.
	temperatureConfigLoader(t)(config.Config{
		Provider: config.ProviderOpenAICompatible, Model: "chat-model", ActiveProviderName: "Work",
		OpenAICompatibleProviders: []config.OpenAICompatibleProviderConfig{
			{Name: "Work", BaseURL: srv.URL, Model: "chat-model"},
		},
		CompactionEnabled:   true,
		CompactionThreshold: 4000,
		CompactionModel:     config.CompactionModelConfig{Kind: "openai", Provider: "Work", Model: "chat-model"},
		CompactionOnFailure: string(config.CompactionFallbackCurrent),
	})
	s := &ChatSession{ChatHistory: compactableHistory()}

	_, _, res, err := compactContextWith(context.Background(), s, false, 4000, getCompactionResponse)
	if err != nil {
		t.Fatalf("compaction: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls=%d, want the failed request plus one resend", got)
	}
	if res.FellBack || len(res.Attempts) != 0 {
		t.Fatalf("resend must not count as a fallback: %+v", res)
	}
	if !strings.Contains(s.ChatHistory[1].Content, "## Goal") {
		t.Fatalf("summary not committed: %+v", s.ChatHistory)
	}
}
