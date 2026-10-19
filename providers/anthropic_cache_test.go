package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fuckyourcli/reasoning"
	"fuckyourcli/types"
)

// anthropicCachedStream is a complete stream whose usage reports both a cache
// write (the turn that fills the cache) and a cache read (a later turn).
func anthropicCachedStream() string {
	var b strings.Builder
	b.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":900}}}`))
	b.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	b.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`))
	b.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
	b.WriteString(anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2,"cache_read_input_tokens":900}}`))
	b.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
	return b.String()
}

// resetAnthropicCache restores the process-wide cache level so one test cannot
// make the next one silently skip a breakpoint.
func resetAnthropicCache(t *testing.T) {
	t.Helper()
	anthropicCacheLevel.Store(anthropicCacheAuto)
	t.Setenv("FYC_ANTHROPIC_NO_CACHE", "")
	t.Cleanup(func() { anthropicCacheLevel.Store(anthropicCacheAuto) })
}

func anthropicSSEServer(t *testing.T, bodies *[][]byte, handle func(w http.ResponseWriter, body []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bodies != nil {
			*bodies = append(*bodies, body)
		}
		handle(w, body)
	}))
}

// Without the top-level cache_control every turn re-pays the whole static
// prefix (tool schemas + system prompt) at the full input price.
func TestAnthropicAutomaticPromptCaching(t *testing.T) {
	resetAnthropicCache(t)

	var bodies [][]byte
	srv := anthropicSSEServer(t, &bodies, func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicCachedStream())
	})
	defer srv.Close()

	p := NewAnthropicProvider(srv.URL, "k", "claude-sonnet-4-5", "anthropic", 0.2)
	tools := []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "read", Description: "r"}}}
	res, err := p.StreamChat(context.Background(), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}, tools, NewSilentOutputSink())
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(bodies[0], &req); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, bodies[0])
	}
	cc, ok := req["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("top-level cache_control missing: %s", bodies[0])
	}
	// The static prefix is pinned block by block too: the tool schemas and the
	// system prompt never change, so they must survive a tail that diverges.
	toolsOut, _ := req["tools"].([]any)
	if len(toolsOut) != 1 {
		t.Fatalf("tools missing: %s", bodies[0])
	}
	tool0, _ := toolsOut[0].(map[string]any)
	if cc, _ := tool0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last tool is not pinned: %s", bodies[0])
	}
	system, _ := req["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("system must be an array of blocks: %s", bodies[0])
	}
	sys0, _ := system[0].(map[string]any)
	if sys0["text"] != "sys" {
		t.Fatalf("system text lost: %s", bodies[0])
	}
	if cc, _ := sys0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last system block is not pinned: %s", bodies[0])
	}
	if res.CachedTokens != 900 || res.CacheWriteTokens != 900 {
		t.Fatalf("cache usage not parsed: read=%d write=%d", res.CachedTokens, res.CacheWriteTokens)
	}
}

func TestAnthropicPromptCachingOptOut(t *testing.T) {
	resetAnthropicCache(t)
	t.Setenv("FYC_ANTHROPIC_NO_CACHE", "1")

	var bodies [][]byte
	srv := anthropicSSEServer(t, &bodies, func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicCachedStream())
	})
	defer srv.Close()

	p := NewAnthropicProvider(srv.URL, "k", "claude-sonnet-4-5", "anthropic", 0.2)
	if _, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink()); err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if bytes := string(bodies[0]); strings.Contains(bytes, "cache_control") {
		t.Fatalf("opt-out still sent cache_control: %s", bytes)
	}
}

// A gateway that refuses every cache_control steps down one level per retry
// (automatic → explicit blocks → none) instead of losing the turn.
func TestAnthropicPromptCachingRejectedFallsBack(t *testing.T) {
	resetAnthropicCache(t)

	var bodies [][]byte
	srv := anthropicSSEServer(t, &bodies, func(w http.ResponseWriter, body []byte) {
		if strings.Contains(string(body), `"cache_control"`) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"cache_control: Extra inputs are not permitted"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicCachedStream())
	})
	defer srv.Close()

	p := NewAnthropicProvider(srv.URL, "k", "claude-sonnet-4-5", "anthropic", 0.2)
	res, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink())
	if err != nil {
		t.Fatalf("a refused cache_control must not fail the turn: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q, want ok", res.Content)
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3 (automatic, blocks, none)", len(bodies))
	}
	if got := int(anthropicCacheLevel.Load()); got != anthropicCacheNone {
		t.Fatalf("level = %d, want %d (none)", got, anthropicCacheNone)
	}
	if strings.Contains(string(bodies[2]), "cache_control") {
		t.Fatalf("last retry still carries cache_control: %s", bodies[2])
	}

	// Following turns must not probe the gateway again.
	if _, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if len(bodies) != 4 || strings.Contains(string(bodies[3]), "cache_control") {
		t.Fatalf("a later turn asked for caching again: %s", bodies[3])
	}
}

// A gateway that only refuses the top-level automatic field keeps the explicit
// breakpoints: the static prefix stays cached, and without the automatic
// breakpoint the tail is pinned explicitly.
func TestAnthropicPromptCachingTopLevelRejectedKeepsBlocks(t *testing.T) {
	resetAnthropicCache(t)

	var bodies [][]byte
	srv := anthropicSSEServer(t, &bodies, func(w http.ResponseWriter, body []byte) {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		if _, ok := req["cache_control"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"cache_control is not supported on this endpoint"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicCachedStream())
	})
	defer srv.Close()

	p := NewAnthropicProvider(srv.URL, "k", "claude-sonnet-4-5", "anthropic", 0.2)
	tools := []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "read", Description: "r"}}}
	if _, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, tools, NewSilentOutputSink()); err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2 (automatic then blocks)", len(bodies))
	}
	if got := int(anthropicCacheLevel.Load()); got != anthropicCacheBlocks {
		t.Fatalf("level = %d, want %d (blocks)", got, anthropicCacheBlocks)
	}

	var req map[string]any
	if err := json.Unmarshal(bodies[1], &req); err != nil {
		t.Fatalf("retry body is not JSON: %v", err)
	}
	if _, ok := req["cache_control"]; ok {
		t.Fatalf("retry still asks for automatic caching: %s", bodies[1])
	}
	toolsOut, _ := req["tools"].([]any)
	if len(toolsOut) != 1 {
		t.Fatalf("tools missing from the retry: %s", bodies[1])
	}
	tool0, _ := toolsOut[0].(map[string]any)
	if cc, _ := tool0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("the retry dropped the tool breakpoint too: %s", bodies[1])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("messages missing from the retry: %s", bodies[1])
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("last message has no content: %s", bodies[1])
	}
	block, _ := blocks[len(blocks)-1].(map[string]any)
	if cc, _ := block["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("the rolling tail breakpoint is missing: %s", bodies[1])
	}
}

// A user-provided extra_body cache_control is merged into the same field (no
// duplicate key) and dropped by the fallback retry as well.
func TestAnthropicPromptCachingExtraBodyMergedAndDropped(t *testing.T) {
	resetAnthropicCache(t)

	var bodies [][]byte
	srv := anthropicSSEServer(t, &bodies, func(w http.ResponseWriter, body []byte) {
		if strings.Contains(string(body), `"cache_control"`) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"cache_control: Extra inputs are not permitted"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicCachedStream())
	})
	defer srv.Close()

	p := NewAnthropicProvider(srv.URL, "k", "claude-sonnet-4-5", "anthropic", 0.2)
	p.SetReasoning(reasoning.Intent{
		Mode:      reasoning.ModeDefault,
		ExtraBody: map[string]any{"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
	}, nil)
	if _, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink()); err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var first map[string]any
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatalf("first body is not JSON: %v", err)
	}
	cc, ok := first["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" || cc["ttl"] != "1h" {
		t.Fatalf("extra_body cache_control not merged into the request: %s", bodies[0])
	}
	if len(bodies) != 3 || strings.Contains(string(bodies[2]), "cache_control") {
		t.Fatalf("the fallback must drop the extra_body entry too: %s", bodies[len(bodies)-1])
	}
}
