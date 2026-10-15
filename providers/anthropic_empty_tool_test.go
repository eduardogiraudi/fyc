package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// TestConvertMessagesToAnthropicEmptyToolResult guards against the Anthropic
// API error "messages[N].content: missing field `text`". A tool that produces
// no output left msg.Content empty, and the unconditional text block marshaled
// to {"type":"text"} because Text carries omitempty.
func TestConvertMessagesToAnthropicEmptyToolResult(t *testing.T) {
	history := []types.Message{
		{Role: "user", Content: "run it"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{
			ID:   "toolu_1",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      "sh",
				Arguments: map[string]interface{}{"command": "true"},
			},
		}}},
		{Role: "tool", Name: "sh", ToolCallID: "toolu_1", Content: ""},
	}

	_, msgs := convertMessagesToAnthropic(history)

	b, err := json.Marshal(anthropicRequest{Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	req := string(b)

	// No text block may exist without its required "text" field.
	if strings.Contains(req, `{"type":"text"}`) {
		t.Errorf("request contains a text block missing the text field: %s", req)
	}
	// The tool_result itself must still be present and correctly addressed.
	if !strings.Contains(req, `"tool_use_id":"toolu_1"`) {
		t.Errorf("request lost the tool_result block: %s", req)
	}

	// A tool result with text must keep a proper text block.
	_, msgs2 := convertMessagesToAnthropic([]types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "t2", Function: types.ToolCallFunction{Name: "ls", Arguments: map[string]interface{}{}}}}},
		{Role: "tool", Name: "ls", ToolCallID: "t2", Content: "file.txt"},
	})
	b2, _ := json.Marshal(msgs2)
	if !strings.Contains(string(b2), `{"type":"text","text":"file.txt"}`) {
		t.Errorf("non-empty tool result lost its text block: %s", b2)
	}
}
