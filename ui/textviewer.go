package ui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"fuckyourcli/types"
)

// TextViewerModel is a scrollable, alt-screen viewer for an approval's "See
// details" payload (a long shell command, the sandbox envelope it will run in).
// It mirrors DiffViewerModel but has no line numbers, and wraps long lines to
// the terminal width so nothing is ever cut horizontally.
//
// Lines carry a semantic kind (types.DetailLine), so the viewer can colour the
// envelope and run the Chroma shell lexer over the command section instead of
// dumping a wall of plain text. Order is what matters: content is sanitised and
// wrapped first, highlighting is applied last — the payload is untrusted, so no
// escape from it may survive to the screen.
type TextViewerModel struct {
	Title    string
	Subtitle string
	Lines    []types.DetailLine
	quitting bool
	offset   int

	termWidth  int
	termHeight int
}

// RunTextViewer shows lines in a full-screen scrollable viewer and blocks until
// the user presses ESC/q, then returns. It runs in the alternate screen, so a
// many-line listing never grows taller than the terminal — which is exactly
// what truncated a tall command in the inline permission selector.
func RunTextViewer(title, subtitle string, lines []types.DetailLine) {
	withExternalPrompt(func() struct{} {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w <= 0 {
			w = 80
		}
		if h <= 0 {
			h = 24
		}
		p := tea.NewProgram(TextViewerModel{
			Title:      title,
			Subtitle:   subtitle,
			Lines:      lines,
			termWidth:  w,
			termHeight: h,
		}, tea.WithAltScreen())
		p.Run()
		return struct{}{}
	})
}

func (m TextViewerModel) Init() tea.Cmd { return nil }

// headerLen is how many rows the fixed header occupies: the title, an optional
// subtitle and the separator.
func (m TextViewerModel) headerLen() int {
	n := 2 // title + separator
	if m.Subtitle != "" {
		n++
	}
	return n
}

// visibleHeight is how many content rows fit on screen: the terminal height
// minus the fixed header, the bottom footer and the two optional ↑/↓ scroll
// indicators. Reserving both indicators keeps the frame within the terminal in
// every scroll position (otherwise the header is clipped off the top).
func (m TextViewerModel) visibleHeight() int {
	n := m.termHeight - m.headerLen() - 3
	if n < 1 {
		n = 1
	}
	return n
}

// visualLine is one wrapped display row paired with the kind of its source
// DetailLine, so the renderer knows how to colour it.
type visualLine struct {
	text string
	kind types.DetailLineKind
}

// visualLines sanitises and wraps the content: control sequences are dropped
// and tabs expanded so the width we measure matches what the terminal prints.
// Every returned row already fits the terminal. Highlighting is deliberately
// NOT applied here — the wrapped rows are plain text, and colour is added by
// renderLine once the payload can no longer inject an escape sequence.
func (m TextViewerModel) visualLines() []visualLine {
	width := m.termWidth
	if width < 1 {
		width = 80
	}
	var out []visualLine
	for _, line := range m.Lines {
		text := expandTabs(stripControlSequences(line.Text), tabStopWidth)
		if text == "" {
			out = append(out, visualLine{kind: line.Kind})
			continue
		}
		for _, wrapped := range wrapPlainByANSIWidth(text, width) {
			out = append(out, visualLine{text: wrapped, kind: line.Kind})
		}
	}
	if len(out) == 0 {
		out = append(out, visualLine{})
	}
	return out
}

// Detail-line styles. They only set a foreground: the viewer places each row
// itself, and padding would change the width the wrapper already measured.
var (
	detailSectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fc8403"))
	detailPathStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	detailEnvStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
)

// renderLine paints one already-sanitised display row according to its kind:
// the command section goes through the shell lexer, the other kinds get a flat
// style, and plain text is left untouched.
func renderLine(v visualLine) string {
	if v.text == "" {
		return ""
	}
	switch v.kind {
	case types.DetailSection:
		return detailSectionStyle.Render(v.text)
	case types.DetailCommand:
		return highlightShellLine(v.text)
	case types.DetailPath:
		return detailPathStyle.Render(v.text)
	case types.DetailEnvKey:
		return detailEnvStyle.Render(v.text)
	default:
		return v.text
	}
}

func (m TextViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case tea.KeyMsg:
		visible := m.visibleHeight()
		maxOffset := len(m.visualLines()) - visible
		if maxOffset < 0 {
			maxOffset = 0
		}
		switch msg.String() {
		case "esc", "q":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.offset > 0 {
				m.offset--
			}
		case "down", "j":
			if m.offset < maxOffset {
				m.offset++
			}
		case "pgup", "ctrl+u":
			m.offset = max(0, m.offset-max(1, visible/2))
		case "pgdown", "ctrl+d":
			m.offset = min(maxOffset, m.offset+max(1, visible/2))
		case "home", "g":
			m.offset = 0
		case "end", "G":
			m.offset = maxOffset
		case "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m TextViewerModel) View() string {
	if m.quitting {
		return ""
	}
	visible := m.visibleHeight()
	visual := m.visualLines()
	total := len(visual)

	// Clamp the offset in case the terminal grew since the last key press.
	maxOffset := total - visible
	if maxOffset < 0 {
		maxOffset = 0
	}
	offset := m.offset
	if offset > maxOffset {
		offset = maxOffset
	}

	var b strings.Builder
	b.WriteString(m.Title)
	if m.Subtitle != "" {
		b.WriteString("\n")
		b.WriteString(diffViewStatsStyle.Render(m.Subtitle))
	}
	b.WriteString("\n")
	sepWidth := m.termWidth
	if sepWidth < 1 {
		sepWidth = 80
	}
	b.WriteString(diffViewStatsStyle.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	end := offset + visible
	if end > total {
		end = total
	}
	if offset > 0 {
		b.WriteString(diffViewIndicatorStyle.Render(fmt.Sprintf("  ↑ %d more lines", offset)))
		b.WriteString("\n")
	}
	for i := offset; i < end; i++ {
		b.WriteString(renderLine(visual[i]))
		b.WriteString("\n")
	}
	if end < total {
		b.WriteString(diffViewIndicatorStyle.Render(fmt.Sprintf("  ↓ %d more lines", total-end)))
		b.WriteString("\n")
	}

	// Fill the remaining rows so the footer sits at the bottom, then draw the
	// position indicator.
	used := m.headerLen()
	if offset > 0 {
		used++
	}
	used += end - offset
	if end < total {
		used++
	}
	for i := 0; i < m.termHeight-used-1; i++ {
		b.WriteString("\n")
	}

	var posStr string
	switch {
	case total == 0:
		posStr = "[0/0]"
	case visible >= total:
		posStr = fmt.Sprintf("[1/%d] ▏100%%", total)
	default:
		pct := 0
		if maxOffset > 0 {
			pct = (offset * 100) / maxOffset
		}
		posStr = fmt.Sprintf("[%d/%d] ▏%d%%  ↑/k ↓/j pgup/pgdn g/G q", offset+1, total, pct)
	}
	b.WriteString(diffViewStatsStyle.Render(posStr))
	return b.String()
}
