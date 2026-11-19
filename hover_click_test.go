package main

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/ui"

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

// pressEnter sends Enter the way the terminal would. Windows holds a lone Enter
// for a few milliseconds to tell it from a pasted newline, so the idle tick has
// to be delivered before the key has any effect.
func pressEnter(t *testing.T, m chatModel) (chatModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(chatModel)
	if runtime.GOOS == "windows" && cmd != nil {
		idle, ok := cmd().(ui.InputEnterIdleMsg)
		if !ok {
			t.Fatal("deferred Enter did not produce an idle tick")
		}
		next, cmd = m.Update(idle)
		m = next.(chatModel)
	}
	return m, cmd
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

func slashMouseModel(t *testing.T, height int) chatModel {
	t.Helper()
	suggestions := []ui.Suggestion{
		{Name: "/command-0", Description: "description"},
		{Name: "/command-1", Description: "description"},
		{Name: "/command-2", Description: "description"},
		{Name: "/command-3", Description: "description"},
		{Name: "/command-4", Description: "description"},
		{Name: "/command-5", Description: "description"},
		{Name: "/command-6", Description: "description"},
	}
	provider := func(line string) ([]ui.Suggestion, string) {
		var matches []ui.Suggestion
		for _, suggestion := range suggestions {
			if strings.HasPrefix(suggestion.Name, line) {
				matches = append(matches, suggestion)
			}
		}
		return matches, "extra hint"
	}
	m := newChatModel(nil, nil, nil, provider)
	m.width, m.height = 60, height
	m.statsProviderInfo = ""
	m.enableAltScreen([]string{"previous output"})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(chatModel)
	m.View()
	return m
}

func TestSlashSuggestionMouseClickUsesDrawnRows(t *testing.T) {
	for _, height := range []int{20, 6} {
		t.Run(fmt.Sprintf("height-%d", height), func(t *testing.T) {
			m := slashMouseModel(t, height)
			x, y := modalCell(t, m, "/command-2")
			next, cmd := m.Update(press(x, y))
			m = next.(chatModel)
			if cmd != nil {
				t.Fatal("the first click submitted instead of completing")
			}
			if m.transcript.SelectionText() != "" || m.transcript.selActive {
				t.Fatal("the suggestion click started a transcript selection")
			}
			view := ansi.Strip(m.View())
			if !strings.Contains(view, "/command-2") || strings.Contains(view, "/command-3") {
				t.Fatalf("click did not filter to /command-2:\n%s", view)
			}

			// The second click has the same meaning as Enter on the exact command.
			m.View()
			next, cmd = m.Update(press(2, m.hit.inputRow+2))
			m = next.(chatModel)
			if cmd == nil {
				t.Fatal("the second click did not submit the completed command")
			}
			msg, ok := cmd().(ui.InputSubmitMsg)
			if !ok || msg.Value != "/command-2" {
				t.Fatalf("second click message = %#v, want /command-2 submit", msg)
			}
		})
	}
}

func TestSlashSuggestionWheelDoesNotScrollTranscript(t *testing.T) {
	m := slashMouseModel(t, 16)
	for i := 0; i < 30; i++ {
		m.printCmd(fmt.Sprintf("line-%02d", i))
	}
	m.transcript.ScrollUp(4)
	m.View()
	offset := m.transcript.offset
	x, y := modalCell(t, m, "/command-0")
	next, cmd := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress, X: x, Y: y,
	})
	m = next.(chatModel)
	if cmd != nil || m.transcript.offset != offset {
		t.Fatal("the suggestion wheel scrolled the transcript")
	}
	m, _ = pressEnter(t, m)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "/command-1") || strings.Contains(view, "/command-2") {
		t.Fatalf("wheel did not move the suggestion selection:\n%s", view)
	}
}

func TestSlashSuggestionHoverHighlightsTheRowUnderThePointer(t *testing.T) {
	forceTrueColor(t)
	m := slashMouseModel(t, 20)
	plain := m.View()
	_, y := modalCell(t, m, "/command-2")
	next, _ := m.Update(motion(2, y))
	m = next.(chatModel)
	hovered := m.View()
	if hovered == plain {
		t.Fatal("hovering a suggestion did not change the frame")
	}
	if ansi.Strip(hovered) != ansi.Strip(plain) {
		t.Fatal("hover must only change colours, never the text or its columns")
	}

	// Leaving the popup drops the highlight.
	next, _ = m.Update(motion(2, 0))
	m = next.(chatModel)
	if m.View() != plain {
		t.Fatal("the highlight stayed after the pointer left the suggestions")
	}
}

func TestHistorySearchHoverHighlightsTheRowUnderThePointer(t *testing.T) {
	forceTrueColor(t)
	m := newChatModel(nil, nil, []string{"match 0", "match 1", "match 2"}, nil)
	m.width, m.height = 60, 12
	m.statsProviderInfo = ""
	m.enableAltScreen(nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = next.(chatModel)
	plain := m.View()
	_, y := modalCell(t, m, "match 0")
	next, _ = m.Update(motion(2, y))
	m = next.(chatModel)
	if m.View() == plain {
		t.Fatal("hovering a history row did not change the frame")
	}

	// A key press means the pointer no longer says where the user is aiming.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(chatModel)
	if m.input.HoverActive() {
		t.Fatal("a key press must clear the popup hover")
	}
}
