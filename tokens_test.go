package main

import (
	"math"
	"strings"
	"testing"

	"fuckyourcli/types"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefgh", 2},
	}
	for _, c := range cases {
		if got := estimateTokens(c.text); got != c.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestEstimateMessageTokensContent(t *testing.T) {
	m := types.Message{Role: "user", Content: "abcdefgh"} // 2 content tokens + 4 overhead
	if got := estimateMessageTokens(m); got != 6 {
		t.Errorf("got %d, want 6", got)
	}
}

func TestEstimateMessageTokensToolCalls(t *testing.T) {
	m := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{{
			Function: types.ToolCallFunction{
				Name:         "sh",                    // 1 token
				ArgumentsRaw: strings.Repeat("a", 40), // 10 tokens
			},
		}},
	}
	if got, want := estimateMessageTokens(m), 1+10+4; got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokensImagesUseAreaNotBase64(t *testing.T) {
	// A huge base64 payload must not inflate the estimate: only the area counts.
	img := types.ImageAttachment{
		MIMEType: "image/jpeg",
		Data:     strings.Repeat("A", 200000),
		Width:    1024,
		Height:   768,
	}
	m := types.Message{Role: "tool", Images: []types.ImageAttachment{img}}
	want := int(math.Ceil(1024*768/750.0)) + 4
	if got := estimateMessageTokens(m); got != want {
		t.Errorf("got %d, want %d", got, want)
	}

	// Unknown dimensions fall back to a conservative constant.
	m2 := types.Message{Role: "tool", Images: []types.ImageAttachment{{Data: "x"}}}
	if got, want := estimateMessageTokens(m2), defaultImageTokens+4; got != want {
		t.Errorf("unknown dimensions: got %d, want %d", got, want)
	}
}

func TestEstimateToolsTokens(t *testing.T) {
	tools := []types.Tool{{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "ls",
			Description: strings.Repeat("a", 400),
		},
	}}
	if got := estimateToolsTokens(tools); got < 90 {
		t.Errorf("got %d, want >= 90 (description must be counted)", got)
	}
	if got := estimateToolsTokens(nil); got != 0 {
		t.Errorf("nil tools: got %d, want 0", got)
	}
}

func TestCurrentContextTokensNilSession(t *testing.T) {
	if got := currentContextTokens(nil); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestCurrentContextTokensIncludesHistoryAndTools(t *testing.T) {
	s := &ChatSession{
		ChatHistory: []types.Message{{Role: "system", Content: strings.Repeat("a", 400)}},
		AvailableTools: []types.Tool{{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "ls",
				Description: strings.Repeat("a", 400),
			},
		}},
	}
	if got := currentContextTokens(s); got < 150 {
		t.Errorf("got %d, want >= 150 (history + tools)", got)
	}
}

// contextHistory builds a history whose local estimate is roughly tokens.
func contextHistory(tokens int) []types.Message {
	return []types.Message{{Role: "user", Content: strings.Repeat("a", tokens*4)}}
}

func TestContextTokensUsesProviderMeasurementWhenPlausible(t *testing.T) {
	// ~1000 local tokens of history against a provider count of 2000: a factor
	// of two is normal tokenizer noise, so the provider drives the trigger.
	s := &ChatSession{ChatHistory: contextHistory(1000), lastProviderTokens: 2000, lastProviderMessages: 1}
	if got := contextTokens(s); got != 2000 {
		t.Fatalf("contextTokens = %d, want the provider measurement 2000", got)
	}
}

func TestContextTokensFallsBackWhenProviderMeasurementIsImplausible(t *testing.T) {
	// The provider reports 345 tokens (the uncached tail of a cached prompt, a
	// per-chunk count, a gateway echoing the last message) while the history is
	// ~100k tokens locally: an order-of-magnitude gap is not a tokenizer.
	s := &ChatSession{ChatHistory: contextHistory(100000), lastProviderTokens: 345, lastProviderMessages: 1}
	if got, want := contextTokens(s), currentContextTokens(s); got != want {
		t.Fatalf("contextTokens = %d, want the local estimate %d", got, want)
	}
}

func TestContextTokensAddsMessagesAppendedSinceTheRequest(t *testing.T) {
	// The measurement covers the request, not the response and its tool results:
	// those are appended afterwards and only the local estimate can size them.
	s := &ChatSession{
		ChatHistory:          append(contextHistory(1000), types.Message{Role: "tool", Content: strings.Repeat("b", 400)}),
		lastProviderTokens:   2000,
		lastProviderMessages: 1,
	}
	want := 2000 + estimateMessageTokens(s.ChatHistory[1])
	if got := contextTokens(s); got != want {
		t.Fatalf("contextTokens = %d, want %d (measurement + appended messages)", got, want)
	}
}

func TestContextTokensDiscardsMeasurementAfterHistoryRewrite(t *testing.T) {
	// A rewritten history (compaction, resume) invalidates a measurement taken
	// over a longer prefix.
	s := &ChatSession{ChatHistory: contextHistory(1000), lastProviderTokens: 4000, lastProviderMessages: 5}
	if got := providerContextTokens(s); got != 0 {
		t.Fatalf("providerContextTokens = %d, want 0", got)
	}
	if got, want := contextTokens(s), currentContextTokens(s); got != want {
		t.Fatalf("contextTokens = %d, want the local estimate %d", got, want)
	}
}

func TestRescaleContextTokens(t *testing.T) {
	// A local result is reported in the unit the trigger used.
	if got := rescaleContextTokens(1000, 4000, 2000); got != 2000 {
		t.Fatalf("rescale = %d, want 2000", got)
	}
	if got := rescaleContextTokens(1000, 3000, 3000); got != 1000 {
		t.Fatalf("identity rescale = %d, want 1000", got)
	}
	if got := rescaleContextTokens(1000, 0, 0); got != 1000 {
		t.Fatalf("no-measurement rescale = %d, want 1000", got)
	}
}

func TestTokenStatsLineShowsFullContextWhenItDiffersFromPrompt(t *testing.T) {
	// Anthropic reports the cached prompt separately: "prompt" alone would make
	// the context look tiny next to the compaction threshold.
	line := tokenStatsLine(&types.StreamResult{PromptTokens: 50, CompletionTokens: 7, CachedTokens: 100000, ContextTokens: 100050}, nil)
	if !strings.Contains(line, "context:") || !strings.Contains(line, "100050") {
		t.Fatalf("whole context missing from the stats line: %s", line)
	}
	plain := tokenStatsLine(&types.StreamResult{PromptTokens: 50, CompletionTokens: 7, ContextTokens: 50}, nil)
	if strings.Contains(plain, "context:") {
		t.Fatalf("an identical prompt/context must not be repeated: %s", plain)
	}
}
