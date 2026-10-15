package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func settingsKey(m cliSettingsModel, key tea.KeyType) cliSettingsModel {
	updated, _ := m.Update(tea.KeyMsg{Type: key})
	return updated.(cliSettingsModel)
}

func TestCLISettingsMidTurnToggleAndConfirm(t *testing.T) {
	m := cliSettingsModel{threshold: 32000, step: 1000, min: 1000, max: 1000000}
	if !strings.Contains(m.View(), "Mid-turn compaction (requires Auto Compaction): OFF") {
		t.Fatal("opt-in switch missing from editor")
	}
	m = settingsKey(m, tea.KeyDown)
	m = settingsKey(m, tea.KeyRight)
	if m.threshold != 33000 {
		t.Fatal("threshold adjustment broken")
	}
	m = settingsKey(m, tea.KeyDown)
	m = settingsKey(m, tea.KeyEnter)
	if !m.midTurn || m.enabled || m.confirmed || m.quitting {
		t.Fatal("extra switch must not enable general compaction or confirm")
	}
	m = settingsKey(m, tea.KeyLeft)
	m = settingsKey(m, tea.KeyRight)
	if !m.midTurn {
		t.Fatal("arrow toggle broken")
	}
	m = settingsKey(m, tea.KeyDown)
	m = settingsKey(m, tea.KeyDown)
	if m.cursor != 3 {
		t.Fatal("Back must be the last row")
	}
	m = settingsKey(m, tea.KeyEnter)
	if !m.confirmed || !m.quitting || !m.midTurn {
		t.Fatal("Back must confirm all settings")
	}
}

func TestCLISettingsCancelDoesNotConfirm(t *testing.T) {
	m := cliSettingsModel{midTurn: true, cursor: 2}
	m = settingsKey(m, tea.KeyEsc)
	if !m.quitting || m.confirmed || m.View() != "" {
		t.Fatal("Escape must discard changes")
	}
}
