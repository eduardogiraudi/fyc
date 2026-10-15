package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// StatusHookValues is the editable state of one status-bar hook. It mirrors
// config.StatusBarHook without importing config (which imports ui, so the
// dependency must not be inverted).
type StatusHookValues struct {
	Enabled        bool
	IncludeSecrets bool
	Command        string
	Interval       int
	RefreshOnTurn  bool
}

type statusHookSettingsResult struct {
	values    StatusHookValues
	confirmed bool
}

// statusHookSettingsModel is an inline editor for one status-bar hook.
type statusHookSettingsModel struct {
	title   string
	v       StatusHookValues
	cursor  int
	editing bool
	editBuf string
	editCol int
	quit    bool
	ok      bool
}

func (m statusHookSettingsModel) Init() tea.Cmd { return nil }

func (m statusHookSettingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Inline editing of the command (row 1) and of the interval (row 3).
	if m.editing {
		switch key.String() {
		case "enter":
			switch m.cursor {
			case 1:
				m.v.Command = m.editBuf
			case 3:
				if n, err := strconv.Atoi(strings.TrimSpace(m.editBuf)); err == nil && n >= 0 {
					m.v.Interval = n
				}
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
		m.quit = true
		m.ok = false
		return m, tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < 5 {
			m.cursor++
		}
	case "left", "right":
		switch m.cursor {
		case 0:
			m.v.Enabled = !m.v.Enabled
		case 2:
			m.v.IncludeSecrets = !m.v.IncludeSecrets
		case 4:
			m.v.RefreshOnTurn = !m.v.RefreshOnTurn
		}
	case "enter", " ":
		switch m.cursor {
		case 0:
			m.v.Enabled = !m.v.Enabled
		case 1:
			m.editing = true
			m.editBuf = m.v.Command
			m.editCol = len([]rune(m.v.Command))
		case 2:
			m.v.IncludeSecrets = !m.v.IncludeSecrets
		case 3:
			m.editing = true
			if m.v.Interval > 0 {
				m.editBuf = strconv.Itoa(m.v.Interval)
			} else {
				m.editBuf = ""
			}
			m.editCol = len([]rune(m.editBuf))
		case 4:
			m.v.RefreshOnTurn = !m.v.RefreshOnTurn
		case 5:
			m.quit = true
			m.ok = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m statusHookSettingsModel) renderEdit() string {
	runes := []rune(m.editBuf)
	before := string(runes[:m.editCol])
	if m.editCol < len(runes) {
		cur := string(runes[m.editCol])
		after := string(runes[m.editCol+1:])
		return before + inputCursorStyle.Render(cur) + after
	}
	return before + inputCursorStyle.Render(" ")
}

func (m statusHookSettingsModel) cell(row int, value string) string {
	if m.editing && m.cursor == row {
		return m.renderEdit()
	}
	if value == "" {
		return DimStyle.Render("(not set — ↵ to edit)")
	}
	return value
}

func (m statusHookSettingsModel) View() string {
	if m.quit {
		return ""
	}

	interval := strconv.Itoa(m.v.Interval)
	rows := []string{
		fmt.Sprintf("Enabled:                  %s", onOffLabel(m.v.Enabled)),
		fmt.Sprintf("Command:                  %s", m.cell(1, m.v.Command)),
		fmt.Sprintf("Include API keys/tokens:  %s", onOffLabel(m.v.IncludeSecrets)),
		fmt.Sprintf("Refresh interval (s):     %s", m.cell(3, interval)),
		fmt.Sprintf("Refresh on every turn:    %s", onOffLabel(m.v.RefreshOnTurn)),
		"← Back",
	}

	var b strings.Builder
	b.WriteString(SelectorHeader.Render("Status Bar Hook — "+m.title) + "\n\n")
	for i, row := range rows {
		if i == m.cursor {
			b.WriteString(SelectedBg.Render(" "+RadioOn.String()+" "+row) + "\n")
		} else {
			b.WriteString(" " + RadioOff.String() + " " + row + "\n")
		}
	}
	b.WriteString("\n" + HintStyle.Render("  ▲/▼ navigate  ·  ↵ toggle / edit  ·  esc cancel edit  ·  Back to apply  ·  q discard"))
	b.WriteString("\n" + HintStyle.Render("  command env: FYC_PROVIDER, FYC_MODEL, FYC_BASE_URL, FYC_PROVIDER_TYPE  ·  FYC_API_KEY (only if include is on)"))

	return SelectorBorder.Render(b.String())
}

// StatusHookSettings shows the inline editor for one status-bar hook. Back
// applies the changes, while q/esc discards them.
func StatusHookSettings(title string, v StatusHookValues) (StatusHookValues, bool) {
	res := withExternalPrompt(func() statusHookSettingsResult {
		p := tea.NewProgram(statusHookSettingsModel{title: title, v: v})
		m, _ := p.Run()
		sm := m.(statusHookSettingsModel)
		return statusHookSettingsResult{values: sm.v, confirmed: sm.ok}
	})
	return res.values, res.confirmed
}
