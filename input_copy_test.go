package main

import (
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

// The prompt is decoration, not text: a drag from the left edge copies the typed
// text without the "❯ " that drew it.
func TestInputCopySkipsThePrompt(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.enableAltScreen(nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = next.(chatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha bravo")})
	m = next.(chatModel)
	m.View()

	row := m.hit.inputRow
	_, copied := copyDrag(t, m,
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: row},
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 6, Y: row},
	)
	if want := "alpha"; copied != want {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}

// A wrapped input is copied without the alignment padding every continuation row
// starts with, so pasting it does not indent the following lines.
func TestInputCopySkipsTheContinuationPadding(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.enableAltScreen(nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 20, Height: 12})
	m = next.(chatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abcdefghijklmnopqrstuvwxyz")})
	m = next.(chatModel)
	m.View()

	row := m.hit.inputRow
	if m.hit.inputRows < 2 {
		t.Fatalf("the input did not wrap: %d rows", m.hit.inputRows)
	}
	_, copied := copyDrag(t, m,
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: row},
		tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: row + 1},
	)
	if want := "abcdefghijklmnopqr\nstuv"; copied != want {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}
