package main

import (
	"strings"
	"testing"

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
