package providers

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// These tests pin down the "turn looks finished but nothing happened" bug: a
// stream that is cut before message_stop (network drop, proxy idle timeout, the
// 240s http deadline, an oversized SSE line) used to return a result with an
// empty FinishReason and no error, so the chat loop treated it as a completed
// turn and silently went back to the prompt mid-answer.

// errReader returns its data and then fails, like a connection reset.
type errReader struct {
	data []byte
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

func anthropicSSE(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// anthropicStreamPrefix returns a stream that is truncated in the middle of a
// tool_use block: no content_block_stop, no message_delta, no message_stop.
func anthropicStreamPrefix() string {
	var b strings.Builder
	b.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1}}}`))
	b.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	b.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me read that file. "}}`))
	b.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
	b.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`))
	b.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a"}}`))
	return b.String()
}

func TestParseAnthropicStreamCompleteTurn(t *testing.T) {
	stream := anthropicStreamPrefix() +
		anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"bc.txt\"}"}}`) +
		anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`) +
		anthropicSSE("message_stop", `{"type":"message_stop"}`)

	res, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(stream)}, NewSilentOutputSink())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "Let me read that file. " {
		t.Errorf("content = %q", res.Content)
	}
	if res.FinishReason != "tool_calls" || len(res.ToolCalls) != 1 {
		t.Fatalf("finishReason = %q, toolCalls = %d, want tool_calls/1", res.FinishReason, len(res.ToolCalls))
	}
	if got := res.ToolCalls[0].Function.Arguments["path"]; got != "abc.txt" {
		t.Errorf("tool argument path = %v", got)
	}
}

// EOF before message_stop must be reported as an error, not as an empty turn.
func TestParseAnthropicStreamTruncatedBeforeMessageStop(t *testing.T) {
	res, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(anthropicStreamPrefix())}, NewSilentOutputSink())
	if err == nil {
		t.Fatalf("expected error for truncated stream, got result %+v", res)
	}
	if !strings.Contains(err.Error(), "message_stop") {
		t.Errorf("error should mention message_stop, got: %v", err)
	}
}

// A real read error (connection reset / deadline exceeded) must not look like a
// clean end of stream.
func TestParseAnthropicStreamReadError(t *testing.T) {
	body := &errReader{data: []byte(anthropicStreamPrefix()), err: errors.New("read tcp: connection reset by peer")}
	_, err := parseAnthropicStreamResponse(context.Background(), nopCloser{body}, NewSilentOutputSink())
	if err == nil {
		t.Fatal("expected error for failing body, got nil")
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error should mention the interruption, got: %v", err)
	}
}

// max_tokens truncation is a complete (well-formed) stream: it must keep the
// stop_reason so the CLI can warn that the reply is incomplete.
func TestParseAnthropicStreamMaxTokens(t *testing.T) {
	stream := anthropicStreamPrefix() +
		anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"bc.txt\"}"}}`) +
		anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":8192}}`) +
		anthropicSSE("message_stop", `{"type":"message_stop"}`)

	res, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(stream)}, NewSilentOutputSink())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinishReason != "max_tokens" {
		t.Errorf("finishReason = %q, want max_tokens", res.FinishReason)
	}
}

// A finalized block with partial JSON must never be executed with empty arguments.
func TestParseAnthropicStreamIncompleteToolJSON(t *testing.T) {
	stream := anthropicSSE("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1}}}`) +
		anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write","input":{}}}`) +
		anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"src/mai"}}`) +
		anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":8192}}`) +
		anthropicSSE("message_stop", `{"type":"message_stop"}`)

	res, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(stream)}, NewSilentOutputSink())
	if err == nil || res != nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("incomplete tool JSON must not be executable: result=%+v, err=%v", res, err)
	}
}

// A refusal is an HTTP 200 response with (usually) no content: it must surface.
func TestParseAnthropicStreamRefusal(t *testing.T) {
	stream := anthropicSSE("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`) +
		anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`) +
		anthropicSSE("message_stop", `{"type":"message_stop"}`)

	_, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(stream)}, NewSilentOutputSink())
	if err == nil || !strings.Contains(err.Error(), "refusal") {
		t.Fatalf("expected refusal error, got: %v", err)
	}
}

// A single SSE line larger than the scanner buffer must be reported, not
// silently dropped.
func TestParseAnthropicStreamLineTooLong(t *testing.T) {
	huge := strings.Repeat("x", 8*1024*1024+10)
	stream := anthropicSSE("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`) +
		"data: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + huge + `"}}` + "\n\n"

	if _, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(stream)}, NewSilentOutputSink()); err == nil {
		t.Fatal("expected error for oversized SSE line, got nil")
	}
}

// The reader goroutine must not outlive the parser on an early return.
func TestParseAnthropicStreamReaderGoroutineExits(t *testing.T) {
	// Larger than the 64-slot channel, so the reader goroutine would block
	// forever without the stop channel.
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Either branch may win the select, but the parser must return promptly.
	if _, err := parseAnthropicStreamResponse(ctx, nopCloser{strings.NewReader(b.String())}, NewSilentOutputSink()); err != nil && !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = types.StreamResult{}
}
