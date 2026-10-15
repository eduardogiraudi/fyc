package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// A thinking block without its signature makes the Messages API reject the whole
// request ("thinking.signature: Field required"): the signature is what proves
// the block was produced by Claude. The history of a session carried over from
// another provider (or saved before the signature was tracked) holds reasoning
// text only, so that block must be dropped instead of poisoning every turn.
func TestConvertMessagesToAnthropicDropsUnsignedThinking(t *testing.T) {
	_, msgs := convertMessagesToAnthropic([]types.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer", ReasoningContent: "pondering"},
		{Role: "user", Content: "again"},
	})
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "thinking") || strings.Contains(string(b), "pondering") {
		t.Fatalf("unsigned thinking block must be dropped: %s", b)
	}
	// The turn itself must survive: only the block is dropped.
	if len(msgs) != 3 || msgs[1].Content[0].Type != "text" {
		t.Fatalf("assistant message should keep its text block: %+v", msgs)
	}
}

// A signed block is the one the model generated: it is kept verbatim, first in
// the assistant message, so reasoning continuity across tool calls survives.
func TestConvertMessagesToAnthropicKeepsSignedThinking(t *testing.T) {
	_, msgs := convertMessagesToAnthropic([]types.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer", ReasoningContent: "pondering", ReasoningSignature: "sig-abc"},
	})
	if len(msgs) != 2 || len(msgs[1].Content) != 2 {
		t.Fatalf("expected thinking + text blocks: %+v", msgs)
	}
	if msgs[1].Content[0].Type != "thinking" {
		t.Fatalf("thinking block must come first: %+v", msgs[1].Content)
	}
	if msgs[1].Content[0].Thinking != "pondering" || msgs[1].Content[0].Signature != "sig-abc" {
		t.Errorf("thinking block not preserved verbatim: %+v", msgs[1].Content[0])
	}
}
