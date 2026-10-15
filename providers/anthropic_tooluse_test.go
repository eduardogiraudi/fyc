package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// TestConvertMessagesToAnthropicToolUseEmptyInput guards against the Anthropic
// API error "content: missing field input": a tool_use block must always carry
// an "input" object, even when the tool takes no arguments (empty map).
func TestConvertMessagesToAnthropicToolUseEmptyInput(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{
			ID:   "toolu_1",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      "read_user_preferences",
				Arguments: map[string]interface{}{},
			},
		}}},
	}
	_, converted := convertMessagesToAnthropic(msgs)
	b, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"input":{}`) {
		t.Errorf("tool_use block missing empty input object: %s", s)
	}

	// Ensure the empty input survives a full round-trip through the request type.
	req, err := json.Marshal(anthropicRequest{Messages: converted})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req), `"input":{}`) {
		t.Errorf("request lost empty input object: %s", req)
	}
}

// TestConvertMessagesToAnthropicTextBlocksOmitInput ensures non-tool_use blocks
// do not carry the unused "input" field.
func TestConvertMessagesToAnthropicTextBlocksOmitInput(t *testing.T) {
	_, converted := convertMessagesToAnthropic([]types.Message{{Role: "user", Content: "hello"}})
	b, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"input"`) {
		t.Errorf("text block should not contain input field: %s", b)
	}
}
