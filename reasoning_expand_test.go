package main

import (
	"strings"
	"testing"

	"fuckyourcli/ui"

	"github.com/charmbracelet/x/ansi"
)

// The reasoning trace of a finished turn is a button: the summary row unfolds
// the whole text on a click instead of throwing it away, and collapses again.
func TestFinishedReasoningExpandsOnAClick(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, 24
	m.enableAltScreen(nil)

	next, _ := m.Update(ui.StreamReasoningMsg{Delta: "first thought\nsecond thought\nthird thought"})
	m = next.(chatModel)
	next, _ = m.Update(ui.StreamTextMsg{Token: "answer"})
	m = next.(chatModel)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "click to expand") {
		t.Fatalf("the finished trace is not expandable:\n%s", view)
	}
	if strings.Contains(view, "second thought") {
		t.Fatalf("the trace is already visible before the click:\n%s", view)
	}

	row := rowOf(m.View(), "click to expand")
	if row < 0 {
		t.Fatalf("the summary row is not on screen:\n%s", m.View())
	}
	next, _ = m.Update(press(4, row))
	m = next.(chatModel)
	if m.transcript.selActive {
		t.Fatal("the click started a text selection instead of expanding")
	}
	view = ansi.Strip(m.View())
	for _, want := range []string{"first thought", "second thought", "third thought", "hide reasoning"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the unfolded trace is missing %q:\n%s", want, view)
		}
	}

	next, _ = m.Update(press(4, rowOf(m.View(), "hide reasoning")))
	m = next.(chatModel)
	if view := ansi.Strip(m.View()); strings.Contains(view, "second thought") {
		t.Fatalf("the collapse row did not hide the trace:\n%s", view)
	}
}
