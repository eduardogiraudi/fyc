package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// imageToolMessage builds a tool result carrying a screenshot attachment,
// as produced by the computer-use tool.
func imageToolMessage() types.Message {
	return types.Message{
		Role:       "tool",
		Name:       "computer",
		ToolCallID: "call_1",
		Content:    "screenshot captured",
		Images: []types.ImageAttachment{{
			MIMEType: "image/jpeg",
			Data:     "QUJD",
			Width:    2,
			Height:   1,
		}},
	}
}

func TestConvertMessagesToAnthropicWithImage(t *testing.T) {
	_, msgs := convertMessagesToAnthropic([]types.Message{imageToolMessage()})
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"type":"tool_result"`, `"type":"image"`, `"media_type":"image/jpeg"`, `"data":"QUJD"`, `"type":"text"`} {
		if !strings.Contains(s, want) {
			t.Errorf("anthropic conversion missing %s in %s", want, s)
		}
	}
}

func TestConvertMessagesToCodexInputWithImage(t *testing.T) {
	input := convertMessagesToCodexInput([]types.Message{imageToolMessage()}, "gpt-5.5")
	if len(input) != 1 {
		t.Fatalf("input length = %d, want 1", len(input))
	}
	if input[0]["type"] != "function_call_output" {
		t.Fatalf("type = %v, want function_call_output", input[0]["type"])
	}
	arr, ok := input[0]["output"].([]map[string]interface{})
	if !ok {
		t.Fatalf("output is %T, want []map[string]interface{}", input[0]["output"])
	}
	if len(arr) != 2 {
		t.Fatalf("output items = %d, want 2 (image + text)", len(arr))
	}
	if arr[0]["type"] != "input_image" {
		t.Errorf("first item type = %v, want input_image", arr[0]["type"])
	}
	url, _ := arr[0]["image_url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,QUJD") {
		t.Errorf("image_url = %q, want data URL with base64 payload", url)
	}
	if arr[1]["type"] != "input_text" || arr[1]["text"] != "screenshot captured" {
		t.Errorf("second item = %v, want input_text with original content", arr[1])
	}
}

func TestConvertMessagesToCodexInputPlainText(t *testing.T) {
	msg := imageToolMessage()
	msg.Images = nil
	input := convertMessagesToCodexInput([]types.Message{msg}, "gpt-5.5")
	if _, ok := input[0]["output"].(string); !ok {
		t.Errorf("plain tool output should stay a string, got %T", input[0]["output"])
	}
}

func TestConvertMessagesToOpenAIStripsImages(t *testing.T) {
	msgs := convertMessagesToOpenAI([]types.Message{imageToolMessage()})
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "image omitted") {
		t.Errorf("content = %q, want placeholder mentioning omitted image", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, "QUJD") {
		t.Error("base64 payload leaked into the completions message")
	}
}

func TestConvertMessagesToGeminiWithImage(t *testing.T) {
	with := convertMessagesToGeminiNative([]types.Message{imageToolMessage()}, true)
	if len(with) != 1 || len(with[0].Parts) != 1 || with[0].Parts[0].FunctionResponse == nil {
		t.Fatalf("unexpected gemini conversion: %+v", with)
	}
	fr := with[0].Parts[0].FunctionResponse
	if len(fr.Parts) != 1 || fr.Parts[0].InlineData == nil {
		t.Fatalf("expected one inlineData part, got %+v", fr.Parts)
	}
	if fr.Parts[0].InlineData.MimeType != "image/jpeg" || fr.Parts[0].InlineData.Data != "QUJD" {
		t.Errorf("inlineData = %+v, want jpeg QUJD", fr.Parts[0].InlineData)
	}

	without := convertMessagesToGeminiNative([]types.Message{imageToolMessage()}, false)
	if len(without) != 1 || without[0].Parts[0].FunctionResponse == nil {
		t.Fatalf("unexpected gemini conversion (no images): %+v", without)
	}
	fr2 := without[0].Parts[0].FunctionResponse
	if len(fr2.Parts) != 0 {
		t.Errorf("expected no inlineData parts for unsupported model, got %+v", fr2.Parts)
	}
	res, _ := fr2.Response["result"].(string)
	if !strings.Contains(res, "screenshot omitted") {
		t.Errorf("result = %q, want placeholder mentioning omitted screenshot", res)
	}
}

// TestGeminiToolTurnUsesUserRole verifies that function responses are emitted on
// a "user" turn. Gemini's v1beta API only accepts "user" or "model" as the
// Content.role; sending "function" makes the API reject the whole request with
// 400 INVALID_ARGUMENT "Request contains an invalid argument.".
func TestGeminiToolTurnUsesUserRole(t *testing.T) {
	contents := convertMessagesToGeminiNative([]types.Message{imageToolMessage()}, false)
	if len(contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(contents))
	}
	if contents[0].Role != "user" {
		t.Errorf("tool turn role = %q, want \"user\"", contents[0].Role)
	}
	if len(contents[0].Parts) != 1 || contents[0].Parts[0].FunctionResponse == nil {
		t.Fatalf("expected exactly one functionResponse part, got %+v", contents[0].Parts)
	}
}
