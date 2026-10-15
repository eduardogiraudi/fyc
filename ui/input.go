package ui

import (
	"bytes"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// ── styles ────────────────────────────────────────────────────────────────────

var inputPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403")).Bold(true)
var inputTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403"))
var inputCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403")).Reverse(true)

// ── SuggestionProvider ────────────────────────────────────────────────────────

// SuggestionProvider is called on every keystroke.
// Returns (matches, extraLine) where extraLine is an optional hint string
// shown below the suggestion list (e.g. "Available models: a, b, c").
type SuggestionProvider func(line string) (matches []Suggestion, extraLine string)

const maxVisibleSuggestions = 4

// ── inputModel (bubbletea.Model) ──────────────────────────────────────────────

type InputModel = inputModel

type InputSubmitMsg struct{ Value string }
type InputCancelMsg struct{}

// InputEnterIdleMsg is sent by the deferred-Enter timer on Windows. It carries
// a sequence number so that a stale timer (from an earlier Enter) is ignored.
type InputEnterIdleMsg struct{ seq uint64 }

// enterIdleDelay is how long an Enter is held on Windows before it is treated
// as a submit. If more input arrives within this window the Enter is instead
// reinterpreted as a literal newline (see Update). Needed because on Windows
// bubbletea reads the console via coninput and delivers a pasted newline as a
// discrete KeyEnter, indistinguishable from a typed one.
const enterIdleDelay = 25 * time.Millisecond

type inputModel struct {
	lines [][]rune // multi-line buffer
	row   int
	col   int

	gitBranch string // current git branch, empty if not in a git repo

	history []string
	histIdx int    // points into history; len(history) means "live" slot
	liveBuf string // saved live buffer when navigating history

	suggProvider SuggestionProvider
	suggestions  []Suggestion
	suggExtra    string
	suggSelected int
	suggOffset   int

	submitted bool
	value     string

	width int

	submitDisabled bool

	// pendingEnter / enterSeq implement the Windows deferred-Enter heuristic:
	// a KeyEnter is held for enterIdleDelay; if more input arrives first it is
	// turned into a newline rather than a submit.
	pendingEnter bool
	enterSeq     uint64
}

func NewInputModel(history []string, sp SuggestionProvider) InputModel {
	return newInputModel(history, sp)
}

func newInputModel(history []string, sp SuggestionProvider) inputModel {
	w, _, _ := term.GetSize(int(os.Stdout.Fd()))
	if w <= 0 {
		w = 80
	}
	branch := detectGitBranch()
	return inputModel{
		lines:        [][]rune{{}},
		history:      history,
		histIdx:      len(history),
		suggProvider: sp,
		width:        w,
		gitBranch:    branch,
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// isWordRune returns true if the rune is part of a word (not whitespace or punctuation)
func isWordRune(r rune) bool {
	return !unicode.IsSpace(r) && !unicode.IsPunct(r)
}

// findPrevWordStart finds the start of the previous word from the given column
func findPrevWordStart(line []rune, col int) int {
	if col <= 0 {
		return 0
	}

	// Skip any whitespace/punctuation to the left
	i := col - 1
	for i >= 0 && !isWordRune(line[i]) {
		i--
	}

	// Now find the start of the word
	for i >= 0 && isWordRune(line[i]) {
		i--
	}

	return i + 1
}

// findNextWordStart finds the start of the next word from the given column
func findNextWordStart(line []rune, col int) int {
	// Skip any current word characters
	i := col
	for i < len(line) && isWordRune(line[i]) {
		i++
	}

	// Skip whitespace/punctuation to find next word
	for i < len(line) && !isWordRune(line[i]) {
		i++
	}

	return i
}

// findWordEnd finds the end of the current word from the given column
func findWordEnd(line []rune, col int) int {
	// If we're on whitespace, skip to next word first
	i := col
	for i < len(line) && !isWordRune(line[i]) {
		i++
	}

	// Now find the end of the word
	for i < len(line) && isWordRune(line[i]) {
		i++
	}

	return i
}

// deletePrevWord deletes the word before the cursor and returns the new line and column
func deletePrevWord(line []rune, col int) ([]rune, int) {
	if col == 0 {
		return line, col
	}

	start := findPrevWordStart(line, col)
	newLine := append(append([]rune{}, line[:start]...), line[col:]...)
	return newLine, start
}

// deleteNextWord deletes the word after the cursor and returns the new line
func deleteNextWord(line []rune, col int) []rune {
	if col >= len(line) {
		return line
	}

	end := findWordEnd(line, col)
	newLine := append(append([]rune{}, line[:col]...), line[end:]...)
	return newLine
}

func (m inputModel) fullText() string {
	parts := make([]string, len(m.lines))
	for i, l := range m.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

func (m *inputModel) setFullText(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Split(s, "\n")
	m.lines = make([][]rune, len(parts))
	for i, p := range parts {
		m.lines[i] = []rune(p)
	}
	m.row = len(m.lines) - 1
	m.col = len(m.lines[m.row])
}

func (m inputModel) firstLine() string {
	if len(m.lines) == 0 {
		return ""
	}
	return string(m.lines[0])
}

func (m inputModel) updateSuggestions() inputModel {
	if m.suggProvider != nil {
		previous := m.suggestions
		m.suggestions, m.suggExtra = m.suggProvider(m.firstLine())
		changed := len(previous) != len(m.suggestions)
		if !changed {
			for i, s := range m.suggestions {
				if s.Name != previous[i].Name {
					changed = true
					break
				}
			}
		}
		if changed {
			m.suggSelected, m.suggOffset = 0, 0
		}
		if m.suggSelected >= len(m.suggestions) {
			m.suggSelected = len(m.suggestions) - 1
		}
		if m.suggSelected < 0 {
			m.suggSelected = 0
		}
	}
	m.adjustSuggestionOffset()
	return m
}

func (m *inputModel) adjustSuggestionOffset() {
	if len(m.suggestions) <= maxVisibleSuggestions {
		m.suggOffset = 0
		return
	}
	if m.suggSelected < m.suggOffset {
		m.suggOffset = m.suggSelected
	}
	if m.suggSelected >= m.suggOffset+maxVisibleSuggestions {
		m.suggOffset = m.suggSelected - maxVisibleSuggestions + 1
	}
	m.suggOffset = max(0, min(m.suggOffset, len(m.suggestions)-maxVisibleSuggestions))
}

func commonSuggestionPrefix(suggestions []Suggestion) string {
	if len(suggestions) == 0 {
		return ""
	}

	prefix := []rune(suggestions[0].Name)
	for _, s := range suggestions[1:] {
		name := []rune(s.Name)
		i := 0
		for i < len(prefix) && i < len(name) && prefix[i] == name[i] {
			i++
		}
		prefix = prefix[:i]
		if len(prefix) == 0 {
			break
		}
	}
	return string(prefix)
}

func (m inputModel) completeSuggestion() inputModel {
	if m.row != 0 || m.col != len(m.lines[0]) {
		return m
	}

	line := m.firstLine()
	if !strings.HasPrefix(line, "/") || strings.Contains(line, " ") {
		return m
	}

	m = m.updateSuggestions()
	if len(m.suggestions) == 0 {
		return m
	}

	completion := ""
	if len(m.suggestions) == 1 {
		completion = m.suggestions[0].Name
	} else {
		completion = commonSuggestionPrefix(m.suggestions)
		if len([]rune(completion)) <= len([]rune(line)) {
			return m
		}
	}

	m.lines[0] = []rune(completion)
	m.col = len(m.lines[0])
	return m.updateSuggestions()
}

func (m inputModel) acceptSelectedSuggestion() inputModel {
	if len(m.suggestions) == 0 || m.suggSelected < 0 || m.suggSelected >= len(m.suggestions) {
		return m
	}
	completion := m.suggestions[m.suggSelected].Name
	if strings.HasPrefix(m.firstLine(), "/skills") && completion != "/skills" {
		completion += " "
	}
	m.lines[0] = []rune(completion)
	m.row = 0
	m.col = len(m.lines[0])
	return m.updateSuggestions()
}

func (m inputModel) insertRunes(runes []rune) inputModel {
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\r' {
			// Handle \r\n as a single newline
			if i+1 < len(runes) && runes[i+1] == '\n' {
				continue
			}
			// Handle lone \r as newline
			m = m.breakLine()
			continue
		}
		if r == '\n' {
			m = m.breakLine()
			continue
		}

		line := m.lines[m.row]
		newLine := make([]rune, len(line)+1)
		copy(newLine, line[:m.col])
		newLine[m.col] = r
		copy(newLine[m.col+1:], line[m.col:])
		m.lines[m.row] = newLine
		m.col++
	}
	return m
}

func (m inputModel) breakLine() inputModel {
	cur := m.lines[m.row]
	before := append([]rune{}, cur[:m.col]...)
	after := append([]rune{}, cur[m.col:]...)
	newLines := make([][]rune, len(m.lines)+1)
	copy(newLines, m.lines[:m.row+1])
	newLines[m.row] = before
	newLines[m.row+1] = after
	copy(newLines[m.row+2:], m.lines[m.row+1:])
	m.lines = newLines
	m.row++
	m.col = 0
	return m
}

// ── tea.Model ────────────────────────────────────────────────────────────────

func (m inputModel) Init() tea.Cmd { return nil }

func (m inputModel) Submitted() bool { return m.submitted }

func (m inputModel) Value() string { return strings.TrimRight(m.value, "\n") }

func (m inputModel) HistorySnapshot() []string {
	cp := make([]string, len(m.history))
	copy(cp, m.history)
	return cp
}

func (m inputModel) SetSubmitDisabled(disabled bool) InputModel {
	m.submitDisabled = disabled
	return m
}

func (m inputModel) ResetForNext() inputModel {
	if m.submitted && strings.TrimSpace(m.value) != "" {
		m.history = append(m.history, strings.TrimRight(m.value, "\n"))
	}
	return newInputModel(m.history, m.suggProvider)
}

func (m inputModel) UpdateComponent(msg tea.Msg) (inputModel, tea.Cmd) {
	updated, cmd := m.Update(msg)
	return updated.(inputModel), cmd
}
func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width

	case InputEnterIdleMsg:
		if m.pendingEnter && msg.seq == m.enterSeq {
			m.pendingEnter = false
			return m.handleEnter()
		}
		return m, nil

	case tea.KeyMsg:
		// Windows deferred Enter: if any input arrives before the idle timer
		// fires, the Enter was not a lone submit (it was almost certainly a
		// pasted newline). Turn it into a literal newline and continue.
		if m.pendingEnter {
			m.pendingEnter = false
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeyEnter {
				m = m.breakLine()
			}
		}
		switch msg.Type {

		// Submit on Enter
		case tea.KeyEnter:
			// On Windows, bubbletea reads the console via coninput and
			// delivers a pasted newline as a discrete KeyEnter, identical to a
			// typed one (KeyMsg.Paste is always false there). Defer the
			// decision a few milliseconds: if more input (the rest of a paste)
			// arrives before the timer, the Enter becomes a newline instead of
			// submitting only the first line. See the pendingEnter handling at
			// the top of the KeyMsg case.
			if runtime.GOOS == "windows" && !msg.Paste {
				m.pendingEnter = true
				m.enterSeq++
				seq := m.enterSeq
				return m, tea.Tick(enterIdleDelay, func(time.Time) tea.Msg {
					return InputEnterIdleMsg{seq: seq}
				})
			}
			return m.handleEnter()
		case tea.KeyCtrlO:
			return m, runCtrlOHandler()
		case tea.KeySpace:
			m = m.insertRunes([]rune{' '})
			m = m.updateSuggestions()

		case tea.KeyTab:
			m = m.completeSuggestion()
		// Insert literal newline with Ctrl+J
		case tea.KeyCtrlJ:
			m = m.breakLine()

		// ── cursor movement ──────────────────────────────────────────────────
		case tea.KeyLeft:
			if m.col > 0 {
				m.col--
			} else if m.row > 0 {
				m.row--
				m.col = len(m.lines[m.row])
			}
		case tea.KeyRight:
			if m.col < len(m.lines[m.row]) {
				m.col++
			} else if m.row < len(m.lines)-1 {
				m.row++
				m.col = 0
			}

		// ── word navigation (Ctrl+Left/Right) ───────────────────────────────
		case tea.KeyCtrlLeft:
			// Try to find previous word in current line
			newCol := findPrevWordStart(m.lines[m.row], m.col)
			if newCol < m.col {
				m.col = newCol
			} else if m.row > 0 {
				// Move to previous line and find last word
				m.row--
				m.col = len(m.lines[m.row])
				m.col = findPrevWordStart(m.lines[m.row], m.col)
			}

		case tea.KeyCtrlRight:
			// Try to find next word in current line
			newCol := findNextWordStart(m.lines[m.row], m.col)
			if newCol > m.col {
				m.col = newCol
			} else if m.row < len(m.lines)-1 {
				// Move to next line and find first word
				m.row++
				m.col = 0
				m.col = findNextWordStart(m.lines[m.row], m.col)
			}

		case tea.KeyUp:
			if len(m.suggestions) > 0 {
				if m.suggSelected > 0 {
					m.suggSelected--
				} else {
					m.suggSelected = len(m.suggestions) - 1
				}
			} else if m.row > 0 {
				// move up within multi-line buffer
				m.row--
				if m.col > len(m.lines[m.row]) {
					m.col = len(m.lines[m.row])
				}
			} else {
				// history: go back
				if m.histIdx > 0 {
					if m.histIdx == len(m.history) {
						m.liveBuf = m.fullText()
					}
					m.histIdx--
					m.setFullText(m.history[m.histIdx])
					// ensure trailing space to prevent suggestion blocking
					if strings.HasPrefix(m.firstLine(), "/") && !strings.HasSuffix(m.firstLine(), " ") {
						m.lines[0] = append(m.lines[0], ' ')
						m.col = len(m.lines[0])
					}
					m = m.updateSuggestions()
				}
			}

		case tea.KeyDown:
			if len(m.suggestions) > 0 {
				m.suggSelected = (m.suggSelected + 1) % len(m.suggestions)
			} else if m.row < len(m.lines)-1 {
				// move down within multi-line buffer
				m.row++
				if m.col > len(m.lines[m.row]) {
					m.col = len(m.lines[m.row])
				}
			} else {
				// history: go forward
				if m.histIdx < len(m.history) {
					m.histIdx++
					if m.histIdx == len(m.history) {
						m.setFullText(m.liveBuf)
					} else {
						m.setFullText(m.history[m.histIdx])
						// ensure trailing space to prevent suggestion blocking
						if strings.HasPrefix(m.firstLine(), "/") && !strings.HasSuffix(m.firstLine(), " ") {
							m.lines[0] = append(m.lines[0], ' ')
							m.col = len(m.lines[0])
						}
					}
					m = m.updateSuggestions()
				}
			}

		case tea.KeyHome, tea.KeyCtrlA:
			m.col = 0
		case tea.KeyEnd, tea.KeyCtrlE:
			m.col = len(m.lines[m.row])

		// ── deletion (with Alt modifier support for word deletion) ─────────
		case tea.KeyBackspace:
			// Check if Alt modifier is pressed (Alt+Backspace = delete previous word)
			if msg.Alt && m.col > 0 {
				newLine, newCol := deletePrevWord(m.lines[m.row], m.col)
				m.lines[m.row] = newLine
				m.col = newCol
			} else {
				if m.col > 0 {
					m.col--
					line := m.lines[m.row]
					m.lines[m.row] = append(line[:m.col:m.col], line[m.col+1:]...)
				} else if m.row > 0 {
					prevLen := len(m.lines[m.row-1])
					m.lines[m.row-1] = append(m.lines[m.row-1], m.lines[m.row]...)
					m.lines = append(m.lines[:m.row], m.lines[m.row+1:]...)
					m.row--
					m.col = prevLen
				}
			}
			m = m.updateSuggestions()

		case tea.KeyDelete:
			// Check if Alt modifier is pressed (Alt+Delete = delete next word)
			if msg.Alt {
				if m.col < len(m.lines[m.row]) {
					m.lines[m.row] = deleteNextWord(m.lines[m.row], m.col)
				} else if m.row < len(m.lines)-1 {
					// If at end of line, merge with next line
					m.lines[m.row] = append(m.lines[m.row], m.lines[m.row+1]...)
					m.lines = append(m.lines[:m.row+1], m.lines[m.row+2:]...)
				}
			} else {
				if m.col < len(m.lines[m.row]) {
					line := m.lines[m.row]
					m.lines[m.row] = append(line[:m.col:m.col], line[m.col+1:]...)
				} else if m.row < len(m.lines)-1 {
					m.lines[m.row] = append(m.lines[m.row], m.lines[m.row+1]...)
					m.lines = append(m.lines[:m.row+1], m.lines[m.row+2:]...)
				}
			}
			m = m.updateSuggestions()

		// ── kill helpers ────────────────────────────────────────────────────
		case tea.KeyCtrlK:
			m.lines[m.row] = m.lines[m.row][:m.col]
			m = m.updateSuggestions()

		case tea.KeyCtrlU:
			m.lines = [][]rune{{}}
			m.row, m.col = 0, 0
			m = m.updateSuggestions()

		// ── quit without submit ─────────────────────────────────────────────
		case tea.KeyCtrlC:
			m.submitted = false
			m.value = ""
			return m, func() tea.Msg { return InputCancelMsg{} }

		// ── rune input (normal typing + paste) ─────────────────────────────
		default:
			if msg.Type == tea.KeyRunes {
				m = m.insertRunes(msg.Runes)
				m = m.updateSuggestions()
			}
		}
	}

	m.adjustSuggestionOffset()
	return m, nil
}

// handleEnter runs the normal Enter behaviour: submit, accept a suggestion, or
// (for /skills) start / continue a multi-line prompt. Extracted so the Windows
// deferred-Enter path can invoke it after the idle window.
func (m inputModel) handleEnter() (inputModel, tea.Cmd) {
	// For /skills: multi-line prompt with ability to submit
	if strings.HasPrefix(m.firstLine(), "/skills") {
		if len(m.suggestions) > 0 {
			return m.acceptSelectedSuggestion(), nil
		}
		// Check if a prompt is already inline after the skill name
		rest := strings.TrimSpace(strings.TrimPrefix(m.firstLine(), "/skills"))
		hasInlinePrompt := len(strings.Fields(rest)) > 1
		// Submit if: inline prompt exists, or user finished multi-line input (empty line after content)
		if hasInlinePrompt || (m.row > 0 && len(m.lines[m.row]) == 0) {
			if m.submitDisabled {
				return m, nil
			}
			m.submitted = true
			m.value = m.fullText()
			return m, func() tea.Msg { return InputSubmitMsg{Value: strings.TrimRight(m.value, "\n")} }
		}
		// No inline prompt yet: break line for multi-line typing
		m = m.breakLine()
		m = m.updateSuggestions()
		return m, nil
	}
	// For other commands: if input already matches the selected suggestion, submit
	if len(m.suggestions) > 0 {
		if m.firstLine() == m.suggestions[m.suggSelected].Name {
			if m.submitDisabled {
				return m, nil
			}
			m.submitted = true
			m.value = m.fullText()
			return m, func() tea.Msg { return InputSubmitMsg{Value: strings.TrimRight(m.value, "\n")} }
		}
		return m.acceptSelectedSuggestion(), nil
	}
	text := strings.TrimSpace(m.fullText())
	if text == "" {
		return m, nil
	}
	if m.submitDisabled {
		return m, nil
	}
	m.submitted = true
	m.value = m.fullText()
	return m, func() tea.Msg { return InputSubmitMsg{Value: strings.TrimRight(m.value, "\n")} }
}

// wrapLine wraps a line of runes into multiple visual lines based on maxWidth.
// Hard wrap only (no word-break): layout is stable character-by-character.
// isCursorLine must be true when this is the line the cursor sits on, so that
// a virtual empty row is added only when the cursor lands at a full-width boundary.
func wrapLine(line []rune, cursorCol int, maxWidth int, isCursorLine bool) ([]string, int, int) {
	if maxWidth <= 0 {
		maxWidth = 1
	}

	// Handle empty line
	if len(line) == 0 {
		return []string{""}, 0, 0
	}

	var wrappedLines []string
	for i := 0; i < len(line); i += maxWidth {
		end := i + maxWidth
		if end > len(line) {
			end = len(line)
		}
		wrappedLines = append(wrappedLines, string(line[i:end]))
	}

	// Virtual empty row only for the cursor line, only at an exact boundary.
	if isCursorLine && len(line)%maxWidth == 0 && cursorCol == len(line) {
		wrappedLines = append(wrappedLines, "")
	}

	visualRow := cursorCol / maxWidth
	visualCol := cursorCol % maxWidth

	return wrappedLines, visualRow, visualCol
}

// detectGitBranch reads .git/HEAD and returns the current branch name.
// Returns empty string if not in a git repo or on error.
func detectGitBranch() string {
	data, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return ""
	}
	data = bytes.TrimSpace(data)
	// Format: "ref: refs/heads/branchname"
	prefix := []byte("ref: refs/heads/")
	if !bytes.HasPrefix(data, prefix) {
		return ""
	}
	branch := string(data[len(prefix):])
	return branch
}

func (m inputModel) promptPrefix() string {
	if m.gitBranch != "" {
		return fmt.Sprintf("(%s) ❯ ", m.gitBranch)
	}
	return "❯ "
}

func RenderSubmittedPrompt(input string) string {
	prefix := "❯ "
	promptWidth := len([]rune(prefix))
	lines := strings.Split(strings.TrimRight(input, "\n"), "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}

	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(inputPromptStyle.Render(prefix))
		} else {
			b.WriteString(strings.Repeat(" ", promptWidth))
		}
		b.WriteString(inputTextStyle.Render(line))
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m inputModel) View() string {
	var b strings.Builder

	// Build prompt prefix: show git branch if available
	promptPrefix := m.promptPrefix()

	// Calculate available width for text (subtract prompt width)
	promptWidth := len([]rune(promptPrefix))
	availableWidth := m.width - promptWidth
	if availableWidth < 1 {
		availableWidth = 1
	}

	for r, line := range m.lines {
		// Wrap the line if needed
		wrappedLines, cursorVisualRow, cursorVisualCol := wrapLine(line, m.col, availableWidth, r == m.row)

		for wr, wrappedLine := range wrappedLines {
			// Only show prompt on the first visual line of each logical line
			if r == 0 && wr == 0 {
				b.WriteString(inputPromptStyle.Render(promptPrefix))
			} else {
				b.WriteString(strings.Repeat(" ", promptWidth)) // continuation lines get two spaces for alignment
			}

			// Draw cursor only on the correct visual row and if not submitted
			isCursorRow := (r == m.row) && (wr == cursorVisualRow) && !m.submitted

			if isCursorRow {
				before := ""
				var cur, after string

				if cursorVisualCol < len([]rune(wrappedLine)) {
					runes := []rune(wrappedLine)
					before = string(runes[:cursorVisualCol])
					cur = string(runes[cursorVisualCol])
					after = string(runes[cursorVisualCol+1:])
				} else {
					before = wrappedLine
					cur = " "
					after = ""
				}

				b.WriteString(inputTextStyle.Render(before))
				b.WriteString(inputCursorStyle.Render(cur))
				b.WriteString(inputTextStyle.Render(after))
			} else {
				b.WriteString(inputTextStyle.Render(wrappedLine))
			}
			b.WriteString("\n")
		}
	}

	// Show suggestions in a compact, aligned block.
	if len(m.suggestions) > 0 && !m.submitted {
		header := "╭─ suggestions"
		end := min(m.suggOffset+maxVisibleSuggestions, len(m.suggestions))
		if len(m.suggestions) > maxVisibleSuggestions {
			header += fmt.Sprintf(" · ↑ %d hidden · ↓ %d hidden", m.suggOffset, len(m.suggestions)-end)
		}
		b.WriteString(DimStyle.Render(header) + "\n")
		for i := m.suggOffset; i < end; i++ {
			s := m.suggestions[i]
			name := OkStyle.Render(s.Name)
			if i == m.suggSelected {
				name = SelectedBg.Render(s.Name)
			}
			b.WriteString(fmt.Sprintf("%s %s  %s\n",
				DimStyle.Render("│"),
				name,
				InfoStyle.Render(s.Description),
			))
		}
		if m.suggExtra != "" {
			b.WriteString(fmt.Sprintf("%s %s\n", DimStyle.Render("│"), TipStyle.Render(m.suggExtra)))
		}
		b.WriteString(DimStyle.Render("╰─ ↑/↓ navigate · enter to select · tab to complete") + "\n")
	}

	return b.String()
}

// ── InputArea — public API ────────────────────────────────────────────────────

// InputArea is a stateful, reusable input widget that replaces readline in the
// main chat loop. Supports:
//   - Enter to submit
//   - Ctrl+J for a literal newline (so pasted multi-line text works natively)
//   - ↑/↓ for history navigation (when on the first/last buffer line)
//   - Ctrl+A/E (line start/end), Ctrl+K (kill to EOL), Ctrl+U (clear all)
//   - Left/Right wrap across lines
//   - Autocomplete suggestions via a SuggestionProvider callback
type InputArea struct {
	history      []string
	suggProvider SuggestionProvider
}

// NewInputArea creates an InputArea.
//
//	history:  previous inputs (oldest first); pass nil for empty history.
//	sp:       optional SuggestionProvider for command autocomplete.
func NewInputArea(history []string, sp SuggestionProvider) *InputArea {
	h := make([]string, len(history))
	copy(h, history)
	return &InputArea{history: h, suggProvider: sp}
}

// ReadLine runs the interactive prompt and returns the submitted text.
// Returns ("", false) on Ctrl+C or I/O error.
func (a *InputArea) ReadLine() (string, bool) {
	m := newInputModel(a.history, a.suggProvider)
	prog := tea.NewProgram(m)
	result, err := prog.Run()
	if err != nil {
		return "", false
	}
	final := result.(inputModel)
	if !final.submitted {
		return "", false
	}
	text := strings.TrimRight(final.value, "\n")
	if text != "" {
		a.history = append(a.history, text)
	}
	return text, true
}

// History returns a copy of the accumulated history slice.
func (a *InputArea) History() []string {
	cp := make([]string, len(a.history))
	copy(cp, a.history)
	return cp
}
