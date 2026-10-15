package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cliSettingsResult is the outcome of the CLI settings editor.
type cliSettingsResult struct {
	enabled   bool
	midTurn   bool
	threshold int
	confirmed bool
}

// cliSettingsModel adjusts the compaction switches and token threshold inline.
type cliSettingsModel struct {
	title     string
	enabled   bool
	midTurn   bool
	threshold int
	step      int
	min       int
	max       int
	cursor    int
	quitting  bool
	confirmed bool
}

func (m cliSettingsModel) Init() tea.Cmd { return nil }

func (m cliSettingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q", "esc":
		m.quitting, m.confirmed = true, false
		return m, tea.Quit
	case "up":
		m.cursor = clampInt(m.cursor-1, 0, 3)
	case "down":
		m.cursor = clampInt(m.cursor+1, 0, 3)
	case "left", "right", "enter", " ":
		switch m.cursor {
		case 0:
			m.enabled = !m.enabled
		case 1:
			if key.String() == "left" {
				m.threshold = clampInt(m.threshold-m.step, m.min, m.max)
			} else if key.String() == "right" {
				m.threshold = clampInt(m.threshold+m.step, m.min, m.max)
			}
		case 2:
			m.midTurn = !m.midTurn
		case 3:
			if key.String() == "enter" || key.String() == " " {
				m.quitting, m.confirmed = true, true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m cliSettingsModel) View() string {
	if m.quitting {
		return ""
	}

	rows := []string{
		fmt.Sprintf("Auto Compaction: %s", onOffLabel(m.enabled)),
		fmt.Sprintf("Compaction threshold:  ◀ %d ▶  tokens", m.threshold),
		fmt.Sprintf("Mid-turn compaction (requires Auto Compaction): %s", onOffLabel(m.midTurn)),
		"← Back",
	}

	var b strings.Builder
	b.WriteString(SelectorHeader.Render(m.title) + "\n\n")
	for i, row := range rows {
		if i == m.cursor {
			b.WriteString(SelectedBg.Render(" "+RadioOn.String()+" "+row) + "\n")
		} else {
			b.WriteString(" " + RadioOff.String() + " " + row + "\n")
		}
	}
	b.WriteString("\n" + HintStyle.Render("  ▲/▼ navigate  ·  ◀/▶ adjust value  ·  ↵ toggle/confirm  ·  q/esc cancel"))

	return SelectorBorder.Render(b.String())
}

func onOffLabel(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// CLISettings shows the auto-compaction editor; Back applies, q/esc discards.
func CLISettings(title string, enabled, midTurn bool, threshold, step, minThreshold, maxThreshold int) (newEnabled, newMidTurn bool, newThreshold int, confirmed bool) {
	res := withExternalPrompt(func() cliSettingsResult {
		p := tea.NewProgram(cliSettingsModel{
			title: title, enabled: enabled, midTurn: midTurn,
			threshold: threshold, step: step, min: minThreshold, max: maxThreshold,
		})
		m, _ := p.Run()
		sm := m.(cliSettingsModel)
		return cliSettingsResult{enabled: sm.enabled, midTurn: sm.midTurn, threshold: sm.threshold, confirmed: sm.confirmed}
	})
	return res.enabled, res.midTurn, res.threshold, res.confirmed
}
