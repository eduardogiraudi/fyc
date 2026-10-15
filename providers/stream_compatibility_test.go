package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fuckyourcli/types"
)

func TestOpenAICompatibleTerminalMarkers(t *testing.T) {
	for _, tc := range []struct {
		name, ending string
	}{
		{"plain_eof", ""},
		{"finish_reason_only", `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"},
		{"done_only", "data: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"answer"}}]}`+"\n\n"+tc.ending)
			}))
			defer srv.Close()
			provider := NewOpenAICompatibleProvider(srv.URL, "", "probe", "proxy", 0, false, "")
			result, err := provider.StreamChat(context.Background(), nil, nil, NewSilentOutputSink())
			if err != nil || result == nil || result.Content != "answer" || len(result.ToolCalls) != 0 || result.Interrupted {
				t.Fatalf("text response rejected: %+v, %v", result, err)
			}
		})
	}
}

func TestOpenAICompatibleEOFSafety(t *testing.T) {
	chunk := func(delta map[string]any) string {
		data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		if err != nil {
			t.Fatal(err)
		}
		return "data: " + string(data) + "\n\n"
	}
	text := chunk(map[string]any{"content": "answer"})
	nativeTool := func(arguments string) string {
		return chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "sh", "arguments": arguments},
		}}})
	}
	for _, tc := range []struct {
		name, stream string
		readErr      error
		wantError    bool
	}{
		{"text", text, io.EOF, false},
		{"text_with_reasoning", chunk(map[string]any{"reasoning_content": "thinking"}) + text, io.EOF, false},
		{"text_with_usage", text + "data: " + `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}` + "\n\n", io.EOF, false},
		{"empty", "", io.EOF, true},
		{"role_only", chunk(map[string]any{"role": "assistant"}), io.EOF, true},
		{"whitespace", chunk(map[string]any{"content": " \n\t "}), io.EOF, true},
		{"reasoning_only", chunk(map[string]any{"reasoning_content": "thinking"}), io.EOF, true},
		{"native_tool", text + nativeTool(`{"command":"true"}`), io.EOF, true},
		{"partial_native_tool", text + nativeTool(`{"command":"`), io.EOF, true},
		{"xml_tool", text + chunk(map[string]any{"reasoning_content": "<tool_call><function=sh><parameter=command>true</parameter></function></tool_call>"}), io.EOF, true},
		{"partial_xml_tool", text + chunk(map[string]any{"reasoning_content": "<tool_call><function=sh><parameter=command>"}), io.EOF, true},
		{"invalid_event", text + "data: {\"choices\":\n\n", io.EOF, true},
		{"disconnect", text, io.ErrUnexpectedEOF, true},
		{"deadline", text, context.DeadlineExceeded, true},
		{"oversized_line", text + "data: " + strings.Repeat("x", maxStreamLineBytes) + "\n\n", io.EOF, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := nopCloser{&errReader{data: []byte(tc.stream), err: tc.readErr}}
			result, err := parseOpenAIStreamResponse(context.Background(), body, false, NewSilentOutputSink())
			if tc.wantError {
				if err == nil || result != nil {
					t.Fatalf("unsafe EOF accepted: %+v, %v", result, err)
				}
				if tc.readErr != io.EOF && !errors.Is(err, tc.readErr) {
					t.Fatalf("read error lost: %v", err)
				}
				if tc.name == "oversized_line" && !errors.Is(err, bufio.ErrTooLong) {
					t.Fatalf("scanner error lost: %v", err)
				}
				return
			}
			if err != nil || result == nil || result.Content != "answer" || len(result.ToolCalls) != 0 || result.Interrupted || result.FinishReason != "" {
				t.Fatalf("compatible EOF rejected or altered: %+v, %v", result, err)
			}
			if tc.name == "text_with_reasoning" && result.ReasoningContent != "thinking" {
				t.Fatalf("reasoning lost: %q", result.ReasoningContent)
			}
			if tc.name == "text_with_usage" && (result.PromptTokens != 12 || result.CompletionTokens != 3) {
				t.Fatalf("usage lost: %+v", result)
			}
		})
	}
}

func TestStreamToolCallsEmptyArguments(t *testing.T) {
	for _, fixture := range streamFixtures() {
		if fixture.name == "gemini" {
			continue
		}
		for _, raw := range []string{"null", " \nnull\t ", "{}", ""} {
			t.Run(fixture.name+"/"+fmt.Sprintf("%q", raw), func(t *testing.T) {
				var stream string
				switch fixture.name {
				case "responses":
					stream = responsesToolEvent("response.completed", map[string]any{"response": map[string]any{"output": []any{map[string]any{
						"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "ping", "arguments": raw,
					}}}})
				case "openai":
					stream = "data: " + fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ping","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, raw) + "\n\n"
				case "anthropic":
					stream = anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"ping","input":{}}}`) +
						anthropicSSE("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"partial_json":%q}}`, raw)) +
						anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
						anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
						anthropicSSE("message_stop", `{"type":"message_stop"}`)
				}
				result, err := fixture.parse(context.Background(), io.NopCloser(strings.NewReader(stream)), NewSilentOutputSink())
				if err != nil || result == nil || len(result.ToolCalls) != 1 {
					t.Fatalf("empty arguments rejected: %+v, %v", result, err)
				}
				fn := result.ToolCalls[0].Function
				if fn.Arguments == nil || len(fn.Arguments) != 0 || fn.ArgumentsRaw != raw {
					t.Fatalf("arguments not normalized or raw lost: %+v", fn)
				}
				history := []types.Message{{Role: "assistant", ToolCalls: result.ToolCalls}}
				wantRaw := raw
				if wantRaw == "" {
					wantRaw = "{}"
				}
				if got := convertMessagesToOpenAI(history)[0].ToolCalls[0].Function.Arguments; got != wantRaw {
					t.Fatalf("chat replay arguments = %q, want %q", got, wantRaw)
				}
				if got := convertMessagesToCodexInput(history, "probe")[0]["arguments"]; got != wantRaw {
					t.Fatalf("responses replay arguments = %q, want %q", got, wantRaw)
				}
				_, messages := convertMessagesToAnthropic(history)
				input := messages[0].Content[0].Input
				if input == nil || *input == nil || len(*input) != 0 {
					t.Fatalf("anthropic replay lost empty object: %+v", input)
				}
			})
		}
	}
}

func TestValidateStreamToolCallsRejectNonObjects(t *testing.T) {
	for _, raw := range []string{"nul", "null null", "[]", `"null"`, "true", "42", `{"command":`, " \n "} {
		t.Run(raw, func(t *testing.T) {
			result := &types.StreamResult{ToolCalls: []types.ToolCall{{Function: types.ToolCallFunction{Name: "sh", ArgumentsRaw: raw}}}}
			if err := validateStreamToolCalls(result); err == nil {
				t.Fatalf("invalid arguments accepted: %q", raw)
			}
		})
	}
}

func TestReasoningXMLToolCallsPassStreamValidation(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "thinking"} {
		for _, tc := range []struct {
			name, xml string
			args      map[string]any
		}{
			{"sh", "<tool_call><function=sh><parameter=command>printf 'hello'\necho done</parameter></function></tool_call>", map[string]any{"command": "printf 'hello'\necho done"}},
			{"ping", "<tool_call><function=ping></function></tool_call>", map[string]any{}},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				var stream strings.Builder
				for _, fragment := range []string{tc.xml[:len(tc.xml)/2], tc.xml[len(tc.xml)/2:]} {
					data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{field: fragment}}}})
					if err != nil {
						t.Fatal(err)
					}
					fmt.Fprintf(&stream, "data: %s\n\n", data)
				}
				stream.WriteString("data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n")
				result, err := parseOpenAIStreamResponse(context.Background(), io.NopCloser(strings.NewReader(stream.String())), true, NewSilentOutputSink())
				if err != nil || result == nil || len(result.ToolCalls) != 1 || result.FinishReason != "tool_calls" {
					t.Fatalf("XML tool rejected: %+v, %v", result, err)
				}
				fn := result.ToolCalls[0].Function
				if fn.Name != tc.name || fn.ArgumentsRaw != "" || len(fn.Arguments) != len(tc.args) {
					t.Fatalf("unexpected XML arguments: %+v", fn)
				}
				for key, want := range tc.args {
					if fn.Arguments[key] != want {
						t.Fatalf("XML argument %s = %v, want %v", key, fn.Arguments[key], want)
					}
				}
				raw := convertMessagesToOpenAI([]types.Message{{Role: "assistant", ToolCalls: result.ToolCalls}})[0].ToolCalls[0].Function.Arguments
				if !json.Valid([]byte(raw)) {
					t.Fatalf("XML replay contains non-JSON arguments: %q", raw)
				}
			})
		}
	}
}

func TestGeminiStreamPromptFeedback(t *testing.T) {
	for _, tc := range []struct {
		name, data, wantError string
	}{
		{"prompt_safety", `{"promptFeedback":{"blockReason":"SAFETY"}}`, "gemini prompt blocked: SAFETY"},
		{"prompt_blocklist", `{"candidates":[],"promptFeedback":{"blockReason":"BLOCKLIST"}}`, "gemini prompt blocked: BLOCKLIST"},
		{"prompt_rating_blocked", `{"promptFeedback":{"safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","blocked":true}]}}`, "gemini prompt blocked: SAFETY"},
		{"candidate_rating_blocked", `{"candidates":[{"safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","blocked":true}]}]}`, "gemini response blocked: SAFETY"},
		{"rating_not_blocked", `{"promptFeedback":{"safetyRatings":[{"probability":"HIGH","blocked":false}]}}`, "before a terminal event"},
		{"unspecified_block", `{"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}}`, "before a terminal event"},
		{"empty_feedback", `{"promptFeedback":{}}`, "before a terminal event"},
		{"candidate_finish_safety", `{"candidates":[{"finishReason":"SAFETY"}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseGeminiStreamResponse(context.Background(), io.NopCloser(strings.NewReader("data: "+tc.data+"\n\n")), NewSilentOutputSink())
			if tc.wantError != "" {
				if err == nil || result != nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("feedback misclassified: %+v, %v; want %q", result, err, tc.wantError)
				}
			} else if err != nil || result == nil || result.FinishReason != "safety" {
				t.Fatalf("existing terminal safety behavior changed: %+v, %v", result, err)
			}
		})
	}
}

// This probe never executes tools or dumps reasoning; it replays the actual wire response.
// Its small token budget may end with length before the model emits answer text.
func TestLiveOllamaStreamTermination(t *testing.T) {
	if os.Getenv("FYC_LIVE_OLLAMA") != "1" {
		t.Skip("set FYC_LIVE_OLLAMA=1 and FYC_OLLAMA_MODEL to probe localhost:11434")
	}
	model := os.Getenv("FYC_OLLAMA_MODEL")
	if model == "" {
		t.Fatal("set FYC_OLLAMA_MODEL to an installed local model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	payload, err := json.Marshal(map[string]any{
		"model": model, "stream": true, "max_tokens": 128,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []any{map[string]any{"role": "user", "content": "Reply with just OK."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:11434/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	wire, err := io.ReadAll(io.LimitReader(resp.Body, maxStreamLineBytes))
	if err != nil {
		t.Fatal(err)
	}
	var frames int
	var done bool
	var reason string
	scanner := bufio.NewScanner(bytes.NewReader(wire))
	scanner.Buffer(make([]byte, 1024), maxStreamLineBytes)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			done = true
			continue
		}
		frames++
		var frame OpenAIResponse
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			t.Fatal(err)
		}
		for _, choice := range frame.Choices {
			if choice.FinishReason != "" {
				reason = choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	result, err := parseOpenAIStreamResponse(ctx, io.NopCloser(bytes.NewReader(wire)), false, NewSilentOutputSink())
	var textBytes, reasoningBytes int
	if result != nil {
		textBytes, reasoningBytes = len(result.Content), len(result.ReasoningContent)
	}
	t.Logf("model=%s frames=%d finish_reason=%q DONE=%t text_bytes=%d reasoning_bytes=%d parser_error=%v", model, frames, reason, done, textBytes, reasoningBytes, err)
	if err != nil {
		t.Fatalf("parser rejected live stream: %v", err)
	}
	if err := validateOllamaProbeResult(result, reason); err != nil {
		t.Fatal(err)
	}
}

func validateOllamaProbeResult(result *types.StreamResult, reason string) error {
	if result == nil {
		return fmt.Errorf("live stream returned no result")
	}
	if result.Interrupted {
		return fmt.Errorf("live stream was interrupted")
	}
	if result.FinishReason != reason {
		return fmt.Errorf("live stream finish_reason lost: got %q, want %q", result.FinishReason, reason)
	}
	if strings.TrimSpace(result.Content) == "" && strings.TrimSpace(result.ReasoningContent) == "" {
		return fmt.Errorf("live stream returned no text or reasoning (finish_reason=%q)", reason)
	}
	return nil
}

func TestOllamaProbeResultValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    *types.StreamResult
		wantError bool
	}{
		{"answer", &types.StreamResult{Content: "OK", FinishReason: "length"}, false},
		{"reasoning_only", &types.StreamResult{ReasoningContent: "thinking", FinishReason: "length"}, false},
		{"missing", nil, true},
		{"empty", &types.StreamResult{FinishReason: "length"}, true},
		{"whitespace", &types.StreamResult{Content: " \n", ReasoningContent: "\t", FinishReason: "length"}, true},
		{"interrupted", &types.StreamResult{ReasoningContent: "thinking", FinishReason: "length", Interrupted: true}, true},
		{"finish_reason_lost", &types.StreamResult{Content: "OK"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOllamaProbeResult(tc.result, "length"); (err != nil) != tc.wantError {
				t.Fatalf("validation error = %v, wantError=%t", err, tc.wantError)
			}
		})
	}
}

func TestOllamaProbeReasoningOnlyTokenLimit(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "thinking"} {
		t.Run(field, func(t *testing.T) {
			stream := fmt.Sprintf("data: {\"choices\":[{\"delta\":{%q:\"thinking\"}}]}\n\n", field) +
				"data: " + `{"choices":[{"delta":{},"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n"
			result, err := parseOpenAIStreamResponse(context.Background(), io.NopCloser(strings.NewReader(stream)), false, NewSilentOutputSink())
			if err != nil {
				t.Fatal(err)
			}
			if err := validateOllamaProbeResult(result, "length"); err != nil {
				t.Fatal(err)
			}
			if result.Content != "" || result.ReasoningContent != "thinking" {
				t.Fatal("reasoning-only response was not preserved")
			}
		})
	}
}
