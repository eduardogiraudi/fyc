package agui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// decodeSSE parses the "data: {json}\n\n" frames produced by the Sink.
func decodeSSE(t *testing.T, raw string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("invalid event JSON %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func eventTypes(events []map[string]interface{}) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i], _ = e["type"].(string)
	}
	return types
}

func TestSinkEmitsTextMessageTriade(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, nil, "t1", "r1")

	s.Reset()
	s.WriteText("Ciao")
	s.WriteText(" mondo")
	s.WriteNewline()
	s.Finalize()

	events := decodeSSE(t, buf.String())
	got := eventTypes(events)
	want := []string{EvTextMessageStart, EvTextMessageContent, EvTextMessageContent, EvTextMessageEnd}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected event sequence: %v", got)
	}
	if events[1]["delta"] != "Ciao" || events[2]["delta"] != " mondo" {
		t.Fatalf("unexpected deltas: %v / %v", events[1]["delta"], events[2]["delta"])
	}
	// message ids must match within the triade
	if events[0]["messageId"] != events[3]["messageId"] {
		t.Fatalf("messageId mismatch: %v vs %v", events[0]["messageId"], events[3]["messageId"])
	}
}

func TestSinkEmitsToolCallLifecycle(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, nil, "t1", "r1")

	s.ToolCallStarted("c1", "read", map[string]interface{}{"path": "a.go"})
	s.ToolCallFinished("c1", "read", "file content")
	s.Finalize()

	events := decodeSSE(t, buf.String())
	got := eventTypes(events)
	want := []string{EvToolCallStart, EvToolCallArgs, EvToolCallEnd, EvToolCallResult}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected event sequence: %v", got)
	}
	if events[3]["toolCallId"] != "c1" || events[3]["content"] != "file content" {
		t.Fatalf("unexpected tool result: %+v", events[3])
	}
}

func TestSinkReasoningAndCustom(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, nil, "t1", "r1")

	s.WriteReasoning("think")
	s.WriteDiffCraft(3, 1)
	s.WriteStats("provider info", "token line")
	s.Finalize()

	events := decodeSSE(t, buf.String())
	got := strings.Join(eventTypes(events), ",")
	want := strings.Join([]string{
		EvReasoningStart, EvReasoningMessageStart, EvReasoningMessageContent,
		EvCustom,
		EvReasoningMessageEnd, EvReasoningEnd,
	}, ",")
	if got != want {
		t.Fatalf("unexpected event sequence: %s", got)
	}
	if events[3]["name"] != "fyc.replace_anchors" {
		t.Fatalf("unexpected custom event: %+v", events[3])
	}
	// REASONING_START and REASONING_END carry the messageId of the reasoning
	// message they delimit: the AG-UI schemas mark it as required.
	if id, _ := events[0]["messageId"].(string); id == "" {
		t.Fatalf("REASONING_START is missing the required messageId: %+v", events[0])
	} else if events[1]["messageId"] != id || events[5]["messageId"] != id {
		t.Fatalf("reasoning messageId mismatch: %v / %v / %v", events[0]["messageId"], events[1]["messageId"], events[5]["messageId"])
	}
	if events[1]["role"] != "reasoning" {
		t.Fatalf("unexpected reasoning role: %v", events[1]["role"])
	}
	if id, _ := events[5]["messageId"].(string); id == "" {
		t.Fatalf("REASONING_END is missing the required messageId: %+v", events[5])
	}
	md := s.Metadata()
	if md["providerInfo"] != "provider info" || md["tokenStats"] != "token line" {
		t.Fatalf("unexpected metadata: %+v", md)
	}
}

// TestSinkEmitsSpecRequiredFields drives a full turn and checks the fields the
// AG-UI schemas require per event type. Clients validate every incoming event
// (the JS/TS SDK does it with zod), so a single missing id aborts the stream
// with `invalid_type … path:["messageId"]`.
func TestSinkEmitsSpecRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, nil, "t1", "r1")

	s.Reset()
	s.WriteReasoning("think")
	s.WriteText("answer")
	s.ToolCallStarted("c1", "read", map[string]interface{}{"path": "a.go"})
	s.ToolCallFinished("c1", "read", "content")
	s.Finalize()

	needMessageID := map[string]bool{
		EvTextMessageStart: true, EvTextMessageContent: true, EvTextMessageEnd: true,
		EvReasoningStart: true, EvReasoningMessageStart: true, EvReasoningMessageContent: true,
		EvReasoningMessageEnd: true, EvReasoningEnd: true, EvToolCallResult: true,
	}
	needToolCallID := map[string]bool{
		EvToolCallStart: true, EvToolCallArgs: true, EvToolCallEnd: true, EvToolCallResult: true,
	}

	events := decodeSSE(t, buf.String())
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	for _, ev := range events {
		typ, _ := ev["type"].(string)
		if needMessageID[typ] {
			if id, _ := ev["messageId"].(string); id == "" {
				t.Fatalf("%s is missing the required messageId: %+v", typ, ev)
			}
		}
		if needToolCallID[typ] {
			if id, _ := ev["toolCallId"].(string); id == "" {
				t.Fatalf("%s is missing the required toolCallId: %+v", typ, ev)
			}
		}
	}
}

func TestSinkWriteToolOutputIsNoop(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, nil, "t1", "r1")
	s.WriteToolOutput("label", "result")
	s.Finalize()
	if buf.Len() != 0 {
		t.Fatalf("expected no events from the unstructured fallback, got %q", buf.String())
	}
}
