package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runeKey builds a KeyMsg for a printable rune.
func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// TestSelectorQuitReturnsCancelSentinel guards the fix for the bug where
// q / Ctrl+C / Esc in a selector behaved like pressing Enter on the first
// option: dismissal must yield SelectorCanceled, never a valid index.
func TestSelectorQuitReturnsCancelSentinel(t *testing.T) {
	cases := map[string]tea.KeyMsg{
		"q":      runeKey('q'),
		"ctrl+c": {Type: tea.KeyCtrlC},
		"esc":    {Type: tea.KeyEsc},
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			m := SelectorModel{Title: "t", Options: []string{"a", "b", "c"}, SuggestIdx: -1}
			updated, cmd := m.Update(key)
			sm := updated.(SelectorModel)
			if sm.Choice != SelectorCanceled {
				t.Fatalf("%s: Choice = %d, want SelectorCanceled (%d)", name, sm.Choice, SelectorCanceled)
			}
			if !sm.quitting {
				t.Fatalf("%s: selector should be quitting", name)
			}
			if cmd == nil {
				t.Fatalf("%s: expected a quit command", name)
			}
		})
	}
}

// TestSelectorCancelSentinelIsNegative ensures the sentinel can never collide
// with a real option index.
func TestSelectorCancelSentinelIsNegative(t *testing.T) {
	if SelectorCanceled >= 0 {
		t.Fatalf("SelectorCanceled = %d, want a negative sentinel", SelectorCanceled)
	}
}

// TestSelectorEnterReturnsCursor is the control case: a real confirmation still
// returns the highlighted row.
func TestSelectorEnterReturnsCursor(t *testing.T) {
	m := SelectorModel{Title: "t", Options: []string{"a", "b", "c"}, Cursor: 2, SuggestIdx: -1}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(SelectorModel)
	if sm.Choice != 2 {
		t.Fatalf("enter: Choice = %d, want 2", sm.Choice)
	}
}

// TestMultiSelectQuitCancelsStates ensures cancelling a multi-select discards
// the toggles (nil slice), so callers treat it as "no change".
func TestMultiSelectQuitCancelsStates(t *testing.T) {
	m := MultiSelectModel{
		Title:   "t",
		Options: []string{"a", "b", "Done"},
		States:  []bool{true, false, true},
	}
	updated, _ := m.Update(runeKey('q'))
	sm := updated.(MultiSelectModel)
	if sm.States != nil {
		t.Fatalf("q should cancel and nil the states, got %v", sm.States)
	}
}

// TestMultiSelectDoneKeepsStates is the control case: confirming with "Done"
// keeps the toggled states.
func TestMultiSelectDoneKeepsStates(t *testing.T) {
	m := MultiSelectModel{
		Title:   "t",
		Options: []string{"a", "b", "Done"},
		States:  []bool{true, false, true},
		Cursor:  2,
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(MultiSelectModel)
	if len(sm.States) != 3 {
		t.Fatalf("Done should keep the states, got %v", sm.States)
	}
}

// TestMultiSelectRequireSelectionBlocksEmptyAction: with RequireSelection set,
// confirming the action row while nothing is toggled must be a no-op.
func TestMultiSelectRequireSelectionBlocksEmptyAction(t *testing.T) {
	m := MultiSelectModel{
		Title:            "t",
		Options:          []string{"a", "b", "Delete"},
		States:           []bool{false, false, true},
		Cursor:           2,
		RequireSelection: true,
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(MultiSelectModel)
	if sm.quitting {
		t.Fatalf("action row must not confirm with an empty selection")
	}
	if cmd != nil {
		t.Fatalf("expected no quit command, got one")
	}
}

// TestMultiSelectRequireSelectionAllowsAfterToggle is the control case: once a
// row is toggled, the action row confirms normally.
func TestMultiSelectRequireSelectionAllowsAfterToggle(t *testing.T) {
	m := MultiSelectModel{
		Title:            "t",
		Options:          []string{"a", "b", "Delete"},
		States:           []bool{false, true, true},
		Cursor:           2,
		RequireSelection: true,
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(MultiSelectModel)
	if !sm.quitting {
		t.Fatalf("action row should confirm once something is selected")
	}
	if cmd == nil {
		t.Fatalf("expected a quit command")
	}
}

// TestMultiSelectHasSelectionIgnoresActionRow: the trailing action row is never
// counted as a toggle, even when its own state is true.
func TestMultiSelectHasSelectionIgnoresActionRow(t *testing.T) {
	m := MultiSelectModel{
		Options: []string{"a", "b", "Done"},
		States:  []bool{false, false, true},
	}
	if m.hasSelection() {
		t.Fatalf("action row state must not count as a selection")
	}
	m.States[0] = true
	if !m.hasSelection() {
		t.Fatalf("a toggled row must count as a selection")
	}
}

// TestMultiSelectRequireSelectionActionUnreachable: while nothing is toggled the
// Down key must not move the cursor onto the trailing action row.
func TestMultiSelectRequireSelectionActionUnreachable(t *testing.T) {
	m := MultiSelectModel{
		Options:          []string{"a", "b", "Delete"},
		States:           []bool{false, false, true},
		Cursor:           0,
		RequireSelection: true,
	}
	down := tea.KeyMsg{Type: tea.KeyDown}
	for i := 0; i < 5; i++ {
		updated, _ := m.Update(down)
		m = updated.(MultiSelectModel)
	}
	if m.Cursor != 1 {
		t.Fatalf("cursor reached %d, want the last toggle row 1", m.Cursor)
	}
}

// TestMultiSelectRequireSelectionActionReachableAfterToggle is the control case:
// once a row is toggled the action row becomes reachable.
func TestMultiSelectRequireSelectionActionReachableAfterToggle(t *testing.T) {
	m := MultiSelectModel{
		Options:          []string{"a", "b", "Delete"},
		States:           []bool{false, false, true},
		Cursor:           1,
		RequireSelection: true,
	}
	// Toggle row 1 (index 1), then move down to the action row.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(MultiSelectModel)
	down := tea.KeyMsg{Type: tea.KeyDown}
	updated, _ = m.Update(down)
	m = updated.(MultiSelectModel)
	if m.Cursor != 2 {
		t.Fatalf("cursor = %d, want the action row 2 once something is selected", m.Cursor)
	}
}

// TestMultiSelectActionReachableWithoutRequirement guards the existing callers:
// without RequireSelection the action row is always reachable.
func TestMultiSelectActionReachableWithoutRequirement(t *testing.T) {
	m := MultiSelectModel{
		Options: []string{"a", "b", "Done"},
		States:  []bool{false, false, true},
		Cursor:  0,
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(MultiSelectModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(MultiSelectModel)
	if m.Cursor != 2 {
		t.Fatalf("cursor = %d, want the action row 2", m.Cursor)
	}
}
