package main

import (
	"fmt"
	"strings"
	"testing"

	"fuckyourcli/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// forceTrueColor makes the renderer keep its styles: a test has no TTY, and
// lipgloss would otherwise strip them and hide the highlight entirely.
func forceTrueColor(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
}

// clickableModel is an alt-screen chat with a full transcript, drawn once so the
// click geometry is the one the user would be looking at.
func clickableModel(t *testing.T, height int) chatModel {
	t.Helper()
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, height
	m.enableAltScreen(nil)
	for i := 0; i < 40; i++ {
		m.printCmd(fmt.Sprintf("line-%02d", i))
	}
	m.View()
	return m
}

func press(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: x, Y: y}
}

func motion(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Button: tea.MouseButtonNone, Action: tea.MouseActionMotion, X: x, Y: y}
}

// The [Mode: …] badge is a button: clicking it flips auto mode exactly like
// ctrl+o, without starting a text selection.
func TestModeBadgeClickTogglesAutoMode(t *testing.T) {
	previous := config.Get().NoAsk
	t.Cleanup(func() { config.SetNoAsk(previous) })
	config.SetNoAsk(false)

	m := clickableModel(t, 16)
	if !m.hit.modeOK {
		t.Fatalf("the mode badge is not clickable:\n%s", m.View())
	}
	rows := strings.Split(m.View(), "\n")
	if row := rows[m.hit.modeRow]; !strings.Contains(row, "[Mode: manual]") {
		t.Fatalf("row %d is not the status line: %q", m.hit.modeRow, row)
	}

	next, cmd := m.Update(press(m.hit.modeCol+1, m.hit.modeRow))
	m = next.(chatModel)
	if cmd == nil {
		t.Fatal("clicking the badge ran nothing")
	}
	if m.transcript.selActive {
		t.Fatal("the click started a text selection instead of pressing the button")
	}
	next, _ = m.Update(cmd())
	m = next.(chatModel)

	if !config.Get().NoAsk {
		t.Fatal("the click did not enable auto mode")
	}
	if view := m.View(); !strings.Contains(view, "[Mode: auto]") {
		t.Fatalf("the status line still shows the old mode:\n%s", view)
	}
}

// The "N rows below" hint is a button too: clicking it returns to the live
// output, which is what ctrl+end does with the keyboard.
func TestScrollHintClickFollowsTheLiveOutput(t *testing.T) {
	m := clickableModel(t, 12)
	m.transcript.ScrollUp(5)
	rows := strings.Split(m.View(), "\n")
	if !m.hit.scrollOK {
		t.Fatalf("the scroll hint is not clickable:\n%s", m.View())
	}
	if row := rows[m.hit.scrollRow]; !strings.Contains(row, "rows below") {
		t.Fatalf("row %d is not the scroll hint: %q", m.hit.scrollRow, row)
	}

	next, _ := m.Update(press(4, m.hit.scrollRow))
	m = next.(chatModel)
	if m.transcript.ScrolledUp() {
		t.Fatal("the click did not return to the live output")
	}
	view := m.View()
	if strings.Contains(view, "rows below") {
		t.Fatalf("the hint outlived the jump:\n%s", view)
	}
	if !strings.Contains(view, "line-39") {
		t.Fatalf("the live output is not back on screen:\n%s", view)
	}
}

// Hovering a button paints its background; leaving restores the idle look.
func TestHoverHighlightsTheModeBadge(t *testing.T) {
	forceTrueColor(t)
	m := clickableModel(t, 16)
	idle := strings.Split(m.View(), "\n")[m.hit.modeRow]

	next, _ := m.Update(motion(m.hit.modeCol+1, m.hit.modeRow))
	m = next.(chatModel)
	if m.hover != hoverMode {
		t.Fatalf("hover = %v, want the mode badge", m.hover)
	}
	if hovered := strings.Split(m.View(), "\n")[m.hit.modeRow]; hovered == idle {
		t.Fatalf("the badge did not change under the pointer: %q", idle)
	}

	next, _ = m.Update(motion(0, 0))
	m = next.(chatModel)
	if m.hover != hoverNone {
		t.Fatalf("hover = %v after leaving the badge, want none", m.hover)
	}
	if moved := strings.Split(m.View(), "\n")[m.hit.modeRow]; moved != idle {
		t.Fatalf("the badge kept its highlight after the pointer left:\n%q", moved)
	}
}

// The scroll hint lights up too: it is the second button of the frame.
func TestHoverHighlightsTheScrollHint(t *testing.T) {
	forceTrueColor(t)
	m := clickableModel(t, 12)
	m.transcript.ScrollUp(5)
	idle := strings.Split(m.View(), "\n")[m.hit.scrollRow]

	next, _ := m.Update(motion(2, m.hit.scrollRow))
	m = next.(chatModel)
	if m.hover != hoverScrollBack {
		t.Fatalf("hover = %v, want the scroll hint", m.hover)
	}
	if hovered := strings.Split(m.View(), "\n")[m.hit.scrollRow]; hovered == idle {
		t.Fatalf("the hint did not change under the pointer: %q", idle)
	}
}

// The brackets of the badge are decoration: only what sits between them is the
// button, so a click on "[" or "]" must not toggle auto mode.
func TestModeBadgeIsClickableOnlyInsideTheBrackets(t *testing.T) {
	previous := config.Get().NoAsk
	t.Cleanup(func() { config.SetNoAsk(previous) })
	config.SetNoAsk(false)

	m := clickableModel(t, 16)
	row := strings.Split(m.View(), "\n")[m.hit.modeRow]
	if got := ansi.Cut(ansi.Strip(row), m.hit.modeCol-1, m.hit.modeCol); got != "[" {
		t.Fatalf("the hit area does not start after the bracket, found %q in %q", got, row)
	}
	if got := ansi.Cut(ansi.Strip(row), m.hit.modeCol+m.hit.modeWidth, m.hit.modeCol+m.hit.modeWidth+1); got != "]" {
		t.Fatalf("the hit area does not end before the bracket, found %q in %q", got, row)
	}
	if m.hitAt(m.hit.modeCol-1, m.hit.modeRow) != hoverNone {
		t.Fatal("the opening bracket is clickable")
	}
	if m.hitAt(m.hit.modeCol+m.hit.modeWidth, m.hit.modeRow) != hoverNone {
		t.Fatal("the closing bracket is clickable")
	}
	if m.hitAt(m.hit.modeCol, m.hit.modeRow) != hoverMode {
		t.Fatal("the badge text is not clickable")
	}
}

// Without the mouse (the inline REPL draws no frame and owns no geometry) a
// stray event must not be taken for a click on something.
func TestHoverIsIgnoredOutsideTheAltScreen(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width = 60
	next, _ := m.Update(motion(0, 0))
	m = next.(chatModel)
	if m.hover != hoverNone {
		t.Fatalf("the inline REPL handled a hover (hover=%v)", m.hover)
	}
	if m.hitAt(0, 0) != hoverNone {
		t.Fatal("a model without a frame must report no clickable part")
	}
}

// Returning to the live output removes the hint from the frame: the pointer is
// no longer on any button.
func TestHoverClearsWithTheScrollHint(t *testing.T) {
	m := clickableModel(t, 12)
	m.transcript.ScrollUp(5)
	m.View()
	next, _ := m.Update(motion(2, m.hit.scrollRow))
	m = next.(chatModel)
	if m.hover != hoverScrollBack {
		t.Fatalf("hover = %v, want the scroll hint", m.hover)
	}

	for i := 0; i < 2; i++ {
		next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
		m = next.(chatModel)
	}
	if m.transcript.ScrolledUp() {
		t.Fatal("the wheel did not return to the live output")
	}
	if m.hover != hoverNone {
		t.Fatalf("hover = %v after the hint left the frame, want none", m.hover)
	}
}
