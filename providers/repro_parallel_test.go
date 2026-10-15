package providers

import (
	"encoding/json"
	"testing"

	"fuckyourcli/types"
)

// TestConvertMessagesToAnthropicParallelToolResults verifies that the tool_result
// blocks answering a parallel tool_use turn are coalesced into a single user
// message. Splitting them into separate user messages makes Anthropic reject the
// request with "tool_use ids were found without tool_result blocks immediately after".
func TestConvertMessagesToAnthropicParallelToolResults(t *testing.T) {
	history := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "guardati PLAN.md"},
		{Role: "assistant", ToolCalls: []types.ToolCall{
			{ID: "call_00_UqPdBhJNDAeYgWBt6Hxj2728", Type: "function", Function: types.ToolCallFunction{Name: "ls", Arguments: map[string]interface{}{"path": "."}}},
			{ID: "call_01_5BaxQbjyGVzAHgXgnHCY0188", Type: "function", Function: types.ToolCallFunction{Name: "read", Arguments: map[string]interface{}{"path": "PLAN.md"}}},
		}},
		{Role: "tool", Name: "ls", ToolCallID: "call_00_UqPdBhJNDAeYgWBt6Hxj2728", Content: "ls-output"},
		{Role: "tool", Name: "read", ToolCallID: "call_01_5BaxQbjyGVzAHgXgnHCY0188", Content: "plan-content"},
	}
	_, msgs := convertMessagesToAnthropic(history)
	for i, m := range msgs {
		b, _ := json.Marshal(m)
		t.Logf("messages[%d] = %s", i, b)
	}
	// user, assistant, single coalesced user with both tool_results.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("last message = %+v, want user with 2 tool_result blocks", last)
	}
	for i, want := range []string{"call_00_UqPdBhJNDAeYgWBt6Hxj2728", "call_01_5BaxQbjyGVzAHgXgnHCY0188"} {
		if last.Content[i].Type != "tool_result" || last.Content[i].ToolUseID != want {
			t.Errorf("tool_result[%d] = %+v, want tool_use_id %s", i, last.Content[i], want)
		}
	}
}
