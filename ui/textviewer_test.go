package ui

import (
	"strings"
	"testing"

	"fuckyourcli/types"
)

// TestVisualLinesSanitisesAndKeepsKinds guards the two invariants the viewer
// relies on: no escape from the (untrusted) payload ever reaches a row, and the
// semantic kind survives sanitising and wrapping so the colouring stays uniform
// across a wrapped line.
func TestVisualLinesSanitisesAndKeepsKinds(t *testing.T) {
	m := TextViewerModel{
		termWidth:  10,
		termHeight: 24,
		Lines: []types.DetailLine{
			{Kind: types.DetailSection, Text: "Command:"},
			{Kind: types.DetailCommand, Text: "  echo \x1b[31mred\x1b[0m"},
			{},
			{Kind: types.DetailPath, Text: "  /very/long/path"},
		},
	}
	visual := m.visualLines()
	if len(visual) == 0 {
		t.Fatal("no visual rows produced")
	}
	for _, v := range visual {
		if strings.Contains(v.text, "\x1b") {
			t.Fatalf("escape survived into the visual row: %q", v.text)
		}
		if DisplayWidth(v.text) > 10 {
			t.Fatalf("row wider than the terminal: %q", v.text)
		}
	}
	if visual[0].kind != types.DetailSection || visual[0].text != "Command:" {
		t.Fatalf("first row = %+v", visual[0])
	}
	if visual[1].kind != types.DetailCommand || visual[1].text != "  echo red" {
		t.Fatalf("command row = %+v", visual[1])
	}
	var pathRows int
	for _, v := range visual {
		if v.kind == types.DetailPath {
			pathRows++
		}
	}
	if pathRows < 2 {
		t.Fatalf("a path wider than the terminal must wrap, got %d row(s)", pathRows)
	}
}

// TestRenderLineHighlightsOnlyCommands checks that colour is applied last and
// only where a kind asks for it: plain text is returned verbatim, the command
// section goes through the shell lexer, and no row ever gains a newline (which
// would desynchronise the viewer's row accounting).
func TestRenderLineHighlightsOnlyCommands(t *testing.T) {
	if got := renderLine(visualLine{}); got != "" {
		t.Fatalf("empty row must render empty, got %q", got)
	}
	if got := renderLine(visualLine{text: "Persistence: false"}); got != "Persistence: false" {
		t.Fatalf("plain text must be untouched, got %q", got)
	}

	cmd := renderLine(visualLine{kind: types.DetailCommand, text: "echo hi"})
	if !strings.Contains(cmd, "\x1b[") {
		t.Fatalf("command row is not highlighted: %q", cmd)
	}
	if strings.Contains(cmd, "\n") {
		t.Fatalf("highlighting must not add a newline: %q", cmd)
	}
	if plain := stripControlSequences(cmd); plain != "echo hi" {
		t.Fatalf("highlighting changed the text: %q", plain)
	}
}

// TestHighlightShellLineEmptyIsEmpty pins the empty-line fast path: the Chroma
// formatter would otherwise emit a bare reset for a blank row.
func TestHighlightShellLineEmptyIsEmpty(t *testing.T) {
	if got := highlightShellLine(""); got != "" {
		t.Fatalf("highlightShellLine(\"\") = %q, want empty", got)
	}
}
