package main

import (
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// generatingModel is an alt-screen chat in the middle of a turn: nothing runs
// the provider, the flag is what the commands have to respect.
func generatingModel(t *testing.T) chatModel {
	t.Helper()
	m := newChatModel(&ChatSession{}, nil, nil, nil)
	m.width, m.height = 60, 16
	m.enableAltScreen([]string{"earlier output"})
	m.generating = true
	m.View()
	return m
}

// A display command still runs while the model is generating, its output lands
// in the transient notice and never in the transcript, and it does not end the
// turn.
func TestLiveCommandRunsBesideTheGeneration(t *testing.T) {
	previous := config.Get().NoAsk
	t.Cleanup(func() { config.SetNoAsk(previous) })
	config.SetNoAsk(false)

	m := generatingModel(t)
	before := strings.Join(m.transcript.lines, "\n")

	next, cmd := m.Update(ui.InputSubmitMsg{Value: "/auto"})
	m = next.(chatModel)
	msg := runMsgs(cmd)
	done, ok := msg.(liveCommandDoneMsg)
	if !ok {
		t.Fatalf("the command produced %T, not a live command result", msg)
	}
	if !strings.Contains(done.text, "Auto mode enabled") {
		t.Fatalf("the command printed %q", done.text)
	}
	next, _ = m.Update(done)
	m = next.(chatModel)

	if !m.generating {
		t.Fatal("a display command ended the turn")
	}
	if !config.Get().NoAsk {
		t.Fatal("auto mode was not toggled")
	}
	if got := strings.Join(m.transcript.lines, "\n"); got != before {
		t.Fatalf("the command rewrote the transcript:\n%s", got)
	}
	if frame := m.View(); !strings.Contains(frame, "Auto mode enabled") {
		t.Fatalf("the notice is not on screen:\n%s", frame)
	}
	if strings.Index(strings.Join(strings.Split(m.View(), "\n"), "\n"), "earlier output") < 0 {
		t.Fatal("the transcript moved away")
	}
	// The notice sticks to the input bar: the row right above it.
	rows := strings.Split(m.View(), "\n")
	if m.hit.inputRow < 1 || !strings.Contains(rows[m.hit.inputRow-1], "Auto mode enabled") {
		t.Fatalf("the notice is not right above the input:\n%s", m.View())
	}

	// The notice clears itself, and only its own timer can clear it.
	next, _ = m.Update(noticeExpiredMsg{seq: m.noticeSeq - 1})
	if m = next.(chatModel); !strings.Contains(m.View(), "Auto mode enabled") {
		t.Fatal("a stale timer cleared the notice")
	}
	next, _ = m.Update(noticeExpiredMsg{seq: m.noticeSeq})
	if m = next.(chatModel); strings.Contains(m.View(), "Auto mode enabled") {
		t.Fatal("the notice outlived its timer")
	}
}

// A command that touches the conversation — a mode above all — has to wait, and
// the text stays in the input so it can be sent once the turn ends.
func TestBlockedCommandKeepsTheDraft(t *testing.T) {
	m := generatingModel(t)

	// Type the command and press Enter the way the terminal would: the draft
	// that survives the refusal is the input model's own buffer.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/mode chat")})
	m = next.(chatModel)
	next, submit := pressEnter(t, m)
	m = next.(chatModel)
	next, cmd := m.Update(runMsgs(submit))
	m = next.(chatModel)
	if !strings.Contains(m.View(), "/mode is not available while the model is generating") {
		t.Fatalf("the refusal is not on screen:\n%s", m.View())
	}
	// The timer is not run: it is a tea.Tick that would hold the test for ten
	// seconds (the frame test below feeds its message directly).
	if cmd == nil {
		t.Fatal("the refusal has no timer")
	}
	if strings.TrimSpace(m.transcript.lines[0]) != "earlier output" || len(m.transcript.lines) != 1 {
		t.Fatalf("the refusal went into the transcript: %q", m.transcript.lines)
	}

	// The draft is intact and submittable as soon as the turn is over.
	m.generating = false
	if !strings.Contains(m.View(), "/mode chat") {
		t.Fatalf("the draft was lost:\n%s", m.View())
	}
	next, cmd = pressEnter(t, m)
	m = next.(chatModel)
	msg := runMsgs(cmd)
	if submit, ok := msg.(ui.InputSubmitMsg); !ok || submit.Value != "/mode chat" {
		t.Fatalf("the retained draft submitted %#v", msg)
	}
}

// A plain prompt is refused the same way: there is no second turn in flight.
func TestPromptWaitsForTheTurn(t *testing.T) {
	m := generatingModel(t)
	next, cmd := m.Update(ui.InputSubmitMsg{Value: "write the file"})
	m = next.(chatModel)
	if !strings.Contains(m.View(), "wait for this turn to finish") {
		t.Fatalf("the prompt was accepted:\n%s", m.View())
	}
	if cmd == nil {
		t.Fatal("the refusal has no timer")
	}
}

// Only the command word decides what is live: arguments are fine, a path that
// merely starts with a slash is not a command at all.
func TestLiveSlashCommandClassification(t *testing.T) {
	for _, input := range []string{"/auto", "/auto on", "/temp 0.8", "/tools", "/usage", "  /help  "} {
		if !liveSlashCommand(input) {
			t.Fatalf("%q was refused", input)
		}
	}
	for _, input := range []string{"", "hello", "/usr/bin/env", "/mode chat", "/model", "/provider", "/clear", "/compact", "/retry", "/sessions", "/skills x", "/plan", "/deep", "/exit"} {
		if liveSlashCommand(input) {
			t.Fatalf("%q was allowed", input)
		}
	}
}

// The input bar is part of the frame the user can select: dragging over the
// typed text highlights it and releasing copies exactly those cells.
func TestInputBarIsSelectable(t *testing.T) {
	var copied string
	old := copyText
	copyText = func(text string) (string, error) {
		copied = text
		return "test", nil
	}
	defer func() { copyText = old }()

	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 40, 12
	m.enableAltScreen(nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha bravo")})
	m = next.(chatModel)
	m.View()

	row := m.hit.inputRow
	if !m.hit.inputOK || row < 0 {
		t.Fatalf("the input is not where the frame says it is (row %d, ok %v)", row, m.hit.inputOK)
	}
	// "❯ " is two cells of the row, so column 2 is the first typed rune.
	next, _ = m.Update(press(2, row))
	m = next.(chatModel)
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 6, Y: row})
	m = next.(chatModel)
	if !strings.Contains(m.inputView(), "\x1b[7m") {
		t.Fatalf("the dragged text is not highlighted: %q", m.inputView())
	}
	next, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 6, Y: row})
	m = next.(chatModel)

	msg := runMsgs(cmd)
	if done, ok := msg.(clipboardCopiedMsg); !ok || done.err != nil {
		t.Fatalf("the release did not copy: %#v", msg)
	}
	next, _ = m.Update(msg)
	m = next.(chatModel)
	if got, want := copied, "alpha"; got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
	if !strings.Contains(m.View(), "copied 1 line(s)") {
		t.Fatalf("the frame does not confirm the copy:\n%s", m.View())
	}
	// A drag that starts on the input never selects transcript rows.
	if m.transcript.selActive {
		t.Fatal("the input press started a transcript selection")
	}
}

// A press on the transcript still selects the transcript, and a drag that leaves
// the input bar stops at its last row instead of running into the transcript.
func TestInputSelectionStaysOnTheInputBar(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 40, 12
	m.enableAltScreen([]string{"alpha", "bravo"})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(chatModel)
	m.View()

	next, _ = m.Update(press(0, 0))
	m = next.(chatModel)
	if m.inputSel.active {
		t.Fatal("a press on the transcript started an input selection")
	}
	if !m.transcript.selActive {
		t.Fatal("a press on the transcript selected nothing")
	}

	next, _ = m.Update(press(2, m.hit.inputRow))
	m = next.(chatModel)
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 20, Y: 0})
	m = next.(chatModel)
	if !m.inputSel.active {
		t.Fatal("the drag left the input selection")
	}
	if m.inputSel.cursor.row != 0 {
		t.Fatalf("the drag ran to row %d of the input", m.inputSel.cursor.row)
	}
}
