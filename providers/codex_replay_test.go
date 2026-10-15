package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fuckyourcli/reasoning"
	"fuckyourcli/types"
)

// replayBlob is the fake encrypted reasoning state the mock server hands out.
const replayBlob = "gAAAAABmock-encrypted-reasoning-blob"

// TestCodexReasoningReplayEndToEnd drives the whole Codex path against a local
// server that speaks the Responses SSE protocol: request → SSE parse → history
// → replayed request. It is the deterministic counterpart of
// TestLiveCodexEncryptedReasoningReplay (root package), which needs a real
// account: here the API is simulated, but every line of fyc's side is real
// (HTTP, framing, parsing, history, request building).
//
// What it pins:
//  1. an active reasoning mode asks for include=["reasoning.encrypted_content"]
//     and the reasoning field itself;
//  2. the encrypted item returned in response.completed is exposed as
//     StreamResult.ReasoningItem, along with the reasoning summary and usage;
//  3. replaying it puts the very same item (id + blob) back in the next
//     request's input array;
//  4. a blob the server rejects surfaces as an error (no silent retry).
func TestCodexReasoningReplayEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		// Any blob the server cannot decrypt fails the whole turn (a request can
		// carry several reasoning items: one per assistant turn replayed).
		tampered := false
		replayed := false
		for _, blob := range reasoningBlobs(body) {
			if blob == replayBlob {
				replayed = true
				continue
			}
			tampered = true
		}
		if tampered {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid encrypted_content"}}`))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flush := func() {}
		if f, ok := w.(http.Flusher); ok {
			flush = f.Flush
		}
		if replayed {
			// Replay turn: the server already has the reasoning state.
			sseEvent(w, `{"type":"response.output_text.delta","delta":"OK"}`)
			sseEvent(w, `{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":1},"output":[{"type":"message","id":"msg_2"}]}}`)
		} else {
			sseEvent(w, `{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1","encrypted_content":"`+replayBlob+`","summary":[]}}`)
			sseEvent(w, `{"type":"response.reasoning_summary_text.delta","delta":"weighing the options"}`)
			sseEvent(w, `{"type":"response.output_text.delta","delta":"Yes."}`)
			sseEvent(w, `{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":2},"output":[{"type":"reasoning","id":"rs_1","encrypted_content":"`+replayBlob+`","summary":[]},{"type":"message","id":"msg_1"}]}}`)
		}
		sseEvent(w, "[DONE]")
		flush()
	}))
	defer srv.Close()

	prevBase := codexBaseURL
	codexBaseURL = srv.URL
	t.Cleanup(func() { codexBaseURL = prevBase })

	p := NewCodexProvider("test-token", "codex-auto-review", 0, false, "")
	p.SetReasoning(reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "high"}, nil)
	sink := NewSilentOutputSink()
	ctx := context.Background()

	// ── Turn 1 ───────────────────────────────────────────────────────────────
	history := []types.Message{{Role: "user", Content: "is 2^10 larger than 10^3?"}}
	t1, err := p.StreamChat(ctx, history, nil, sink)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if t1.Content != "Yes." {
		t.Fatalf("content = %q, want %q", t1.Content, "Yes.")
	}
	if t1.ReasoningContent != "weighing the options" {
		t.Fatalf("reasoning = %q", t1.ReasoningContent)
	}
	if !strings.Contains(t1.ReasoningItem, replayBlob) || !strings.Contains(t1.ReasoningItem, `"rs_1"`) {
		t.Fatalf("encrypted reasoning item not captured: %q", t1.ReasoningItem)
	}
	if t1.PromptTokens != 10 || t1.CompletionTokens != 2 {
		t.Fatalf("usage not parsed: prompt=%d completion=%d", t1.PromptTokens, t1.CompletionTokens)
	}

	// Turn 1 must both ask for the encrypted state and send the effort.
	if !includeHas(bodies[0], "reasoning.encrypted_content") {
		t.Fatalf("turn 1 did not request the encrypted state: %v", bodies[0]["include"])
	}
	if effort := reasoningEffort(bodies[0]); effort != "high" {
		t.Fatalf("reasoning.effort = %q, want %q", effort, "high")
	}

	// ── Turn 2: replay the item verbatim ────────────────────────────────────
	history = append(history,
		types.Message{Role: "assistant", Content: t1.Content, ReasoningContent: t1.ReasoningContent, ReasoningItem: t1.ReasoningItem},
		types.Message{Role: "user", Content: "reply with OK"},
	)
	t2, err := p.StreamChat(ctx, history, nil, sink)
	if err != nil {
		t.Fatalf("turn 2 (replay): %v", err)
	}
	if strings.TrimSpace(t2.Content) != "OK" {
		t.Fatalf("turn 2 content = %q", t2.Content)
	}
	replayed := reasoningItem(bodies[1])
	if replayed == nil {
		t.Fatal("the replayed request carried no reasoning item")
	}
	if replayed["id"] != "rs_1" || replayed["encrypted_content"] != replayBlob {
		t.Fatalf("the replayed item was altered: %v", replayed)
	}

	// ── Turn 3: a blob the server refuses must surface as an error ───────────
	history = append(history,
		types.Message{Role: "assistant", Content: t2.Content, ReasoningItem: tamperReasoningItem(t1.ReasoningItem)},
		types.Message{Role: "user", Content: "reply with OK"},
	)
	if _, err := p.StreamChat(ctx, history, nil, sink); err == nil {
		t.Fatal("a rejected blob must fail the turn")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("unexpected error for a rejected blob: %v", err)
	}
}

// sseEvent writes one SSE data line (the only framing the parser relies on).
func sseEvent(w io.Writer, payload string) {
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

// reasoningItem returns the first reasoning item of a request's input array.
func reasoningItem(body map[string]any) map[string]any {
	input, _ := body["input"].([]any)
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := item["type"].(string); kind == "reasoning" {
			return item
		}
	}
	return nil
}

// reasoningBlobs collects the encrypted content of every reasoning item in the
// request, in order.
func reasoningBlobs(body map[string]any) []string {
	var blobs []string
	input, _ := body["input"].([]any)
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := item["type"].(string); kind != "reasoning" {
			continue
		}
		if blob, ok := item["encrypted_content"].(string); ok && blob != "" {
			blobs = append(blobs, blob)
		}
	}
	return blobs
}

func includeHas(body map[string]any, want string) bool {
	arr, _ := body["include"].([]any)
	for _, v := range arr {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

func reasoningEffort(body map[string]any) string {
	r, _ := body["reasoning"].(map[string]any)
	effort, _ := r["effort"].(string)
	return effort
}

// tamperReasoningItem flips bits of the encrypted content of every reasoning
// item, keeping the items otherwise well formed. It accepts both shapes the
// history can hold (a single object or an array) and always returns an array.
func tamperReasoningItem(item string) string {
	var items []map[string]any
	if strings.HasPrefix(strings.TrimSpace(item), "[") {
		if json.Unmarshal([]byte(item), &items) != nil {
			return item
		}
	} else {
		var m map[string]any
		if json.Unmarshal([]byte(item), &m) != nil {
			return item
		}
		items = []map[string]any{m}
	}
	for _, m := range items {
		blob, _ := m["encrypted_content"].(string)
		if blob == "" {
			continue
		}
		flipped := []byte(blob)
		flipped[0] ^= 0x01
		m["encrypted_content"] = string(flipped)
	}
	out, err := json.Marshal(items)
	if err != nil {
		return item
	}
	return string(out)
}
