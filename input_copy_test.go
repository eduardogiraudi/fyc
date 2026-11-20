package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// copyDrag runs a left-button drag over the frame and returns what reached the
// clipboard.
func copyDrag(t *testing.T, m chatModel, from, to tea.MouseMsg) (chatModel, string) {
	t.Helper()
	var copied string
	old := copyText
	copyText = func(text string) (string, error) {
		copied = text
		return "test", nil
	}
	defer func() { copyText = old }()

	next, _ := m.Update(from)
	m = next.(chatModel)
	next, _ = m.Update(to)
	m = next.(chatModel)
	release := to
	release.Action = tea.MouseActionRelease
	next, cmd := m.Update(release)
	m = next.(chatModel)
	if msg := runMsgs(cmd); msg != nil {
		next, _ = m.Update(msg)
		m = next.(chatModel)
	}
	return m, copied
}

// inputGutter is how many leading cells of the input bar are prompt decoration:
// the "❯ " prompt, which also carries the git branch when the tests run inside a
// checkout, plus the padding that keeps wrapped rows aligned under it.
func inputGutter(t *testing.T, m chatModel) int {
	t.Helper()
	rows, gutters := m.inputRows()
	if len(rows) == 0 || len(gutters) == 0 {
		t.Fatal("the input bar drew no row")
	}
	return gutters[0]
}

// The prompt is decoration, not text: a drag from the left edge copies the typed
// text without the prompt that drew it.
func TestInputCopySkipsThePrompt(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.enableAltScreen(nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	m = next.(chatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha bravo")})
	m = next.(chatModel)
	m.View()

	row := m.hit.inputRow
	gutter := inputGutter(t, m)
	_, copied := copyDrag(t, m,
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: row},
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: gutter + 4, Y: row},
	)
	if want := "alpha"; copied != want {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}

// A wrapped input is copied without the alignment padding every continuation row
// starts with, so pasting it does not indent the following lines.
func TestInputCopySkipsTheContinuationPadding(t *testing.T) {
	// A wide terminal keeps room for the text whatever the branch is called.
	const width = 120
	m := newChatModel(nil, nil, nil, nil)
	m.enableAltScreen(nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
	m = next.(chatModel)
	m.View()
	// The prompt can be wider than "❯ " (it carries the git branch), so how much
	// text fits on a row — and what fills two rows — depends on its width.
	gutter := inputGutter(t, m)
	avail := width - gutter
	if avail < 5 {
		t.Fatalf("the prompt leaves %d cells for the text", avail)
	}
	text := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 20)[:2*avail+4]
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	m = next.(chatModel)
	m.View()

	row := m.hit.inputRow
	if m.hit.inputRows < 2 {
		t.Fatalf("the input did not wrap: %d rows", m.hit.inputRows)
	}
	// From the left edge to four cells past the padding of the second row.
	_, copied := copyDrag(t, m,
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: row},
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: gutter + 3, Y: row + 1},
	)
	if want := text[:avail] + "\n" + text[avail:avail+4]; copied != want {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}
