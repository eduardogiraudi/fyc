package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// textPromptModel is a single-line text prompt shown while the long-lived chat
// program is paused (see withExternalPrompt). Enter confirms; Esc/Ctrl+C
// cancels and returns ok=false. Running it inside withExternalPrompt is what
// makes it safe to show from a slash command: the main program releases the
// terminal first, exactly like the SelectOption* helpers.
type textPromptModel struct {
	title string
	buf   []rune
	col   int
	value string
	ok    bool
	done  bool
	width int
}

func (m textPromptModel) Init() tea.Cmd { return nil }

func (m textPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.value = string(m.buf)
			m.ok = true
			m.done = true
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			return m, tea.Quit
		case tea.KeyLeft:
			if m.col > 0 {
				m.col--
			}
		case tea.KeyRight:
			if m.col < len(m.buf) {
				m.col++
			}
		case tea.KeyHome, tea.KeyCtrlA:
			m.col = 0
		case tea.KeyEnd, tea.KeyCtrlE:
			m.col = len(m.buf)
		case tea.KeyCtrlLeft:
			m.col = findPrevWordStart(m.buf, m.col)
		case tea.KeyCtrlRight:
			m.col = findNextWordStart(m.buf, m.col)
		case tea.KeyBackspace:
			if msg.Alt {
				start := findPrevWordStart(m.buf, m.col)
				m.buf = append(m.buf[:start], m.buf[m.col:]...)
				m.col = start
			} else if m.col > 0 {
				m.buf = append(m.buf[:m.col-1], m.buf[m.col:]...)
				m.col--
			}
		case tea.KeyDelete:
			if msg.Alt {
				end := findWordEnd(m.buf, m.col)
				m.buf = append(m.buf[:m.col], m.buf[end:]...)
			} else if m.col < len(m.buf) {
				m.buf = append(m.buf[:m.col], m.buf[m.col+1:]...)
			}
		case tea.KeyCtrlK:
			m.buf = m.buf[:m.col]
		case tea.KeyCtrlU:
			m.buf = nil
			m.col = 0
		case tea.KeySpace:
			m.insertRunes([]rune{' '})
		default:
			if msg.Type == tea.KeyRunes {
				m.insertRunes(msg.Runes)
			}
		}
	}
	return m, nil
}

func (m *textPromptModel) insertRunes(runes []rune) {
	out := make([]rune, 0, len(m.buf)+len(runes))
	out = append(out, m.buf[:m.col]...)
	out = append(out, runes...)
	out = append(out, m.buf[m.col:]...)
	m.buf = out
	m.col += len(runes)
}

func (m textPromptModel) View() string {
	if m.done {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = terminalWidth()
	}
	avail := width - 4
	if avail < 10 {
		avail = 10
	}

	var b strings.Builder
	b.WriteString(TitleStyle.Render(m.title) + "\n")

	wrapped, cursorRow, cursorCol := wrapLine(m.buf, m.col, avail, true)
	for r, wl := range wrapped {
		if r == cursorRow {
			runes := []rune(wl)
			var before, cur, after string
			if cursorCol < len(runes) {
				before = string(runes[:cursorCol])
				cur = string(runes[cursorCol])
				after = string(runes[cursorCol+1:])
			} else {
				before = wl
				cur = " "
			}
			b.WriteString(inputTextStyle.Render(before))
			b.WriteString(inputCursorStyle.Render(cur))
			b.WriteString(inputTextStyle.Render(after))
		} else {
			b.WriteString(inputTextStyle.Render(wl))
		}
		b.WriteString("\n")
	}

	b.WriteString(HintStyle.Render("  ↵ confirm  ·  esc cancel"))
	return ClampBlock(b.String(), width)
}

// InputLine prompts for a single line of text, exclusively taking over the
// terminal while the chat program is paused. ok is false when the user cancels
// (Esc / Ctrl+C). It is the safe replacement for OutputSink.ReadLine inside a
// running TUI: a bare nested tea.Program would fight the main one for stdin.
func InputLine(title string) (string, bool) {
	res := withExternalPrompt(func() struct {
		value string
		ok    bool
	} {
		p := tea.NewProgram(textPromptModel{title: title, width: terminalWidth()})
		m, _ := p.Run()
		tm := m.(textPromptModel)
		return struct {
			value string
			ok    bool
		}{tm.value, tm.ok}
	})
	return res.value, res.ok
}
