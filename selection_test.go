package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// selectableModel is a chat in alt-screen mode with a known transcript.
func selectableModel(t *testing.T, rows []string) chatModel {
	t.Helper()
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 40, 12
	m.enableAltScreen(rows)
	// The frame fixes the mapping between screen cells and transcript rows.
	m.View()
	return m
}

// Dragging from one cell to another selects the text in between, one line per
// transcript row and without the trailing padding.
func TestSelectionCoversTheDraggedRows(t *testing.T) {
	m := selectableModel(t, []string{"alpha", "bravo charlie", "delta"})
	m.transcript.SelectionStart(0, 2)
	m.transcript.SelectionExtend(1, 3)

	if got, want := m.transcript.SelectionText(), "pha\nbrav"; got != want {
		t.Fatalf("selection = %q, want %q", got, want)
	}
	if from, to, ok := m.transcript.selectionSpan(0); !ok || from != 2 || to != math.MaxInt {
		t.Fatalf("first row span = [%d,%d) ok=%v, want the tail from column 2", from, to, ok)
	}
	if from, to, ok := m.transcript.selectionSpan(1); !ok || from != 0 || to != 4 {
		t.Fatalf("second row span = [%d,%d) ok=%v, want the head up to column 3", from, to, ok)
	}
	if _, _, ok := m.transcript.selectionSpan(2); ok {
		t.Fatal("a row below the drag must stay unhighlighted")
	}
}

// A drag that ends where it started selects nothing, so a plain click must not
// write an empty string to the clipboard.
func TestSelectionIgnoresAPlainClick(t *testing.T) {
	m := selectableModel(t, []string{"alpha"})
	m.transcript.SelectionStart(0, 2)
	if got := m.transcript.SelectionText(); got != "" {
		t.Fatalf("a click selected %q", got)
	}
}

// Releasing the mouse hands the selection to the clipboard, in both directions
// (dragging upwards is not a different gesture).
func TestMouseDragCopiesToTheClipboard(t *testing.T) {
	var copied string
	old := copyText
	copyText = func(text string) (string, error) {
		copied = text
		return "test", nil
	}
	defer func() { copyText = old }()

	m := selectableModel(t, []string{"alpha", "bravo", "charlie"})
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 1})
	m = next.(chatModel)
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 2, Y: 2})
	m = next.(chatModel)
	next, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 2, Y: 2})
	m = next.(chatModel)
	if m.transcript.selActive || m.transcript.SelectionText() != "" {
		t.Fatal("the highlight outlived the copy")
	}

	msg := runMsgs(cmd)
	if msg == nil {
		t.Fatal("releasing the mouse ran nothing")
	}
	next, _ = m.Update(msg)
	m = next.(chatModel)
	if got, want := copied, "bravo\ncha"; got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
	if !strings.Contains(m.View(), "copied 2 line(s)") {
		t.Fatalf("the frame does not confirm the copy:\n%s", m.View())
	}
	next, _ = m.Update(copyNoticeExpiredMsg{})
	m = next.(chatModel)
	if strings.Contains(m.View(), "copied") {
		t.Fatal("the confirmation outlived its timer")
	}
}

// The confirmation has to match what actually happened: a terminal helper or an
// X11 selection we own really did take the text, while OSC 52 is fire-and-forget
// and must not read as a confirmation where the terminal drops it (VTE).
func TestCopyNoticeTracksTheRoute(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		vte      string
		want     string
		wantFail bool
	}{
		{name: "native helper", source: "wl-copy", want: "copied 2 line(s) to the clipboard"},
		{name: "x11 selection", source: "x11", want: "copied 2 line(s) to the clipboard"},
		{name: "osc52 elsewhere", source: "osc52", want: "copied 2 line(s) via OSC 52"},
		{name: "osc52 on vte", source: "osc52", vte: "7600", want: "not copied", wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VTE_VERSION", tc.vte)
			m := selectableModel(t, []string{"alpha", "bravo"})
			next, _ := m.Update(clipboardCopiedMsg{lines: 2, source: tc.source})
			m = next.(chatModel)
			if !strings.Contains(m.View(), tc.want) {
				t.Fatalf("notice does not contain %q:\n%s", tc.want, m.View())
			}
			if m.copyNoticeErr != tc.wantFail {
				t.Fatalf("copyNoticeErr = %v, want %v", m.copyNoticeErr, tc.wantFail)
			}
		})
	}
}

// The wheel and the keyboard keep scrolling the transcript; a selection does not
// swallow them.
func TestSelectionDoesNotBlockScrolling(t *testing.T) {
	rows := make([]string, 60)
	for i := range rows {
		rows[i] = fmt.Sprintf("row-%02d", i)
	}
	m := selectableModel(t, rows)
	m.transcript.SelectionStart(0, 0)
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = next.(chatModel)
	if !m.transcript.ScrolledUp() {
		t.Fatal("the wheel stopped scrolling while a selection was open")
	}
}
