package main

import (
	"fmt"
	"strings"
	"testing"

	"fuckyourcli/types"
	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// expandableModel is an alt-screen chat holding one truncated tool result.
func expandableModel(t *testing.T, height int) chatModel {
	t.Helper()
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, height
	m.enableAltScreen(nil)
	m.transcript.AppendTool(ui.NewToolOutput("read file a.go", "one\ntwo\nthree\nfour", ""))
	m.View()
	return m
}

// rowOf returns the frame row containing needle, or -1.
func rowOf(view, needle string) int {
	for i, row := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(row, needle) {
			return i
		}
	}
	return -1
}

// The "… N more lines hidden" marker is a button in the alt-screen TUI: clicking
// it reveals what it hides, and the collapse row puts it back.
func TestClickExpandsTheHiddenToolOutput(t *testing.T) {
	m := expandableModel(t, 20)
	marker := rowOf(m.View(), "more lines hidden")
	if marker < 0 {
		t.Fatalf("no marker in the frame:\n%s", m.View())
	}
	if strings.Contains(ansi.Strip(m.View()), "four") {
		t.Fatalf("the hidden rows are already visible:\n%s", m.View())
	}

	next, cmd := m.Update(press(4, marker))
	m = next.(chatModel)
	if cmd != nil {
		t.Fatal("expanding ran a command")
	}
	if m.transcript.selActive {
		t.Fatal("the click started a text selection instead of expanding")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "four") || !strings.Contains(view, "collapse 2 lines") {
		t.Fatalf("the click did not expand the result:\n%s", view)
	}

	collapse := rowOf(m.View(), "collapse 2 lines")
	next, _ = m.Update(press(4, collapse))
	m = next.(chatModel)
	if view := ansi.Strip(m.View()); strings.Contains(view, "four") {
		t.Fatalf("the collapse row did not hide the result again:\n%s", view)
	}
}

// A row that is not a control keeps behaving like plain text: the press starts
// a drag selection.
func TestClickOnPlainTranscriptRowStillSelects(t *testing.T) {
	m := expandableModel(t, 20)
	next, _ := m.Update(press(4, rowOf(m.View(), "one")))
	m = next.(chatModel)
	if !m.transcript.selActive {
		t.Fatal("a press on plain output must start a selection")
	}
}

// The marker lights up under the pointer, and only its own row does.
func TestHoverHighlightsTheExpandableRow(t *testing.T) {
	forceTrueColor(t)
	m := expandableModel(t, 20)
	plain := m.View()
	marker := rowOf(m.View(), "more lines hidden")

	next, _ := m.Update(motion(4, marker))
	m = next.(chatModel)
	if !m.transcript.hoverActive {
		t.Fatal("the marker row is not hovered")
	}
	hovered := m.View()
	if hovered == plain {
		t.Fatal("the marker did not change under the pointer")
	}
	if !strings.Contains(strings.Split(hovered, "\n")[marker], "38;2;255;255;255") {
		t.Fatalf("the marker is not white under the pointer: %q", strings.Split(hovered, "\n")[marker])
	}

	next, _ = m.Update(motion(4, rowOf(m.View(), "one")))
	m = next.(chatModel)
	if m.transcript.hoverActive {
		t.Fatal("a plain row stayed highlighted")
	}
	if m.View() != plain {
		t.Fatal("the marker kept its highlight after the pointer left")
	}
}

// The control is only the cells the hover highlight paints: the empty part of
// the row to its right neither highlights nor toggles.
func TestControlRowIsOnlyActiveOverItsText(t *testing.T) {
	m := expandableModel(t, 20)
	marker := rowOf(m.View(), "more lines hidden")
	if marker < 0 {
		t.Fatalf("no marker in the frame:\n%s", m.View())
	}
	width := ansi.StringWidth(strings.Split(ansi.Strip(m.View()), "\n")[marker])
	if width >= m.width-2 {
		t.Fatalf("marker row spans the whole line (%d cols), nothing to test", width)
	}

	next, _ := m.Update(motion(width+2, marker))
	m = next.(chatModel)
	if m.transcript.hoverActive {
		t.Fatal("the empty cells right of the marker are hovered")
	}
	next, _ = m.Update(press(width+2, marker))
	m = next.(chatModel)
	if strings.Contains(ansi.Strip(m.View()), "four") {
		t.Fatal("a click right of the marker expanded the result")
	}

	next, _ = m.Update(motion(width-1, marker))
	m = next.(chatModel)
	if !m.transcript.hoverActive {
		t.Fatal("the last cell of the marker is not hovered")
	}
	next, _ = m.Update(press(width-1, marker))
	m = next.(chatModel)
	if !strings.Contains(ansi.Strip(m.View()), "four") {
		t.Fatal("a click on the marker text did not expand the result")
	}
}

// A replayed tool result keeps the marker: the resumed chat shows the same
// clickable row the live one had.
func TestReplayedToolOutputStaysExpandable(t *testing.T) {
	var seed []ui.SeedBlock
	ui.SetPreProgramBlockSink(func(b ui.SeedBlock) { seed = append(seed, b) })
	defer ui.SetPreProgramBlockSink(nil)

	// At start-up the chat has no sink yet: the block must still reach the seed.
	s := &ChatSession{}
	writeSessionToolResult(s, types.Message{
		Role:       "tool",
		Name:       "read",
		Content:    "one\ntwo\nthree\nfour",
		ToolDetail: " first\n-second\n+TWO",
	}, types.ToolCall{Function: types.ToolCallFunction{Name: "read"}}, 80)

	if len(seed) != 1 || seed[0].Tool == nil {
		t.Fatalf("replay seed = %#v, want one expandable block", seed)
	}
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, 20
	m.enableAltScreenBlocks(seed)
	m.View()
	view := ansi.Strip(m.View())
	for _, want := range []string{"more lines hidden", "show changed lines"} {
		if !strings.Contains(view, want) {
			t.Fatalf("replayed block is missing %q:\n%s", want, view)
		}
	}
}

// The live path keeps the whole result in the transcript, and the expansion
// survives a resize (the block is re-rendered, not frozen at the old width).
func TestLiveToolOutputStaysExpandableAcrossResize(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, 24
	m.enableAltScreen(nil)
	next, _ := m.Update(ui.StreamToolOutputMsg{
		Label:  "patched file a.go",
		Result: "updated (+1 -1 lines)\nnew anchors:\nk7fwd§two",
		Detail: " first\n-second\n+TWO",
	})
	m = next.(chatModel)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "show changed lines") {
		t.Fatalf("live tool output lost its detail control:\n%s", view)
	}

	next, _ = m.Update(press(4, rowOf(m.View(), "show changed lines")))
	m = next.(chatModel)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "+TWO") {
		t.Fatalf("the click did not show the changed lines:\n%s", view)
	}

	next, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = next.(chatModel)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "+TWO") {
		t.Fatalf("the resize collapsed the open detail:\n%s", view)
	}
}

// Expanding a block the reader had scrolled back to must not jump the window:
// the revealed rows land where the marker was, and the rows above stay put.
func TestExpandingAScrolledBackBlockKeepsTheWindowInPlace(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, 16
	m.enableAltScreen(nil)
	m.transcript.AppendTool(ui.NewToolOutput("read file a.go", "one\ntwo\nthree\nfour", ""))
	for i := 0; i < 40; i++ {
		m.printCmd(fmt.Sprintf("line-%02d", i))
	}
	m.View()
	m.transcript.ScrollTop()
	m.View()

	marker := rowOf(m.View(), "more lines hidden")
	if marker < 1 {
		t.Fatalf("the marker is not on screen (row %d):\n%s", marker, m.View())
	}
	before := strings.Split(ansi.Strip(m.View()), "\n")

	next, _ := m.Update(press(4, marker))
	m = next.(chatModel)
	after := strings.Split(ansi.Strip(m.View()), "\n")

	if !strings.Contains(after[marker], "three") {
		t.Fatalf("the revealed row did not land on the marker row: %q", after[marker])
	}
	for i := 0; i < marker; i++ {
		if before[i] != after[i] {
			t.Fatalf("row %d moved: %q -> %q", i, before[i], after[i])
		}
	}
}
