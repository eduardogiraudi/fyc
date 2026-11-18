package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func (m *inputModel) findHistoryMatch(start int) {
	m.searchIdx = -1
	query := strings.ToLower(string(m.searchQuery))
	for i := start; i >= 0; i-- {
		if strings.Contains(strings.ToLower(m.history[i]), query) {
			m.searchIdx = i
			return
		}
	}
}

func (m inputModel) updateHistorySearch(msg tea.KeyMsg) (inputModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlR, tea.KeyDown:
		previous := m.searchIdx
		if previous >= 0 {
			m.findHistoryMatch(previous - 1)
			if m.searchIdx < 0 {
				m.searchIdx = previous
			}
		}
	case tea.KeyUp:
		query := strings.ToLower(string(m.searchQuery))
		for i := m.searchIdx + 1; i < len(m.history); i++ {
			if strings.Contains(strings.ToLower(m.history[i]), query) {
				m.searchIdx = i
				break
			}
		}
	case tea.KeyLeft:
		m.searchCol = max(0, m.searchCol-1)
	case tea.KeyRight:
		m.searchCol = min(len(m.searchQuery), m.searchCol+1)
	case tea.KeyHome, tea.KeyCtrlA:
		m.searchCol = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		m.searchCol = len(m.searchQuery)
	case tea.KeyRunes, tea.KeySpace:
		text := string(msg.Runes)
		if msg.Type == tea.KeySpace {
			text = " "
		}
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
		runes := []rune(text)
		query := append([]rune(nil), m.searchQuery[:m.searchCol]...)
		query = append(query, runes...)
		m.searchQuery = append(query, m.searchQuery[m.searchCol:]...)
		m.searchCol += len(runes)
		m.findHistoryMatch(len(m.history) - 1)
	case tea.KeyBackspace:
		if m.searchCol > 0 {
			query := append([]rune(nil), m.searchQuery[:m.searchCol-1]...)
			m.searchQuery = append(query, m.searchQuery[m.searchCol:]...)
			m.searchCol--
			m.findHistoryMatch(len(m.history) - 1)
		}
	case tea.KeyDelete:
		if m.searchCol < len(m.searchQuery) {
			query := append([]rune(nil), m.searchQuery[:m.searchCol]...)
			m.searchQuery = append(query, m.searchQuery[m.searchCol+1:]...)
			m.findHistoryMatch(len(m.history) - 1)
		}
	case tea.KeyCtrlU:
		m.searchQuery = nil
		m.searchCol = 0
		m.findHistoryMatch(len(m.history) - 1)
	case tea.KeyEsc, tea.KeyCtrlC:
		m.searchActive = false
	case tea.KeyEnter, tea.KeyTab:
		if m.searchIdx >= 0 {
			if m.histIdx == len(m.history) {
				m.liveBuf = m.fullText()
			}
			m.histIdx = m.searchIdx
			m.setFullText(m.history[m.searchIdx])
		}
		m.searchActive = false
		m = m.updateSuggestions()
	case tea.KeyCtrlO:
		return m, runCtrlOHandler()
	}
	return m, nil
}

func (m inputModel) historySearchView() string {
	width := max(1, m.width)
	query := strings.ToLower(string(m.searchQuery))
	var matches []int
	selected := 0
	for i := len(m.history) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(m.history[i]), query) {
			if i == m.searchIdx {
				selected = len(matches)
			}
			matches = append(matches, i)
		}
	}
	var b strings.Builder
	prefix := ansi.Truncate("Search history ❯ ", max(0, width-2), "")
	budget := max(0, width-ansi.StringWidth(prefix)-1)
	before := string(m.searchQuery[:m.searchCol])
	before = ansi.Cut(before, max(0, ansi.StringWidth(before)-budget), ansi.StringWidth(before))
	cur := " "
	after := ""
	if m.searchCol < len(m.searchQuery) {
		cur = string(m.searchQuery[m.searchCol])
		after = string(m.searchQuery[m.searchCol+1:])
	}
	if ansi.StringWidth(cur) > width-ansi.StringWidth(prefix)-ansi.StringWidth(before) {
		cur = " "
	}
	after = ansi.Truncate(after, max(0, width-ansi.StringWidth(prefix)-ansi.StringWidth(before)-ansi.StringWidth(cur)), "")
	b.WriteString(inputPromptStyle.Render(prefix) + inputTextStyle.Render(before) + inputCursorStyle.Render(cur) + inputTextStyle.Render(after) + "\n")
	// Limit the results before the chat clamps its live area from the bottom.
	if m.height == 1 {
		return b.String()
	}
	header := fmt.Sprintf("History · %d matches · ↑/↓ select · Enter recall · Esc/Ctrl+C cancel", len(matches))
	b.WriteString(DimStyle.Render(ansi.Truncate(header, width, "…")) + "\n")
	visible := maxVisibleSuggestions
	if m.height > 0 {
		visible = min(visible, max(0, m.height-2))
	}
	if len(matches) == 0 && visible > 0 {
		b.WriteString(DimStyle.Render(ansi.Truncate("No matching history entry", width, "…")) + "\n")
	}
	offset := max(0, selected-visible+1)
	if visible > 0 {
		for _, i := range matches[offset:min(len(matches), offset+visible)] {
			text := "  " + strings.Join(strings.Fields(m.history[i]), " ")
			style := inputTextStyle
			if i == m.searchIdx {
				text = "› " + strings.Join(strings.Fields(m.history[i]), " ")
				style = SelectedBg
			}
			b.WriteString(style.Render(ansi.Truncate(text, width, "…")) + "\n")
		}
	}
	return b.String()
}
