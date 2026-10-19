package main

import (
	"context"
	"strings"
	"testing"

	"fuckyourcli/types"
)

func toolPair(id, tool string, args map[string]interface{}, content string) []types.Message {
	call := types.ToolCall{ID: id, Function: types.ToolCallFunction{Name: tool, Arguments: args}}
	return []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{call}},
		{Role: "tool", Name: tool, ToolCallID: id, Content: content},
	}
}

func pathArgs(p string) map[string]interface{} { return map[string]interface{}{"path": p} }

func toolResultIndex(t *testing.T, history []types.Message, id string) int {
	t.Helper()
	for i, m := range history {
		if m.Role == "tool" && m.ToolCallID == id {
			return i
		}
	}
	t.Fatalf("no tool result for call %q", id)
	return -1
}

func TestPruneSupersededToolResultsElidesOnlyTheDuplicate(t *testing.T) {
	body := strings.Repeat("file body ", 300)
	history := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	history = append(history, toolPair("dup", "read", pathArgs("a.go"), body)...)
	history = append(history, toolPair("once", "read", pathArgs("b.go"), body)...)
	history = append(history, toolPair("anchors", "get_anchors", pathArgs("a.go"), body)...)
	history = append(history, toolPair("latest", "read", pathArgs("./a.go"), body)...)

	out := pruneSupersededToolResults(history)

	dup := out[toolResultIndex(t, out, "dup")]
	if !strings.Contains(dup.Content, "omitted from this transcript") || !strings.Contains(dup.Content, "a.go") {
		t.Fatalf("superseded read not elided: %q", dup.Content)
	}
	if dup.Role != "tool" || dup.Name != "read" || dup.ToolCallID != "dup" {
		t.Fatalf("elision broke the call pairing: %+v", dup)
	}
	// A single read has no successor, another tool never supersedes one, and
	// "./a.go" is the same target as "a.go" (so it is the surviving one).
	for _, id := range []string{"once", "anchors", "latest"} {
		if got := out[toolResultIndex(t, out, id)].Content; got != body {
			t.Fatalf("%s must be kept verbatim, got %q", id, got)
		}
	}
	if history[toolResultIndex(t, history, "dup")].Content != body {
		t.Fatal("the live history was mutated")
	}
}

func TestPruneSupersededToolResultsKeepsErrorsAndShortResults(t *testing.T) {
	body := strings.Repeat("file body ", 300)
	history := []types.Message{{Role: "user", Content: "task"}}
	history = append(history, toolPair("err", "read", pathArgs("e.go"), "error: permission denied")...)
	history = append(history, toolPair("err-latest", "read", pathArgs("e.go"), body)...)
	history = append(history, toolPair("short", "ls", pathArgs("."), "a.go\nb.go")...)
	history = append(history, toolPair("short-latest", "ls", pathArgs("."), body)...)

	out := pruneSupersededToolResults(history)

	if got := out[toolResultIndex(t, out, "err")].Content; got != "error: permission denied" {
		t.Fatalf("an error result must never be elided, got %q", got)
	}
	if got := out[toolResultIndex(t, out, "short")].Content; got != "a.go\nb.go" {
		t.Fatalf("a short result must not be replaced, got %q", got)
	}
	for _, id := range []string{"err-latest", "short-latest"} {
		if got := out[toolResultIndex(t, out, id)].Content; got != body {
			t.Fatalf("the latest result must be kept, %s = %q", id, got)
		}
	}
}

func TestPruneSupersededToolResultsNoopReturnsSameHistory(t *testing.T) {
	history := []types.Message{{Role: "user", Content: "task"}}
	history = append(history, toolPair("only", "read", pathArgs("a.go"), strings.Repeat("x", 2000))...)
	out := pruneSupersededToolResults(history)
	if len(out) != len(history) || out[1].Content != history[1].Content {
		t.Fatal("a history without duplicates must pass through untouched")
	}
}

func TestCompactionTranscriptElidesSupersededResults(t *testing.T) {
	compactionTestConfig(t, true, true, 8000)
	first := strings.Repeat("old body ", 400)
	second := strings.Repeat("new body ", 400)
	history := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	history = append(history, toolPair("first", "read", pathArgs("a.go"), first)...)
	history = append(history, toolPair("second", "read", pathArgs("a.go"), second)...)
	s := &ChatSession{ChatHistory: history}

	_, _, err := compactContextWith(context.Background(), s, false, 8000, func(_ context.Context, messages []types.Message, _ *ChatSession) (string, error) {
		transcript := messages[len(messages)-1].Content
		if strings.Contains(transcript, "old body") {
			t.Fatal("a superseded result was sent to the summarizer")
		}
		if !strings.Contains(transcript, "new body") || !strings.Contains(transcript, "omitted from this transcript") {
			t.Fatal("the latest result or the elision note is missing from the transcript")
		}
		return "summary", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ChatHistory) != 3 || s.ChatHistory[1].Content != compactionSummaryPrefix+"summary" {
		t.Fatalf("compaction did not commit the summary: %+v", s.ChatHistory)
	}
}
