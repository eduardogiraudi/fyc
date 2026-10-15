package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// hooksSettingsResult is the outcome of the tool-hooks editor.
type hooksSettingsResult struct {
	preOn     bool
	preCmd    string
	postOn    bool
	postCmd   string
	confirmed bool
}

// hooksSettingsModel is an inline editor for the pre/post tool hooks: two
// toggles plus two editable command strings.
type hooksSettingsModel struct {
	preOn     bool
	preCmd    string
	postOn    bool
	postCmd   string
	cursor    int
	editing   bool
	editBuf   string
	editCol   int // cursor position in runes
	quitting  bool
	confirmed bool
}

func (m hooksSettingsModel) Init() tea.Cmd { return nil }

func (m hooksSettingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Inline command editing mode.
	if m.editing {
		switch key.String() {
		case "enter":
			if m.cursor == 1 {
				m.preCmd = m.editBuf
			} else {
				m.postCmd = m.editBuf
			}
			m.editing = false
			return m, nil
		case "esc", "ctrl+c":
			m.editing = false
			return m, nil
		case "left":
			if m.editCol > 0 {
				m.editCol--
			}
			return m, nil
		case "right":
			if m.editCol < len([]rune(m.editBuf)) {
				m.editCol++
			}
			return m, nil
		case "home":
			m.editCol = 0
			return m, nil
		case "end":
			m.editCol = len([]rune(m.editBuf))
			return m, nil
		case "backspace":
			runes := []rune(m.editBuf)
			if m.editCol > 0 {
				m.editBuf = string(append(runes[:m.editCol-1], runes[m.editCol:]...))
				m.editCol--
			}
			return m, nil
		case "delete":
			runes := []rune(m.editBuf)
			if m.editCol < len(runes) {
				m.editBuf = string(append(runes[:m.editCol], runes[m.editCol+1:]...))
			}
			return m, nil
		default:
			if key.Type == tea.KeyRunes {
				runes := []rune(m.editBuf)
				buf := append([]rune{}, runes[:m.editCol]...)
				buf = append(buf, key.Runes...)
				buf = append(buf, runes[m.editCol:]...)
				m.editBuf = string(buf)
				m.editCol += len(key.Runes)
			} else if key.Type == tea.KeySpace {
				runes := []rune(m.editBuf)
				buf := append([]rune{}, runes[:m.editCol]...)
				buf = append(buf, ' ')
				buf = append(buf, runes[m.editCol:]...)
				m.editBuf = string(buf)
				m.editCol++
			}
			return m, nil
		}
	}

	switch key.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		m.confirmed = false
		return m, tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < 4 {
			m.cursor++
		}
	case "left", "right":
		switch m.cursor {
		case 0:
			m.preOn = !m.preOn
		case 2:
			m.postOn = !m.postOn
		}
	case "enter", " ":
		switch m.cursor {
		case 0:
			m.preOn = !m.preOn
		case 1:
			m.editing = true
			m.editBuf = m.preCmd
			m.editCol = len([]rune(m.preCmd))
		case 2:
			m.postOn = !m.postOn
		case 3:
			m.editing = true
			m.editBuf = m.postCmd
			m.editCol = len([]rune(m.postCmd))
		case 4:
			m.quitting = true
			m.confirmed = true
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m hooksSettingsModel) renderEdit() string {
	runes := []rune(m.editBuf)
	before := string(runes[:m.editCol])
	if m.editCol < len(runes) {
		cur := string(runes[m.editCol])
		after := string(runes[m.editCol+1:])
		return before + inputCursorStyle.Render(cur) + after
	}
	return before + inputCursorStyle.Render(" ")
}

func (m hooksSettingsModel) commandCell(row int, value string) string {
	if m.editing && m.cursor == row {
		return m.renderEdit()
	}
	if value == "" {
		return DimStyle.Render("(not set — ↵ to edit)")
	}
	return value
}

func (m hooksSettingsModel) View() string {
	if m.quitting {
		return ""
	}

	rows := []string{
		fmt.Sprintf("Pre-tool hook:      %s", onOffLabel(m.preOn)),
		fmt.Sprintf("Pre-tool command:   %s", m.commandCell(1, m.preCmd)),
		fmt.Sprintf("Post-tool hook:     %s", onOffLabel(m.postOn)),
		fmt.Sprintf("Post-tool command:  %s", m.commandCell(3, m.postCmd)),
		"← Back",
	}

	var b strings.Builder
	b.WriteString(SelectorHeader.Render("Tool Hooks") + "\n\n")
	for i, row := range rows {
		if i == m.cursor {
			b.WriteString(SelectedBg.Render(" "+RadioOn.String()+" "+row) + "\n")
		} else {
			b.WriteString(" " + RadioOff.String() + " " + row + "\n")
		}
	}
	b.WriteString("\n" + HintStyle.Render("  ▲/▼ navigate  ·  ↵ toggle / edit command  ·  esc cancel edit  ·  Back to apply  ·  q discard"))

	return SelectorBorder.Render(b.String())
}

// HooksSettings shows an inline editor for the pre/post tool hooks. The Back
// row applies the changes, while q/esc discards them.
func HooksSettings(preOn bool, preCmd string, postOn bool, postCmd string) (newPreOn bool, newPreCmd string, newPostOn bool, newPostCmd string, confirmed bool) {
	res := withExternalPrompt(func() hooksSettingsResult {
		p := tea.NewProgram(hooksSettingsModel{
			preOn:   preOn,
			preCmd:  preCmd,
			postOn:  postOn,
			postCmd: postCmd,
		})
		m, _ := p.Run()
		sm := m.(hooksSettingsModel)
		return hooksSettingsResult{
			preOn:     sm.preOn,
			preCmd:    sm.preCmd,
			postOn:    sm.postOn,
			postCmd:   sm.postCmd,
			confirmed: sm.confirmed,
		}
	})
	return res.preOn, res.preCmd, res.postOn, res.postCmd, res.confirmed
}
