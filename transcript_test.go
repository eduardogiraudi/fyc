package main

import (
	"fmt"
	"strings"
	"testing"

	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTranscriptFollowsTheTail(t *testing.T) {
	tr := &transcriptState{}
	for _, row := range []string{"a", "b", "c", "d"} {
		tr.Append(row)
	}
	if got := tr.View(2); got != "c\nd" {
		t.Fatalf("View(2) = %q, want the last two rows", got)
	}
	// Whitespace-only blocks are separators already handled by the caller.
	tr.Append("  \n\n")
	if got := tr.View(4); got != "a\nb\nc\nd" {
		t.Fatalf("a blank block was appended: %q", got)
	}
}

func TestTranscriptScrollKeepsRowsOnScreen(t *testing.T) {
	tr := &transcriptState{}
	for _, row := range []string{"a", "b", "c", "d", "e", "f"} {
		tr.Append(row)
	}
	if got := tr.View(3); got != "d\ne\nf" {
		t.Fatalf("live tail = %q", got)
	}
	tr.ScrollUp(2)
	if !tr.ScrolledUp() {
		t.Fatal("scrolling up did not leave the live output")
	}
	if got := tr.View(3); got != "b\nc\nd" {
		t.Fatalf("after scrolling up = %q", got)
	}
	// New output lands below the window: a reader keeps the rows they are on.
	tr.Append("g")
	if got := tr.View(3); got != "b\nc\nd" {
		t.Fatalf("the window moved on append = %q", got)
	}
	// Scrolling past the top stops at the first row.
	tr.ScrollUp(100)
	if got := tr.View(3); got != "a\nb\nc" {
		t.Fatalf("clamped top = %q", got)
	}
	tr.ScrollDown(100)
	if tr.ScrolledUp() {
		t.Fatal("scrolling down did not return to the live output")
	}
	if got := tr.View(3); got != "e\nf\ng" {
		t.Fatalf("after returning to live output = %q", got)
	}
}

func TestTranscriptScrollTopAndFollow(t *testing.T) {
	tr := &transcriptState{}
	for i := 0; i < 10; i++ {
		tr.Append(fmt.Sprintf("row%d", i))
	}
	tr.View(4)
	tr.ScrollTop()
	if got := tr.View(4); got != "row0\nrow1\nrow2\nrow3" {
		t.Fatalf("ScrollTop = %q", got)
	}
	tr.Follow()
	if got := tr.View(4); got != "row6\nrow7\nrow8\nrow9" {
		t.Fatalf("Follow = %q", got)
	}
}

func TestTranscriptIsBounded(t *testing.T) {
	tr := &transcriptState{}
	for i := 0; i < transcriptMaxLines+50; i++ {
		tr.Append("row")
	}
	if len(tr.lines) != transcriptMaxLines {
		t.Fatalf("transcript holds %d rows, want %d", len(tr.lines), transcriptMaxLines)
	}
}

// The alt-screen frame is drawn by the app, not by the scrollback: it must fill
// the terminal exactly and keep the input on the last row.
func TestAltScreenViewFillsTheFrame(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen([]string{"banner row"})
	for i := 0; i < 40; i++ {
		m.printCmd(fmt.Sprintf("line-%02d", i))
	}
	if n := len(m.transcript.lines); n != 41 {
		t.Fatalf("transcript holds %d rows, want 41", n)
	}

	rows := strings.Split(m.View(), "\n")
	if len(rows) != m.height {
		t.Fatalf("frame has %d rows, want %d:\n%s", len(rows), m.height, m.View())
	}
	view := m.View()
	if strings.Contains(view, "line-00") {
		t.Fatal("the oldest rows should have scrolled out of the frame")
	}
	if !strings.Contains(view, "line-39") {
		t.Fatal("the live output is not on screen")
	}
	if !strings.Contains(rows[len(rows)-1], "❯") {
		t.Fatalf("the input is not the last row: %q", rows[len(rows)-1])
	}
}

// Scrolling up in the full-screen frame must show older rows and keep the input
// where it is.
func TestAltScreenViewScrollsTheTranscript(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 12
	m.enableAltScreen(nil)
	for i := 0; i < 30; i++ {
		m.printCmd(fmt.Sprintf("line-%02d", i))
	}
	m.View()
	m.transcript.ScrollUp(5)

	rows := strings.Split(m.View(), "\n")
	if len(rows) != m.height {
		t.Fatalf("frame has %d rows, want %d", len(rows), m.height)
	}
	view := m.View()
	if !strings.Contains(view, "line-24") {
		t.Fatalf("scrolled frame does not show the older rows:\n%s", view)
	}
	if strings.Contains(view, "line-29") {
		t.Fatalf("scrolled frame still shows the live output:\n%s", view)
	}
	if !strings.Contains(rows[len(rows)-1], "❯") {
		t.Fatalf("the input is not the last row: %q", rows[len(rows)-1])
	}
	if !strings.Contains(view, "rows below") {
		t.Fatalf("no scroll hint while scrolled up:\n%s", view)
	}
}

// Without a transcript the chat keeps printing into the scrollback.
func TestReplModeStillPrints(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width = 60
	if cmd := m.printCmd("hello"); cmd == nil {
		t.Fatal("the inline REPL must print the block (tea.Println), not buffer it")
	}
	if m.transcript != nil {
		t.Fatal("the inline REPL must not keep a transcript")
	}
}

// A completed turn is appended to the transcript, above the live area: the
// frame keeps the banner on top and the input on the last row.
func TestAltScreenKeepsCompletedOutputAboveTheInput(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, 24
	m.enableAltScreen([]string{"banner"})
	m.printCmd(ui.RenderSubmittedPrompt("hello there"))
	m.printCmd(ui.SeparatorStyle.Render(strings.Repeat("─", 40)))
	next, _ := m.Update(ui.StreamTextMsg{Token: "Error: boom\n\nUse /retry to repeat.\n"})
	m = next.(chatModel)
	next, _ = m.Update(generationDoneMsg{})
	m = next.(chatModel)

	rows := strings.Split(m.View(), "\n")
	if len(rows) != m.height {
		t.Fatalf("frame has %d rows, want %d", len(rows), m.height)
	}
	if !strings.HasPrefix(rows[0], "banner") {
		t.Fatalf("the transcript does not start at the top: %q", rows[0])
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "hello there") || !strings.Contains(joined, "boom") {
		t.Fatalf("the turn is missing from the transcript:\n%s", joined)
	}
	if !strings.Contains(rows[len(rows)-1], "❯") {
		t.Fatalf("the input is not the last row: %q", rows[len(rows)-1])
	}
}

// The wheel belongs to the transcript in alt-screen mode: it must not walk the
// input history (which is what the terminal's arrow-key translation would do).
func TestAltScreenWheelScrollsTheTranscript(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 12
	m.enableAltScreen(nil)
	for i := 0; i < 40; i++ {
		m.printCmd(fmt.Sprintf("line-%02d", i))
	}
	m.View()

	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = next.(chatModel)
	if got := m.transcript.offset; got != mouseScrollRows {
		t.Fatalf("wheel up moved %d rows, want %d", got, mouseScrollRows)
	}
	if view := m.View(); !strings.Contains(view, "rows below") {
		t.Fatalf("the frame does not show the scroll hint:\n%s", view)
	}
	if view := m.View(); strings.Contains(view, "line-39") {
		t.Fatalf("the live output is still on screen after scrolling up:\n%s", view)
	}

	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = next.(chatModel)
	if m.transcript.ScrolledUp() {
		t.Fatal("wheel down did not return to the live output")
	}
}

// Without a transcript the wheel must not be swallowed: the inline REPL has no
// in-app scrollback, so the event is simply ignored.
func TestReplModeIgnoresTheWheel(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width = 60
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = next.(chatModel)
	if m.transcript != nil {
		t.Fatal("the inline REPL must not keep a transcript")
	}
}
