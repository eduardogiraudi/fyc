package ui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"fuckyourcli/types"
)

// DiffLineKind categorises a single line in a unified diff. It is an alias of
// types.DiffLineKind so the interaction layer (tools.Interactor) can carry diff
// payloads without importing the terminal UI.
type DiffLineKind = types.DiffLineKind

const (
	DiffContext    = types.DiffContext    // context line (' ')
	DiffAdded      = types.DiffAdded      // addition line ('+')
	DiffRemoved    = types.DiffRemoved    // deletion line ('-')
	DiffHunkHeader = types.DiffHunkHeader // @@ hunk header
)

// DiffLine represents one line of a diff with its semantic kind.
type DiffLine = types.DiffLine

// DiffViewerModel is a scrollable TUI for viewing a unified diff.
// Use tea.NewProgram(DiffViewerModel{...}) and p.Run().
type DiffViewerModel struct {
	Title    string
	Stats    string
	Lines    []DiffLine
	Filename string
	quitting bool
	offset   int // visual scroll offset (rows from top)

	termWidth  int
	termHeight int
	headerRows int // how many rows the header occupies
}

// ensureDiffLineStyles defines the lipgloss styles used for diff lines.
var (
	diffViewAddStyle       = lipgloss.NewStyle().Background(lipgloss.Color("#008000"))            // dark green bg
	diffViewDelStyle       = lipgloss.NewStyle().Background(lipgloss.Color("#C30913"))            // dark red bg
	diffViewCtxStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#cccccc"))            // light grey fg
	diffViewHunkStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403")).Bold(true) // orange
	diffViewStatsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#bcbcbc"))
	diffViewIndicatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#585858")).Italic(true)
)

// ansiCyan is a raw ANSI escape for light cyan foreground (used for line number gutter).
// ansiLineNum is the ANSI foreground escape matching BlueStyle (#89b4fa).
var ansiLineNum = FgANSI("#89b4fa")

// ansiReset is the standard SGR reset sequence.
const ansiReset = "\x1b[0m"

// Background ANSI escapes for full-width diff backgrounds.
var (
	ansiAddBg = BgANSI("#143417") // dark green
	ansiDelBg = BgANSI("#3c1618") // dark red
)

// RunDiffViewer creates and runs a full-screen diff viewer program.
// It blocks until the user presses ESC or q, then returns.
func RunDiffViewer(title, stats, filename string, lines []DiffLine) {
	withExternalPrompt(func() struct{} {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))

		if err != nil || w <= 0 {
			w = 80
		}
		if h <= 0 {
			h = 24
		}

		model := DiffViewerModel{
			Title:      title,
			Stats:      stats,
			Filename:   filename,
			Lines:      lines,
			termWidth:  w,
			termHeight: h,
			headerRows: 3,
		}
		model.offset = model.firstChangedVisualOffset()

		p := tea.NewProgram(model, tea.WithAltScreen())
		p.Run()
		return struct{}{}
	})
}

func (m DiffViewerModel) Init() tea.Cmd {
	// Enter alt screen, no special init needed
	return nil
}

// visibleHeight is how many diff rows fit on screen: the terminal height minus
// the header rows, the bottom footer and the two optional ↑/↓ scroll
// indicators. Reserving both indicators keeps the frame within the terminal in
// every scroll position (otherwise the header is clipped off the top).
func (m DiffViewerModel) visibleHeight() int {
	n := m.termHeight - m.headerRows - 3
	if n < 1 {
		n = 1
	}
	return n
}

// diffVisualLine is one terminal row derived from a logical diff line.
// Continuations keep the source line's style but deliberately leave its gutter
// blank: line numbers refer to source lines, never to wrapped screen rows.
type diffVisualLine struct {
	line         DiffLine
	content      string
	gutter       string
	continuation bool
}

func diffGutter(line DiffLine) string {
	if line.LineNum > 0 {
		return fmt.Sprintf("%4d │ ", line.LineNum)
	}
	return "     │ "
}

func diffPrefix(kind DiffLineKind, continuation bool) string {
	if continuation {
		return "  "
	}
	switch kind {
	case DiffAdded:
		return "+ "
	case DiffRemoved:
		return "- "
	default:
		return "  "
	}
}

// visualLines wraps each source line to the space remaining after its gutter
// and marker. Wrapping precedes highlighting so ANSI escapes cannot affect the
// measurement or cause the terminal to soft-wrap behind the viewer's back.
func (m DiffViewerModel) visualLines() []diffVisualLine {
	width := m.termWidth
	if width < 1 {
		width = 80
	}
	var out []diffVisualLine
	for _, line := range m.Lines {
		gutter := diffGutter(line)
		prefix := diffPrefix(line.Kind, false)
		continuationGutter := "     │ "
		contentWidth := width - max(DisplayWidth(gutter), DisplayWidth(continuationGutter)) - DisplayWidth(prefix)
		if contentWidth < 1 {
			contentWidth = 1
		}

		content := stripControlSequences(line.Content)
		// Tabs must be expanded from their actual column, after the gutter and
		// diff marker, rather than from column zero.
		leading := strings.Repeat(" ", DisplayWidth(gutter)+DisplayWidth(prefix))
		content = strings.TrimPrefix(expandTabs(leading+content, tabStopWidth), leading)
		wrapped := wrapPlainByANSIWidth(content, contentWidth)
		for i, part := range wrapped {
			out = append(out, diffVisualLine{
				line:         line,
				content:      part,
				gutter:       gutter,
				continuation: i > 0,
			})
		}
	}
	return out
}

func (m DiffViewerModel) firstChangedVisualOffset() int {
	for i, line := range m.visualLines() {
		if line.line.Kind != DiffContext {
			return i
		}
	}
	return 0
}

func (m DiffViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.offset = min(m.offset, max(0, len(m.visualLines())-m.visibleHeight()))
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

func (m DiffViewerModel) View() string {
	if m.quitting {
		return ""
	}
	visual := m.visualLines()
	visible := m.visibleHeight()
	total := len(visual)
	maxOffset := max(0, total-visible)
	offset := min(m.offset, maxOffset)
	end := min(offset+visible, total)

	var b strings.Builder
	b.WriteString(m.Title)
	if m.Stats != "" {
		b.WriteString("  ")
		b.WriteString(m.Stats)
	}
	b.WriteString("\n")
	sepWidth := m.termWidth
	if sepWidth < 1 {
		sepWidth = 80
	}
	b.WriteString(diffViewStatsStyle.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	if offset > 0 {
		b.WriteString(diffViewIndicatorStyle.Render(fmt.Sprintf("  ↑ %d more lines", offset)))
		b.WriteString("\n")
	}
	for _, v := range visual[offset:end] {
		b.WriteString(renderDiffVisualLine(v, m.Filename, m.termWidth))
		b.WriteString("\n")
	}
	if end < total {
		b.WriteString(diffViewIndicatorStyle.Render(fmt.Sprintf("  ↓ %d more lines", total-end)))
		b.WriteString("\n")
	}

	used := m.headerRows
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
	return ClampBlock(b.String(), m.termWidth)
}

func renderDiffVisualLine(v diffVisualLine, filename string, termWidth int) string {
	gutter := v.gutter
	if v.continuation {
		gutter = "     │ "
	}
	prefix := diffPrefix(v.line.Kind, v.continuation)
	content := v.content
	if v.line.Kind != DiffHunkHeader {
		content = highlightLine(content, filename)
	}
	body := prefix + content

	var style lipgloss.Style
	var bgANSI string
	switch v.line.Kind {
	case DiffAdded:
		style, bgANSI = diffViewAddStyle, ansiAddBg
	case DiffRemoved:
		style, bgANSI = diffViewDelStyle, ansiDelBg
	case DiffContext:
		style = diffViewCtxStyle
	case DiffHunkHeader:
		style = diffViewHunkStyle
	}
	if v.line.Kind != DiffAdded && v.line.Kind != DiffRemoved {
		return ansiLineNum + gutter + ansiReset + style.Render(body)
	}

	targetWidth := max(1, termWidth)
	if extra := targetWidth - DisplayWidth(gutter) - DisplayWidth(body); extra > 0 {
		body += strings.Repeat(" ", extra)
	}
	rendered := style.Render(body)
	rendered = strings.ReplaceAll(rendered, ansiReset, ansiReset+bgANSI)
	return ansiLineNum + gutter + ansiReset + rendered + ansiReset
}

var lexerCache = struct {
	sync.RWMutex
	m map[string]chroma.Lexer
}{
	m: make(map[string]chroma.Lexer),
}

func getLexer(filename string) chroma.Lexer {
	lexerCache.RLock()
	if l, ok := lexerCache.m[filename]; ok {
		lexerCache.RUnlock()
		return l
	}
	lexerCache.RUnlock()

	lexerCache.Lock()
	defer lexerCache.Unlock()
	if l, ok := lexerCache.m[filename]; ok {
		return l
	}

	l := lexers.Match(filename)
	if l == nil {
		l = lexers.Get("text")
	}
	lexerCache.m[filename] = l
	return l
}

// highlightWith renders one line with a Chroma lexer using the shared style and
// the 256-colour formatter. A nil lexer, a tokenisation error or a formatting
// error all fall back to the untokenised text, so highlighting can never lose
// the content it decorates.
func highlightWith(lexer chroma.Lexer, text string) string {
	if lexer == nil || text == "" {
		return text
	}
	var buf bytes.Buffer
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		return text
	}
	if err := formatters.Get("terminal256").Format(&buf, styles.GitHubDark, iterator); err != nil {
		return text
	}
	// Chroma's terminal formatter closes every token with a reset; a trailing
	// newline some lexers emit would desynchronise the viewer's row accounting.
	return strings.TrimRight(buf.String(), "\n")
}

func highlightLine(text, filename string) string {
	return highlightWith(getLexer(filename), text)
}

// shellLexerOnce is the cached bash lexer for the "See details" command
// section: the shell is the only language an approval's detail payload shows.
var shellLexerOnce = sync.OnceValue(func() chroma.Lexer { return lexers.Get("bash") })

// highlightShellLine colourises one line of a shell command.
func highlightShellLine(text string) string {
	return highlightWith(shellLexerOnce(), text)
}
