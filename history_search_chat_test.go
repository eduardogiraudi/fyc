package main

import (
	"strings"
	"testing"

	"fuckyourcli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestChatHistorySearchCtrlCOwnsInput(t *testing.T) {
	for _, alt := range []bool{false, true} {
		for _, generating := range []bool{false, true} {
			s := &ChatSession{}
			ctx := newGenerationContext(s)
			m := newChatModel(s, nil, []string{"first", "second"}, nil)
			m.generating = generating
			if alt {
				m.enableAltScreen(nil)
			}
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
			m = updated.(chatModel)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			m = updated.(chatModel)
			if cmd != nil || m.quitting || m.input.HistorySearchActive() || ctx.Err() != nil {
				t.Fatalf("Ctrl+C must only close search (alt=%v, generating=%v)", alt, generating)
			}
			finishGeneration(s)
		}
	}
}

func TestChatHistorySearchBarSurvivesShortFrames(t *testing.T) {
	m := newChatModel(nil, nil, []string{"first", "second", "third", "fourth", "fifth"}, nil)
	m.enableAltScreen(nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = updated.(chatModel)
	for _, height := range []int{1, 3, 24} {
		updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
		m = updated.(chatModel)
		rows := strings.Split(ansi.Strip(m.View()), "\n")
		barRow := -1
		for i, row := range rows {
			if strings.Contains(row, "Search history") {
				barRow = i
			}
		}
		if len(rows) != height || barRow < 0 {
			t.Fatalf("search bar must remain visible at height %d:\n%s", height, m.View())
		}
		for i, row := range rows {
			if strings.Contains(row, "› ") && i <= barRow {
				t.Fatalf("search results must be below the bar:\n%s", m.View())
			}
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(chatModel)
		if height >= 3 && !strings.Contains(ansi.Strip(m.View()), "› ") {
			t.Fatal("selected result must remain visible in short frames")
		}
	}
}

func TestChatHistorySearchMouseUsesDrawnRows(t *testing.T) {
	for _, height := range []int{4, 12} {
		m := newChatModel(nil, nil, []string{"match 0", "match 1", "match 2", "match 3", "match 4", "match 5"}, nil)
		m.width, m.height = 60, height
		m.statsProviderInfo = ""
		m.enableAltScreen(nil)
		for i := 0; i < 20; i++ {
			m.printCmd("previous output")
		}
		m.transcript.ScrollUp(4)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		m = updated.(chatModel)
		m.View()
		x, y := modalCell(t, m, "match 5")
		offset := m.transcript.offset
		for i := 0; i < 4; i++ {
			next, cmd := m.Update(tea.MouseMsg{
				Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress, X: x, Y: y,
			})
			m = next.(chatModel)
			if cmd != nil {
				t.Fatalf("history wheel returned a command at height %d", height)
			}
			m.View()
		}
		if m.transcript.offset != offset {
			t.Fatalf("history wheel scrolled the transcript at height %d", height)
		}
		x, y = modalCell(t, m, "match 1")
		next, cmd := m.Update(press(x, y))
		m = next.(chatModel)
		if cmd != nil || m.input.HistorySearchActive() {
			t.Fatalf("click did not recall the history entry at height %d", height)
		}
		if m.transcript.selActive || m.transcript.SelectionText() != "" {
			t.Fatal("history click started a transcript selection")
		}
		next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(chatModel)
		if cmd == nil {
			t.Fatalf("recalled entry did not reach the input at height %d", height)
		}
		msg, ok := cmd().(ui.InputSubmitMsg)
		if !ok || msg.Value != "match 1" {
			t.Fatalf("recalled entry = %#v at height %d, want match 1", msg, height)
		}
	}
}
