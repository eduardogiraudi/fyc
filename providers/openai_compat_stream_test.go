package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"fuckyourcli/types"
)

// TestReasoningContentAliases pins the three names the same reasoning text
// travels under on an OpenAI-compatible stream: reasoning_content (DeepSeek and
// most local servers), reasoning (some gateways) and thinking (Ollama's own
// protocol). Missing one of them means the user sees no thinking at all even
// though the model produced it.
func TestReasoningContentAliases(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "thinking"} {
		t.Run(field, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{%q:\"weighing\"}}]}\n\n", field)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Yes.\"}}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()

			p := NewOpenAICompatibleProvider(srv.URL, "", "gemma4:31b-cloud", "ollama", 0.1, false, "")
			res, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink())
			if err != nil {
				t.Fatalf("StreamChat: %v", err)
			}
			if res.ReasoningContent != "weighing" {
				t.Fatalf("reasoning not captured from %q: %+v", field, res)
			}
			if res.Content != "Yes." {
				t.Fatalf("content = %q, want %q", res.Content, "Yes.")
			}
		})
	}
}

func TestReasoningDeltaPrecedence(t *testing.T) {
	if got := (OpenAIMessage{Content: "x"}).reasoningDelta(); got != "" {
		t.Fatalf("content must not be read as reasoning: %q", got)
	}
	if got := (OpenAIMessage{Thinking: "t"}).reasoningDelta(); got != "t" {
		t.Fatalf("thinking alias not read: %q", got)
	}
	// reasoning_content wins when a server sends more than one of them.
	both := OpenAIMessage{ReasoningContent: "rc", Reasoning: "r", Thinking: "t"}
	if got := both.reasoningDelta(); got != "rc" {
		t.Fatalf("precedence = %q, want rc", got)
	}
}
