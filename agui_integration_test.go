package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/agui"
	"fuckyourcli/types"
)

// TestHandleToolCallsEmitsStructuredEvents exercises the real tool dispatch path
// with an AG-UI sink: the sink implements types.ToolEventSink, so handleToolCalls
// must emit TOOL_CALL_START/ARGS/END and a TOOL_CALL_RESULT instead of the
// unstructured WriteToolOutput used by the TUI.
func TestHandleToolCallsEmitsStructuredEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf strings.Builder
	sink := agui.NewSink(&buf, nil, "t1", "r1")

	s := &ChatSession{RestrictedRoot: dir}
	s.Sink = sink

	call := types.ToolCall{
		ID: "c1",
		Function: types.ToolCallFunction{
			Name:      "ls",
			Arguments: map[string]interface{}{"path": "."},
		},
	}
	handleToolCalls(s, sink, []types.ToolCall{call}, true)
	sink.Finalize()

	out := buf.String()
	for _, want := range []string{
		`"type":"TOOL_CALL_START"`,
		`"type":"TOOL_CALL_ARGS"`,
		`"type":"TOOL_CALL_END"`,
		`"type":"TOOL_CALL_RESULT"`,
		`"toolCallId":"c1"`,
		`a.go`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in sink output:\n%s", want, out)
		}
	}

	// The tool result must be recorded in the session history.
	if len(s.ChatHistory) != 1 || s.ChatHistory[0].Role != "tool" || s.ChatHistory[0].ToolCallID != "c1" {
		t.Fatalf("unexpected history: %+v", s.ChatHistory)
	}
}

// TestHandleToolCallsGeneratesToolCallID checks that a missing provider id is
// replaced by a stable synthetic one used consistently across the lifecycle.
func TestHandleToolCallsGeneratesToolCallID(t *testing.T) {
	dir := t.TempDir()
	var buf strings.Builder
	sink := agui.NewSink(&buf, nil, "t1", "r1")
	s := &ChatSession{RestrictedRoot: dir}
	s.Sink = sink

	call := types.ToolCall{
		Function: types.ToolCallFunction{
			Name:      "ls",
			Arguments: map[string]interface{}{"path": "."},
		},
	}
	handleToolCalls(s, sink, []types.ToolCall{call}, false)
	sink.Finalize()

	out := buf.String()
	if !strings.Contains(out, `"toolCallId":"call_`) {
		t.Fatalf("expected a synthetic call id, got:\n%s", out)
	}
	// START and RESULT must share the same generated id.
	start := strings.Index(out, `"type":"TOOL_CALL_START"`)
	res := strings.Index(out, `"type":"TOOL_CALL_RESULT"`)
	if start < 0 || res < 0 {
		t.Fatalf("missing lifecycle events:\n%s", out)
	}
	startFrame := out[start : strings.Index(out[start:], "\n")+start]
	resFrame := out[res : strings.Index(out[res:], "\n")+res]
	idOf := func(frame string) string {
		i := strings.Index(frame, `"toolCallId":"`)
		if i < 0 {
			return ""
		}
		rest := frame[i+len(`"toolCallId":"`):]
		return rest[:strings.Index(rest, `"`)]
	}
	if idOf(startFrame) == "" || idOf(startFrame) != idOf(resFrame) {
		t.Fatalf("tool call ids differ: start=%q result=%q", idOf(startFrame), idOf(resFrame))
	}
}
