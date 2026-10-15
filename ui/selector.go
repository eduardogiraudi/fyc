package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// SelectorCanceled is returned by the SelectOption* helpers (and by
// MultiSelectOption, as a nil slice) when the user dismisses the prompt —
// q, Ctrl+C or Esc — instead of choosing a row. It is a negative index, so it
// can never collide with a real option index; callers MUST treat it as
// "cancelled" and never index into the options with it.
const SelectorCanceled = -1

type SelectorModel struct {
	Title             string
	StyledTitle       string // Title already formatted with styles
	Options           []string
	Cursor            int
	Choice            int
	Offset            int    // Offset for scrolling
	InputMode         bool   // If true, we are in text input mode
	InputBuffer       string // Text typed by the user
	Suggestion        string // Suggestion text (if deny with suggestion)
	inputCol          int    // Cursor position in the input buffer (in runes)
	SuggestIdx        int    // Index of the "Deny with suggestion" option (-1 = none)
	quitting          bool
	width             int // Terminal width
	AutoAcceptOnCtrlO bool
	MaxVisible        int // Visible lines before scrolling (0 = default 15)
	PinnedTop         int // First N options always visible, outside the scroll
}

func (m SelectorModel) Init() tea.Cmd { return nil }

func (m SelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlO {
			cmd := runCtrlOHandler()
			if m.AutoAcceptOnCtrlO {
				m.Choice = 0
				m.quitting = true
				return m, tea.Batch(cmd, tea.Quit)
			}
			return m, cmd
		}
		// If we are in input mode, handle typing
		if m.InputMode {
			switch msg.String() {
			case "enter":
				m.Suggestion = strings.TrimSpace(m.InputBuffer)
				m.Choice = m.Cursor
				m.quitting = true
				return m, tea.Quit
			case "ctrl+c", "esc":
				// Go back to selection
				m.InputMode = false
				m.InputBuffer = ""
				m.inputCol = 0
				m.Suggestion = ""
				return m, nil
			case "left":
				if m.inputCol > 0 {
					m.inputCol--
				}
				return m, nil
			case "right":
				buf := []rune(m.InputBuffer)
				if m.inputCol < len(buf) {
					m.inputCol++
				}
				return m, nil
			case "home":
				m.inputCol = 0
				return m, nil
			case "end":
				m.inputCol = len([]rune(m.InputBuffer))
				return m, nil
			case "ctrl+left":
				buf := []rune(m.InputBuffer)
				m.inputCol = findPrevWordStart(buf, m.inputCol)
				return m, nil
			case "ctrl+right":
				buf := []rune(m.InputBuffer)
				m.inputCol = findNextWordStart(buf, m.inputCol)
				return m, nil
			case "ctrl+a":
				m.inputCol = 0
				return m, nil
			case "ctrl+e":
				m.inputCol = len([]rune(m.InputBuffer))
				return m, nil
			case "backspace":
				if len(m.InputBuffer) > 0 {
					runes := []rune(m.InputBuffer)
					if m.inputCol > 0 {
						m.InputBuffer = string(append(runes[:m.inputCol-1], runes[m.inputCol:]...))
						m.inputCol--
					}
				}
				return m, nil
			case "alt+backspace":
				buf := []rune(m.InputBuffer)
				if m.inputCol > 0 {
					start := findPrevWordStart(buf, m.inputCol)
					m.InputBuffer = string(append(buf[:start], buf[m.inputCol:]...))
					m.inputCol = start
				}
				return m, nil
			case "delete":
				runes := []rune(m.InputBuffer)
				if m.inputCol < len(runes) {
					m.InputBuffer = string(append(runes[:m.inputCol], runes[m.inputCol+1:]...))
				}
				return m, nil
			case "alt+delete":
				runes := []rune(m.InputBuffer)
				if m.inputCol < len(runes) {
					end := findWordEnd(runes, m.inputCol)
					m.InputBuffer = string(append(runes[:m.inputCol], runes[end:]...))
				}
				return m, nil
			case "ctrl+k":
				runes := []rune(m.InputBuffer)
				m.InputBuffer = string(runes[:m.inputCol])
				return m, nil
			case "ctrl+u":
				m.InputBuffer = ""
				m.inputCol = 0
			default:
				if msg.Type == tea.KeyRunes {
					runes := []rune(m.InputBuffer)
					var newRunes []rune
					newRunes = append(newRunes, runes[:m.inputCol]...)
					newRunes = append(newRunes, msg.Runes...)
					newRunes = append(newRunes, runes[m.inputCol:]...)
					m.InputBuffer = string(newRunes)
					m.inputCol += len(msg.Runes)
				} else if msg.Type == tea.KeySpace {
					runes := []rune(m.InputBuffer)
					var newRunes []rune
					newRunes = append(newRunes, runes[:m.inputCol]...)
					newRunes = append(newRunes, ' ')
					newRunes = append(newRunes, runes[m.inputCol:]...)
					m.InputBuffer = string(newRunes)
					m.inputCol++
				}
				return m, nil
			}
		}

		switch msg.String() {
		case "ctrl+c", "q", "esc":
			// Dismissal, not a selection: leave a negative sentinel so the
			// caller can tell "quit" apart from "picked the first row".
			m.Choice = SelectorCanceled
			m.quitting = true
			return m, tea.Quit
		case "up":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down":
			if m.Cursor < len(m.Options)-1 {
				m.Cursor++
			}
		case "enter":
			// If the current option is "Deny with suggestion", enter input mode
			if m.SuggestIdx != -1 && m.Cursor == m.SuggestIdx {
				m.InputMode = true
				m.InputBuffer = ""
				return m, nil
			}
			m.Choice = m.Cursor
			m.quitting = true
			return m, tea.Quit
		}
	}

	// Adjust offset to keep cursor visible
	m.adjustOffset()

	return m, nil
}

// visibleCount returns how many options are shown at once before scrolling.
// It defaults to 15 when MaxVisible is unset (<= 0).
func (m SelectorModel) visibleCount() int {
	if m.MaxVisible > 0 {
		return m.MaxVisible
	}
	return 15
}

func (m *SelectorModel) adjustOffset() {
	maxVisible := m.visibleCount()
	pinned := m.PinnedTop
	if pinned < 0 {
		pinned = 0
	}
	if pinned > len(m.Options) {
		pinned = len(m.Options)
	}

	// The scroll window only spans the options after the pinned block.
	scrollLen := len(m.Options) - pinned
	if scrollLen <= maxVisible {
		m.Offset = 0
		return
	}

	rel := m.Cursor - pinned
	if rel < 0 {
		rel = 0 // cursor on a pinned row: no scrolling needed
	}
	if rel >= m.Offset+maxVisible {
		m.Offset = rel - maxVisible + 1
	}
	if rel < m.Offset {
		m.Offset = rel
	}
	if maxOff := scrollLen - maxVisible; m.Offset > maxOff {
		m.Offset = maxOff
	}
	if m.Offset < 0 {
		m.Offset = 0
	}
}

// optionLine renders one option row, applying the selection highlight.
func (m SelectorModel) optionLine(i int) string {
	opt := m.Options[i]
	if strings.HasPrefix(opt, "---") || strings.HasPrefix(opt, "───") {
		return "  " + DimStyle.Render(strings.Repeat("─", 28))
	}
	radio := RadioOff.String()
	if m.Cursor == i {
		radio = RadioOn.String()
	}
	line := fmt.Sprintf(" %s %s", radio, opt)
	if m.Cursor == i {
		return SelectedBg.Render(line)
	}
	return "  " + opt
}

func (m SelectorModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder

	// ── Title ──────────────────────────────────────────────────────────────
	if m.StyledTitle != "" {
		b.WriteString(m.StyledTitle + "\n")
	} else {
		b.WriteString(SelectorHeader.Render(m.Title) + "\n")
	}

	// ── Visible range ───────────────────────────────────────────────────────
	// The first PinnedTop options stay above the scroll window and are always
	// visible; Offset is relative to the scrollable region that follows them.
	pinned := m.PinnedTop
	if pinned < 0 {
		pinned = 0
	}
	if pinned > len(m.Options) {
		pinned = len(m.Options)
	}
	maxVisible := m.visibleCount()
	scrollLen := len(m.Options) - pinned
	start := m.Offset
	if start > scrollLen {
		start = scrollLen
	}
	first := pinned + start
	end := first + maxVisible
	if end > len(m.Options) {
		end = len(m.Options)
	}
	scrollable := scrollLen > maxVisible

	// ── Build content ───────────────────────────────────────────────────────
	var contentLines []string

	for i := 0; i < pinned; i++ {
		contentLines = append(contentLines, m.optionLine(i))
	}

	// Reserve one row per scroll arrow so they never eat an option row: the
	// row stays blank when it has nothing to show, keeping the height stable.
	if scrollable {
		if start > 0 {
			contentLines = append(contentLines, DimStyle.Render("  ▲ "))
		} else {
			contentLines = append(contentLines, " ")
		}
	}

	for i := first; i < end; i++ {
		contentLines = append(contentLines, m.optionLine(i))
	}

	if scrollable {
		if end < len(m.Options) {
			contentLines = append(contentLines, DimStyle.Render("  ▼ "))
		} else {
			contentLines = append(contentLines, " ")
		}
		contentLines = append(contentLines, HintStyle.Render(fmt.Sprintf("  [%d/%d]", m.Cursor+1, len(m.Options))))
	}

	// ── Assemble ────────────────────────────────────────────────────────────
	allContent := b.String()
	if len(contentLines) > 0 {
		allContent += strings.Join(contentLines, "\n")
	}
	allContent += "\n" + HintStyle.Render("  ▲/▼ navigate  ·  ↵ confirm  ·  q/esc quit")

	boxed := SelectorBorder.Render(allContent)

	// ── Suggestion input mode ──────────────────────────────────────────────
	if m.InputMode {
		availWidth := m.width - 14
		if availWidth < 10 {
			availWidth = 10
		}

		var inputBuf strings.Builder
		inputBuf.WriteString("\n")
		inputBuf.WriteString(TitleStyle.Render("Suggestion:"))
		inputBuf.WriteString(" ")

		buf := []rune(m.InputBuffer)
		wrappedLines, visualRow, visualCol := wrapLine(buf, m.inputCol, availWidth, true)

		for wr, wl := range wrappedLines {
			if wr > 0 {
				inputBuf.WriteString("            ")
			}

			if wr == visualRow {
				before := ""
				var cur, after string
				wlRunes := []rune(wl)
				if visualCol < len(wlRunes) {
					before = string(wlRunes[:visualCol])
					cur = string(wlRunes[visualCol])
					after = string(wlRunes[visualCol+1:])
				} else {
					before = wl
					cur = " "
					after = ""
				}
				inputBuf.WriteString(inputTextStyle.Render(before))
				inputBuf.WriteString(inputCursorStyle.Render(cur))
				inputBuf.WriteString(inputTextStyle.Render(after))
			} else {
				inputBuf.WriteString(inputTextStyle.Render(wl))
			}
			inputBuf.WriteString("\n")
		}

		inputBuf.WriteString("\n" + InfoStyle.Render("Esc to cancel, Enter to confirm") + "\n")
		boxed += inputBuf.String()
	}

	// Clamp every row to the terminal width: an over-wide line (a long command,
	// a long option) soft-wraps, the renderer's line count stops matching the
	// screen and the whole frame shifts. The full content stays reachable through
	// the "See details" / "See changes" viewers.
	w := m.width
	if w <= 0 {
		w = terminalWidth()
	}
	return ClampBlock(boxed, w)
}

func SelectOption(title string, options []string) int {
	return withExternalPrompt(func() int {
		p := tea.NewProgram(SelectorModel{
			Title:      title,
			Options:    options,
			SuggestIdx: -1,
		})

		m, _ := p.Run()
		return m.(SelectorModel).Choice
	})
}

// SelectOptionWindow is SelectOption with an explicit window height: only
// maxVisible rows are shown before the list starts scrolling. The first
// pinnedTop options stay visible above the scroll window (e.g. a fixed "back"
// row that never scrolls away).
func SelectOptionWindow(title string, options []string, maxVisible, pinnedTop int) int {
	return withExternalPrompt(func() int {
		p := tea.NewProgram(SelectorModel{
			Title:      title,
			Options:    options,
			SuggestIdx: -1,
			MaxVisible: maxVisible,
			PinnedTop:  pinnedTop,
		})

		m, _ := p.Run()
		return m.(SelectorModel).Choice
	})
}

// SelectOptionWithStyledTitle allows passing a title already formatted with lipgloss styles
func SelectOptionWithStyledTitle(styledTitle string, options []string) int {
	return withExternalPrompt(func() int {
		p := tea.NewProgram(SelectorModel{
			StyledTitle: styledTitle,
			Options:     options,
			SuggestIdx:  -1,
		})

		m, _ := p.Run()
		return m.(SelectorModel).Choice
	})
}

// SelectOptionWithSuggestion shows a selector with inline suggestion input.
// If the user selects the option at denyIdx, an inline text input appears.
// Returns (choiceIndex, suggestionText).
func SelectOptionWithSuggestion(title string, options []string, denyIdx int) (int, string) {
	res := withExternalPrompt(func() struct {
		choice     int
		suggestion string
	} {
		p := tea.NewProgram(SelectorModel{
			Title:             title,
			Options:           options,
			width:             terminalWidth(),
			SuggestIdx:        denyIdx,
			AutoAcceptOnCtrlO: true,
		})
		m, _ := p.Run()
		sm := m.(SelectorModel)
		return struct {
			choice     int
			suggestion string
		}{sm.Choice, sm.Suggestion}
	})
	return res.choice, res.suggestion
}

// SelectOptionWithStyledTitleSuggestion is like SelectOptionWithSuggestion but with a pre-styled title.
func SelectOptionWithStyledTitleSuggestion(styledTitle string, options []string, denyIdx int) (int, string) {
	res := withExternalPrompt(func() struct {
		choice     int
		suggestion string
	} {
		p := tea.NewProgram(SelectorModel{
			StyledTitle:       styledTitle,
			Options:           options,
			width:             terminalWidth(),
			SuggestIdx:        denyIdx,
			AutoAcceptOnCtrlO: true,
		})
		m, _ := p.Run()
		sm := m.(SelectorModel)
		return struct {
			choice     int
			suggestion string
		}{sm.Choice, sm.Suggestion}
	})
	return res.choice, res.suggestion
}

// terminalWidth returns the current terminal width, or 80 if unknown.
func terminalWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// InputText prompts the user for a single line of text using a plain bufio reader.
func InputText(prompt string) string {
	return withExternalPrompt(func() string {
		fmt.Println(TitleStyle.Render(prompt))
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			return strings.TrimSpace(scanner.Text())
		}
		return ""
	})
}

// InputPassword prompts the user for a password without echoing characters.
func InputPassword(prompt string) string {
	return withExternalPrompt(func() string {
		fmt.Print(TitleStyle.Render(prompt) + " ")
		password, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println() // newline after hidden input
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(password))
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// MultiSelectModel — checklist with toggle for each option + Done button.
// ─────────────────────────────────────────────────────────────────────────────

type MultiSelectModel struct {
	Title       string
	StyledTitle string
	Options     []string
	States      []bool
	Cursor      int
	Offset      int
	// RequireSelection blocks the trailing action row while nothing is toggled,
	// so a destructive bulk action (e.g. deleting sessions) can never confirm an
	// empty selection.
	RequireSelection bool
	quitting         bool
}

func (m MultiSelectModel) Init() tea.Cmd { return nil }

func (m MultiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlO {
			return m, runCtrlOHandler()
		}
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			// Cancelled: discard the toggles (nil states) so MultiSelectOption
			// can tell this apart from confirming with "Done".
			m.States = nil
			m.quitting = true
			return m, tea.Quit
		case "up":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down":
			if m.Cursor < m.maxCursor() {
				m.Cursor++
			}
		case "enter":
			if m.Cursor == len(m.Options)-1 {
				// Trailing action row ("Done" / "Delete"): when a selection is
				// required, refuse to confirm while no row is toggled.
				if m.RequireSelection && !m.hasSelection() {
					return m, nil
				}
				m.quitting = true
				return m, tea.Quit
			}
			m.States[m.Cursor] = !m.States[m.Cursor]
		}
	}

	// Adjust offset to keep cursor visible
	if len(m.Options) > 15 {
		if m.Cursor >= m.Offset+15 {
			m.Offset = m.Cursor - 15 + 1
		}
		if m.Cursor < m.Offset {
			m.Offset = m.Cursor
		}
	} else {
		m.Offset = 0
	}

	return m, nil
}

// hasSelection reports whether any toggle row is checked. The trailing row is
// the action (never a toggle), so it is excluded.
func (m MultiSelectModel) hasSelection() bool {
	n := len(m.States)
	if n > len(m.Options) {
		n = len(m.Options)
	}
	if n > 0 {
		n--
	}
	for i := 0; i < n; i++ {
		if m.States[i] {
			return true
		}
	}
	return false
}

// maxCursor returns the last row the cursor may occupy. With RequireSelection
// the trailing action row stays unreachable until at least one row is toggled,
// so the user never lands on an action that would do nothing.
func (m MultiSelectModel) maxCursor() int {
	last := len(m.Options) - 1
	if last <= 0 {
		return 0
	}
	if m.RequireSelection && !m.hasSelection() {
		return last - 1
	}
	return last
}

func (m MultiSelectModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder

	if m.StyledTitle != "" {
		b.WriteString(m.StyledTitle + "\n")
	} else {
		b.WriteString(SelectorHeader.Render(m.Title) + "\n")
	}

	start := m.Offset
	maxVisible := 15
	end := start + maxVisible
	if end > len(m.Options) {
		end = len(m.Options)
	}

	var contentLines []string

	if start > 0 {
		contentLines = append(contentLines, DimStyle.Render("  ▲ "))
	}

	for i := start; i < end; i++ {
		opt := m.Options[i]
		isLast := i == len(m.Options)-1

		if isLast {
			// Action row ("Done" / "Delete"): text only, no checkbox. Dim it
			// while a required selection is still missing so the blocked state
			// is visible.
			disabled := m.RequireSelection && !m.hasSelection()
			label := opt
			if disabled {
				label = DimStyle.Render(opt)
			}
			line := fmt.Sprintf("   %s", label)
			isSelected := m.Cursor == i
			if isSelected && !disabled {
				contentLines = append(contentLines, SelectedBg.Render(line))
			} else {
				contentLines = append(contentLines, "  "+line)
			}
			continue
		}

		checkbox := "[ ]"
		if m.States[i] {
			checkbox = "[✓]"
		}
		line := fmt.Sprintf(" %s %s", checkbox, opt)
		isSelected := m.Cursor == i
		if isSelected {
			contentLines = append(contentLines, SelectedBg.Render(line))
		} else {
			contentLines = append(contentLines, "  "+line)
		}
	}

	if end < len(m.Options) {
		contentLines = append(contentLines, DimStyle.Render("  ▼ "))
	}

	if len(m.Options) > maxVisible {
		contentLines = append(contentLines, HintStyle.Render(fmt.Sprintf("  [%d/%d]", m.Cursor+1, len(m.Options))))
	}

	allContent := b.String()
	if len(contentLines) > 0 {
		allContent += strings.Join(contentLines, "\n")
	}
	allContent += "\n" + HintStyle.Render("  ▲/▼ navigate  ·  ↵ toggle/confirm  ·  q/esc cancel")

	boxed := SelectorBorder.Render(allContent)
	return ClampBlock(boxed, terminalWidth())
}

// MultiSelectOption shows a checklist where each option can be toggled on/off
// with Enter. The last option is the action row — it must be added from the
// outside. Returns the updated states, or nil when the user cancelled.
func MultiSelectOption(title string, options []string, states []bool) []bool {
	return multiSelectRun(title, options, states, false)
}

// MultiSelectOptionWithAction is MultiSelectOption for an action that must be
// armed by a selection: when requireSelection is set the action row cannot be
// confirmed while no row is toggled, so a bulk action (e.g. deleting sessions)
// can never run on an empty selection.
func MultiSelectOptionWithAction(title string, options []string, states []bool, requireSelection bool) []bool {
	return multiSelectRun(title, options, states, requireSelection)
}

func multiSelectRun(title string, options []string, states []bool, requireSelection bool) []bool {
	return withExternalPrompt(func() []bool {
		p := tea.NewProgram(MultiSelectModel{
			Title:            title,
			Options:          options,
			States:           states,
			RequireSelection: requireSelection,
		})
		m, _ := p.Run()
		return m.(MultiSelectModel).States
	})
}
