package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func searchKey(m inputModel, key tea.KeyType) inputModel {
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: key})
	return m
}

func TestHistorySearchMatchesAndRecallsWithoutSubmitting(t *testing.T) {
	m := newInputModel([]string{"old ALPHA", "unrelated", "new alpha\n日本語"}, nil)
	m.setFullText("alpha")
	m = searchKey(m, tea.KeyCtrlR)
	if !m.searchActive || m.searchIdx != 2 {
		t.Fatal("search must start from the draft and newest match")
	}
	m = searchKey(m, tea.KeyCtrlR)
	if m.searchIdx != 0 {
		t.Fatal("repeated Ctrl+R must select the older match")
	}
	m = searchKey(m, tea.KeyCtrlR)
	if m.searchIdx != 0 {
		t.Fatal("exhausted search must retain the oldest match")
	}
	m = searchKey(m, tea.KeyUp)
	m, cmd := m.UpdateComponent(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.submitted || m.searchActive || m.fullText() != "new alpha\n日本語" {
		t.Fatal("Enter must recall the whole prompt without submitting")
	}
	m = searchKey(m, tea.KeyDown)
	if m.fullText() != "alpha" {
		t.Fatal("history navigation must restore the live draft")
	}
}

func TestHistorySearchCancelPreservesDraftCursorAndNavigation(t *testing.T) {
	m := newInputModel([]string{"first", "second"}, nil)
	m.setFullText("draft\n日本語")
	m.row, m.col = 0, 2
	m.histIdx, m.liveBuf = 0, "saved draft"
	m = searchKey(m, tea.KeyCtrlR)
	m = searchKey(m, tea.KeyCtrlU)
	m = searchKey(m, tea.KeyEsc)
	if m.fullText() != "draft\n日本語" || m.row != 0 || m.col != 2 || m.histIdx != 0 || m.liveBuf != "saved draft" {
		t.Fatal("cancel changed the original input state")
	}
}

func TestHistorySearchQueryEditingAndNoMatches(t *testing.T) {
	m := newInputModel([]string{"日本語 test", "other"}, nil)
	m = searchKey(m, tea.KeyCtrlR)
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("日本語"), Paste: true})
	if m.searchIdx != 0 {
		t.Fatal("Unicode pasted query must match")
	}
	m = searchKey(m, tea.KeySpace)
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.searchIdx != -1 || !strings.Contains(m.View(), "No matching history entry") {
		t.Fatal("missing query must show no match")
	}
	m = searchKey(m, tea.KeyBackspace)
	if string(m.searchQuery) != "日本語 " || m.searchIdx != 0 {
		t.Fatal("Backspace must edit by rune and refresh matches")
	}
	m = searchKey(m, tea.KeyCtrlU)
	if len(m.searchQuery) != 0 || m.searchIdx != 1 {
		t.Fatal("cleared query must select newest entry")
	}
}

func TestHistorySearchEmptyHistoryDoesNotSubmit(t *testing.T) {
	m := newInputModel(nil, nil)
	m.setFullText("draft")
	m = searchKey(m, tea.KeyCtrlR)
	m, cmd := m.UpdateComponent(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.submitted || m.searchActive || m.fullText() != "draft" {
		t.Fatal("empty search must not submit or replace draft")
	}
}

func TestHistorySearchViewWidthAndQueryAtTop(t *testing.T) {
	for _, width := range []int{1, 10, 80} {
		m := newInputModel([]string{strings.Repeat("long 日本語\n", 100)}, nil)
		m.width = width
		m = searchKey(m, tea.KeyCtrlR)
		rows := strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n")
		if len(rows) != 3 {
			t.Fatalf("search height = %d, want 3", len(rows))
		}
		for _, row := range rows {
			if ansi.StringWidth(row) > width {
				t.Fatalf("search row overflows width %d: %q", width, row)
			}
		}
		if width == 80 && !strings.Contains(rows[0], "Search history") {
			t.Fatal("query must be above the results")
		}
	}
}

func TestHistorySearchSuppressesSlashSuggestions(t *testing.T) {
	m := newInputModel([]string{"/help"}, func(string) ([]Suggestion, string) {
		return []Suggestion{{Name: "/help"}}, ""
	})
	m = m.updateSuggestions()
	m = searchKey(m, tea.KeyCtrlR)
	if strings.Contains(m.View(), "suggestions") {
		t.Fatal("slash suggestions must not obscure history search")
	}
	m = searchKey(m, tea.KeyTab)
	if m.fullText() != "/help" || m.submitted {
		t.Fatal("Tab must recall slash commands without submitting")
	}
}

func TestHistorySearchCtrlCCancelsWithoutCommand(t *testing.T) {
	m := newInputModel([]string{"entry"}, nil)
	m.setFullText("draft")
	m.col = 2
	m = searchKey(m, tea.KeyCtrlR)
	m = searchKey(m, tea.KeyCtrlU)
	m, cmd := m.UpdateComponent(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || m.searchActive || m.fullText() != "draft" || m.col != 2 {
		t.Fatal("Ctrl+C must close search and preserve the draft without cancelling input")
	}
}

func TestHistorySearchBarEditingAndScrolling(t *testing.T) {
	m := newInputModel([]string{"match 0", "match 1", "match 2", "match 3", "match 4", "match 5"}, nil)
	m = searchKey(m, tea.KeyCtrlR)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "6 matches") || !strings.Contains(view, "› match 5") || strings.Contains(view, "match 0") {
		t.Fatalf("unexpected initial result list:\n%s", view)
	}
	for i := 0; i < 5; i++ {
		m = searchKey(m, tea.KeyDown)
	}
	if m.searchIdx != 0 || !strings.Contains(ansi.Strip(m.View()), "› match 0") {
		t.Fatal("Down must scroll the filtered list and keep selection visible")
	}
	m = searchKey(m, tea.KeyUp)
	if m.searchIdx != 1 {
		t.Fatal("Up must move to the result above")
	}
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("mtch")})
	m = searchKey(m, tea.KeyHome)
	m = searchKey(m, tea.KeyRight)
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if string(m.searchQuery) != "match" || m.searchCol != 2 || m.searchIdx != 5 {
		t.Fatal("typing in the middle must edit the search bar and refresh results")
	}
	m = searchKey(m, tea.KeyDelete)
	if string(m.searchQuery) != "mach" {
		t.Fatal("Delete must remove the rune after the cursor")
	}
	m = searchKey(m, tea.KeyBackspace)
	if string(m.searchQuery) != "mch" || m.searchCol != 1 {
		t.Fatal("Backspace must remove the rune before the cursor")
	}
	m = searchKey(m, tea.KeyEnd)
	if m.searchCol != len(m.searchQuery) {
		t.Fatal("End must move to the end of the search bar")
	}
}

func historySearchMouse(button tea.MouseButton, y int) tea.MouseMsg {
	return tea.MouseMsg{Button: button, Action: tea.MouseActionPress, X: 2, Y: y}
}

func TestHistorySearchMouseWheelScrollsResults(t *testing.T) {
	m := newInputModel([]string{"match 0", "match 1", "match 2", "match 3", "match 4", "match 5"}, nil)
	m = searchKey(m, tea.KeyCtrlR)
	for i := 0; i < 4; i++ {
		updated, cmd, handled := m.UpdateMouse(historySearchMouse(tea.MouseButtonWheelDown, 2))
		if !handled || cmd != nil {
			t.Fatal("wheel over the history results was not handled")
		}
		m = updated
	}
	view := ansi.Strip(m.View())
	if m.searchIdx != 1 || !strings.Contains(view, "› match 1") || strings.Contains(view, "match 5") || strings.Contains(view, "match 0") {
		t.Fatalf("wheel did not scroll the filtered results:\n%s", view)
	}
	updated, _, handled := m.UpdateMouse(historySearchMouse(tea.MouseButtonWheelUp, 2))
	if !handled || updated.searchIdx != 2 {
		t.Fatalf("wheel up selection = %d, handled=%v; want 2/true", updated.searchIdx, handled)
	}
	before := updated
	if after, cmd, handled := updated.UpdateMouse(historySearchMouse(tea.MouseButtonWheelDown, 6)); handled || cmd != nil || after.searchIdx != before.searchIdx {
		t.Fatal("wheel outside history search must fall through to the transcript")
	}
}

func TestHistorySearchMouseClickRecallsWithoutSubmitting(t *testing.T) {
	m := newInputModel([]string{"match 0", "match 1", "match 2", "match 3", "match 4", "match 5"}, nil)
	m.setFullText("draft")
	m = searchKey(m, tea.KeyCtrlR)
	m = searchKey(m, tea.KeyCtrlU)
	for i := 0; i < 4; i++ {
		updated, _, _ := m.UpdateMouse(historySearchMouse(tea.MouseButtonWheelDown, 2))
		m = updated
	}
	updated, cmd, handled := m.UpdateMouse(historySearchMouse(tea.MouseButtonLeft, 5))
	m = updated
	if !handled || cmd != nil || m.searchActive || m.submitted || m.fullText() != "match 1" || m.liveBuf != "draft" {
		t.Fatalf("click recalled %q, active=%v submitted=%v handled=%v", m.fullText(), m.searchActive, m.submitted, handled)
	}
}

func TestHistorySearchMouseConsumesDecorations(t *testing.T) {
	m := newInputModel([]string{"entry"}, nil)
	m = searchKey(m, tea.KeyCtrlR)
	updated, cmd, handled := m.UpdateMouse(historySearchMouse(tea.MouseButtonLeft, 1))
	if !handled || cmd != nil || !updated.searchActive || updated.searchIdx != 0 || updated.fullText() != "" {
		t.Fatal("clicking the history header changed the search or input")
	}
	m.height = 1
	if _, _, handled := m.UpdateMouse(historySearchMouse(tea.MouseButtonLeft, 1)); handled {
		t.Fatal("a clipped history row must not be clickable")
	}
}
