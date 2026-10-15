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
