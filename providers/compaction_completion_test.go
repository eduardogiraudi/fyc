package providers

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCompactionCompletionReasons(t *testing.T) {
	for _, reason := range []string{"stop", "length", "content_filter"} {
		body := io.NopCloser(strings.NewReader(`data: {"choices":[{"delta":{"content":"summary"},"finish_reason":"` + reason + `"}]}` + "\n\ndata: [DONE]\n\n"))
		result, err := parseOpenAIStreamResponse(context.Background(), body, false, NewSilentOutputSink())
		if err != nil || result.FinishReason != reason {
			t.Fatalf("Chat Completions reason %q: %+v, %v", reason, result, err)
		}
	}
	for _, reason := range []string{"STOP", "MAX_TOKENS", "SAFETY"} {
		// Gemini may report finishReason in a chunk without any content parts.
		body := io.NopCloser(strings.NewReader(`data: {"candidates":[{"finishReason":"` + reason + `"}]}` + "\n\n"))
		result, err := parseGeminiStreamResponse(context.Background(), body, NewSilentOutputSink())
		if err != nil || result.FinishReason != strings.ToLower(reason) {
			t.Fatalf("Gemini reason %q: %+v, %v", reason, result, err)
		}
	}
	for _, details := range []string{``, `,"incomplete_details":{"reason":"max_output_tokens"}`} {
		body := io.NopCloser(strings.NewReader(`data: {"type":"response.incomplete","response":{"status":"incomplete"` + details + `}}` + "\n\n"))
		result, err := parseCodexStreamResponse(context.Background(), body, false, NewSilentOutputSink())
		want := "incomplete"
		if details != "" {
			want = "max_output_tokens"
		}
		if err != nil || result.FinishReason != want {
			t.Fatalf("Responses incomplete: %+v, %v", result, err)
		}
	}
}
