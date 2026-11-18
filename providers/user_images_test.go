package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"fuckyourcli/types"
)

func TestUserImagesReachAllTransports(t *testing.T) {
	msg := imageToolMessage()
	msg.Role, msg.Name, msg.ToolCallID = "user", "", ""
	for _, text := range []string{"inspect this", ""} {
		msg.Content = text
		_, anthropic := convertMessagesToAnthropic([]types.Message{msg})
		for _, tc := range []struct {
			name string
			data any
			want string
		}{
			{"anthropic", anthropic, `"type":"image"`},
			{"responses/codex", convertMessagesToCodexInput([]types.Message{msg}, ""), `"type":"input_image"`},
			{"completions", convertMessagesToOpenAI([]types.Message{msg}), `"type":"image_url"`},
			{"gemini", convertMessagesToGeminiNative([]types.Message{msg}, false), `"inlineData"`},
		} {
			b, err := json.Marshal(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if !strings.Contains(s, tc.want) || !strings.Contains(s, "QUJD") || strings.Contains(s, "omitted") || strings.Contains(s, "tool_result") || strings.Contains(s, "function_call_output") {
				t.Errorf("%s dropped user image: %s", tc.name, s)
			}
		}
	}
}
