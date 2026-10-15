package providers

import (
	"context"
	"io"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// recordingSink captures the reasoning the parser pushes to the UI.
type recordingSink struct {
	SilentOutputSink
	reasoning strings.Builder
}

func (s *recordingSink) WriteReasoning(delta string) { s.reasoning.WriteString(delta) }

// TestParseCodexReasoningDoneEventWithoutDeltas pins the other no-delta shape:
// only the final summary text is sent (no deltas), and it must still be shown.
func TestParseCodexReasoningDoneEventWithoutDeltas(t *testing.T) {
	stream := "data: {\"type\":\"response.reasoning_summary_text.done\",\"text\":\"weighing the options\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Yes.\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"id\":\"m\"}]}}\n\n"

	sink := &recordingSink{}
	result, err := parseCodexStreamResponse(
		context.Background(),
		io.NopCloser(strings.NewReader(stream)),
		true,
		sink,
	)
	if err != nil {
		t.Fatalf("parseCodexStreamResponse: %v", err)
	}
	if result.ReasoningContent != "weighing the options" || sink.reasoning.String() != "weighing the options" {
		t.Fatalf("reasoning content = %q, UI = %q", result.ReasoningContent, sink.reasoning.String())
	}
}

// TestParseCodexReasoningSummaryWithoutDeltas pins that a summary carried by the
// reasoning item — and never streamed as delta events — still reaches the UI: the
// thinking panel must not be empty when the response carries a summary.
func TestParseCodexReasoningSummaryWithoutDeltas(t *testing.T) {
	item := `{"type":"reasoning","id":"rs_1","encrypted_content":"blob","summary":[{"type":"summary_text","text":"weighing the options"}]}`
	stream := "data: {\"type\":\"response.output_item.added\",\"item\":" + item + "}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Yes.\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[" + item + ",{\"type\":\"message\",\"id\":\"m\"}]}}\n\n"

	sink := &recordingSink{}
	result, err := parseCodexStreamResponse(
		context.Background(),
		io.NopCloser(strings.NewReader(stream)),
		true,
		sink,
	)
	if err != nil {
		t.Fatalf("parseCodexStreamResponse: %v", err)
	}
	if result.ReasoningContent != "weighing the options" {
		t.Fatalf("reasoning content = %q, want the item summary", result.ReasoningContent)
	}
	if sink.reasoning.String() != "weighing the options" {
		t.Fatalf("the summary never reached the UI: %q", sink.reasoning.String())
	}
	if result.Content != "Yes." {
		t.Fatalf("content = %q", result.Content)
	}
}

// TestParseCodexReasoningDeltasWinOverItem pins the no-duplication rule: when the
// summary arrives as delta events, the copy inside the item is not appended again
// (the item text is only the fallback for streams that send no deltas).
func TestParseCodexReasoningDeltasWinOverItem(t *testing.T) {
	item := `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"weighing the options"}]}`
	stream := "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"weighing \"}\n\n" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"the options\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[" + item + "]}}\n\n"

	sink := &recordingSink{}
	result, err := parseCodexStreamResponse(
		context.Background(),
		io.NopCloser(strings.NewReader(stream)),
		true,
		sink,
	)
	if err != nil {
		t.Fatalf("parseCodexStreamResponse: %v", err)
	}
	if result.ReasoningContent != "weighing the options" {
		t.Fatalf("reasoning content = %q (the deltas must win, unduplicated)", result.ReasoningContent)
	}
}

func TestConvertMessagesToCodexInputReplaysReasoning(t *testing.T) {
	item := `{"type":"reasoning","id":"rs_1","encrypted_content":"abc","summary":[]}`
	in := convertMessagesToCodexInput([]types.Message{
		{Role: "assistant", Content: "hi", ReasoningItem: item},
	}, "gpt-5.5")
	if len(in) != 2 {
		t.Fatalf("expected reasoning + assistant, got %d items: %+v", len(in), in)
	}
	if in[0]["type"] != "reasoning" || in[0]["encrypted_content"] != "abc" || in[0]["id"] != "rs_1" {
		t.Fatalf("reasoning item not replayed: %+v", in[0])
	}
	if in[1]["role"] != "assistant" {
		t.Fatalf("assistant text must follow the reasoning item, got %+v", in[1])
	}
}

func TestConvertMessagesToCodexInputSkipsEmptyReasoning(t *testing.T) {
	in := convertMessagesToCodexInput([]types.Message{{Role: "assistant", Content: "hi"}}, "gpt-5.5")
	if len(in) != 1 || in[0]["role"] != "assistant" {
		t.Fatalf("no reasoning item expected, got %+v", in)
	}
}

// TestConvertMessagesToCodexInputDropsStaleReasoning pins the hot-model-switch
// guard: an item produced by a different model is not replayed (the encrypted
// state is model-bound and the server would reject it), while an item from an
// unknown producer (old sessions) still is.
func TestConvertMessagesToCodexInputDropsStaleReasoning(t *testing.T) {
	item := `{"type":"reasoning","id":"rs_1","encrypted_content":"abc","summary":[]}`

	same := convertMessagesToCodexInput([]types.Message{
		{Role: "assistant", Content: "hi", ReasoningItem: item, ReasoningModel: "gpt-5.5"},
	}, "gpt-5.5")
	if len(same) != 2 || same[0]["type"] != "reasoning" {
		t.Fatalf("same-model item must be replayed: %+v", same)
	}

	stale := convertMessagesToCodexInput([]types.Message{
		{Role: "assistant", Content: "hi", ReasoningItem: item, ReasoningModel: "gpt-5.5"},
	}, "gpt-5.4")
	if len(stale) != 1 || stale[0]["role"] != "assistant" {
		t.Fatalf("stale item must be dropped: %+v", stale)
	}

	unknown := convertMessagesToCodexInput([]types.Message{
		{Role: "assistant", Content: "hi", ReasoningItem: item},
	}, "gpt-5.4")
	if len(unknown) != 2 || unknown[0]["type"] != "reasoning" {
		t.Fatalf("unknown-producer item must be replayed: %+v", unknown)
	}
}

func TestParseCodexCapturesReasoningItem(t *testing.T) {
	stream := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[" +
		"{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"secret\"}]," +
		"\"usage\":{\"input_tokens\":5,\"output_tokens\":7}}}\n\n"

	result, err := parseCodexStreamResponse(
		context.Background(),
		io.NopCloser(strings.NewReader(stream)),
		true,
		NewSilentOutputSink(),
	)
	if err != nil {
		t.Fatalf("parseCodexStreamResponse: %v", err)
	}
	if !strings.Contains(result.ReasoningItem, "secret") || !strings.Contains(result.ReasoningItem, "rs_1") {
		t.Fatalf("reasoning item not captured: %q", result.ReasoningItem)
	}
	if result.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", result.FinishReason)
	}
}

// TestParseCodexCapturesAllReasoningItems pins that every reasoning item of a
// turn is kept (a turn can carry several with interleaved thinking): dropping any
// would lose reasoning continuity on replay.
func TestParseCodexCapturesAllReasoningItems(t *testing.T) {
	stream := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[" +
		"{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"first\"}," +
		"{\"type\":\"reasoning\",\"id\":\"rs_2\",\"encrypted_content\":\"second\"}," +
		"{\"type\":\"message\",\"id\":\"m\"}]}}\n\n"

	result, err := parseCodexStreamResponse(
		context.Background(),
		io.NopCloser(strings.NewReader(stream)),
		true,
		NewSilentOutputSink(),
	)
	if err != nil {
		t.Fatalf("parseCodexStreamResponse: %v", err)
	}
	items := decodeReasoningItems(result.ReasoningItem)
	if len(items) != 2 {
		t.Fatalf("expected 2 reasoning items, got %d: %q", len(items), result.ReasoningItem)
	}
	if items[0]["id"] != "rs_1" || items[1]["id"] != "rs_2" {
		t.Fatalf("reasoning items out of order: %v", items)
	}
}
