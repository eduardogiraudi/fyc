package ui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// On Windows bubbletea delivers a pasted newline as a discrete KeyEnter
// (KeyMsg.Paste is always false), so a naive Enter handler would submit the
// first line. These tests pin the deferred-Enter workaround.

func TestDeferredEnterPasteInsertsNewline(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("deferred-Enter workaround is Windows-only")
	}

	m := newInputModel(nil, nil)

	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1")})

	// The newline of the paste: must NOT submit yet.
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyEnter})
	if m.submitted {
		t.Fatal("Enter must be deferred, not submitted immediately")
	}
	if !m.pendingEnter {
		t.Fatal("expected pendingEnter to be set after Enter")
	}
	seq := m.enterSeq

	// The rest of the paste arrives before the idle timer: the deferred Enter
	// becomes a literal newline and nothing is submitted.
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line2")})
	if m.pendingEnter {
		t.Fatal("pendingEnter must be cleared by subsequent input")
	}
	if m.submitted {
		t.Fatal("a multi-line paste must not submit")
	}
	if got := m.fullText(); got != "line1\nline2" {
		t.Fatalf("fullText = %q, want %q", got, "line1\nline2")
	}

	// A stale timer (the one armed by the pasted Enter) is a no-op.
	m, _ = m.UpdateComponent(InputEnterIdleMsg{seq: seq})
	if m.submitted {
		t.Fatal("stale idle timer must not submit")
	}
}

func TestDeferredEnterSubmitsWhenIdle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("deferred-Enter workaround is Windows-only")
	}

	m := newInputModel(nil, nil)

	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyEnter})
	if m.submitted {
		t.Fatal("Enter must be deferred")
	}

	// No further input: the timer fires and the buffer is submitted.
	m, _ = m.UpdateComponent(InputEnterIdleMsg{seq: m.enterSeq})
	if !m.submitted {
		t.Fatal("expected submit once the enter goes idle")
	}
	if got := m.Value(); got != "hello" {
		t.Fatalf("submitted value = %q, want %q", got, "hello")
	}
}

func suggestionWindowFixture() (inputModel, []Suggestion) {
	var suggestions []Suggestion
	for i := 0; i < 7; i++ {
		suggestions = append(suggestions, Suggestion{Name: fmt.Sprintf("/command-%d", i), Description: "description"})
	}
	m := newInputModel(nil, func(line string) ([]Suggestion, string) {
		var matches []Suggestion
		for _, s := range suggestions {
			if strings.HasPrefix(s.Name, line) {
				matches = append(matches, s)
			}
		}
		return matches, "extra hint"
	})
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	return m, suggestions
}

func assertSuggestionWindow(t *testing.T, m inputModel, suggestions []Suggestion, offset int) {
	t.Helper()
	if m.suggOffset != offset {
		t.Fatalf("offset = %d, want %d", m.suggOffset, offset)
	}
	view := stripControlSequences(m.View())
	end := min(offset+maxVisibleSuggestions, len(suggestions))
	for i, s := range suggestions {
		if got, want := strings.Contains(view, s.Name), i >= offset && i < end; got != want {
			t.Fatalf("visibility of %q = %v, want %v:\n%s", s.Name, got, want, view)
		}
	}
	if got := strings.Count(view, "  description\n"); got != end-offset {
		t.Fatalf("visible suggestion rows = %d, want %d:\n%s", got, end-offset, view)
	}
	if len(suggestions) > maxVisibleSuggestions {
		counts := fmt.Sprintf("↑ %d hidden · ↓ %d hidden", offset, len(suggestions)-end)
		if !strings.Contains(view, counts) {
			t.Fatalf("missing hidden counts %q:\n%s", counts, view)
		}
	}
	if !strings.Contains(view, "extra hint") {
		t.Fatalf("extra hint was lost:\n%s", view)
	}
	if m.suggSelected < offset || m.suggSelected >= end {
		t.Fatalf("selected index %d is outside [%d,%d)", m.suggSelected, offset, end)
	}
}

func TestSuggestionWindowScrollsAndWraps(t *testing.T) {
	m, suggestions := suggestionWindowFixture()
	assertSuggestionWindow(t, m, suggestions, 0)
	height := strings.Count(m.View(), "\n")
	steps := []struct {
		key      tea.KeyType
		selected int
		offset   int
	}{
		{tea.KeyDown, 1, 0},
		{tea.KeyDown, 2, 0},
		{tea.KeyDown, 3, 0},
		{tea.KeyDown, 4, 1},
		{tea.KeyDown, 5, 2},
		{tea.KeyDown, 6, 3},
		{tea.KeyDown, 0, 0},
		{tea.KeyUp, 6, 3},
		{tea.KeyUp, 5, 3},
		{tea.KeyUp, 4, 3},
		{tea.KeyUp, 3, 3},
		{tea.KeyUp, 2, 2},
		{tea.KeyUp, 1, 1},
		{tea.KeyUp, 0, 0},
	}
	for i, step := range steps {
		m, _ = m.UpdateComponent(tea.KeyMsg{Type: step.key})
		if m.suggSelected != step.selected {
			t.Fatalf("step %d: selection = %d, want %d", i, m.suggSelected, step.selected)
		}
		assertSuggestionWindow(t, m, suggestions, step.offset)
		if got := strings.Count(m.View(), "\n"); got != height {
			t.Fatalf("step %d: view height = %d, want %d", i, got, height)
		}
	}
}

func TestSuggestionWindowFilteringAndAcceptingHiddenCommand(t *testing.T) {
	m, _ := suggestionWindowFixture()
	for i := 0; i < 5; i++ {
		m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyDown})
	}
	m = m.updateSuggestions()
	if m.suggSelected != 5 || m.suggOffset != 2 {
		t.Fatal("unchanged matches must keep the selection and window")
	}
	m, cmd := m.handleEnter()
	if cmd != nil || m.firstLine() != "/command-5" || m.submitted {
		t.Fatalf("Enter did not accept the scrolled selection: %q", m.firstLine())
	}
	if m.suggOffset != 0 || m.suggSelected != 0 || len(m.suggestions) != 1 {
		t.Fatal("accepting a command must reset the filtered window")
	}
	m, cmd = m.handleEnter()
	if cmd == nil || !m.submitted || m.Value() != "/command-5" {
		t.Fatal("second Enter must submit the selected command")
	}
	if strings.Contains(m.View(), "suggestions") {
		t.Fatal("submitted input must hide suggestions")
	}
}

func TestSuggestionWindowResetsWhenMatchesChange(t *testing.T) {
	m, suggestions := suggestionWindowFixture()
	m.suggProvider = func(line string) ([]Suggestion, string) {
		if line == "/" {
			return suggestions, "extra hint"
		}
		return suggestions[3:], "extra hint"
	}
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.suggSelected != 0 || m.suggOffset != 0 {
		t.Fatal("filtering must restart at the first match")
	}
	assertSuggestionWindow(t, m, suggestions[3:], 0)
	if strings.Contains(m.View(), "hidden") {
		t.Fatal("four matches must not show hidden counts")
	}
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyDown})
	m.suggProvider = func(string) ([]Suggestion, string) { return suggestions[:4], "extra hint" }
	m = m.updateSuggestions()
	if m.suggSelected != 0 || m.suggOffset != 0 {
		t.Fatal("different matches of the same length must reset the selection")
	}
	m.suggProvider = func(string) ([]Suggestion, string) { return nil, "" }
	m = m.updateSuggestions()
	if m.suggOffset != 0 || m.suggSelected != 0 || strings.Contains(m.View(), "suggestions") {
		t.Fatal("empty matches must clear the window")
	}
}

func TestSuggestionWindowTabUsesHiddenMatches(t *testing.T) {
	m := newInputModel(nil, func(string) ([]Suggestion, string) {
		return []Suggestion{
			{Name: "/abc-one"}, {Name: "/abc-two"}, {Name: "/abc-three"}, {Name: "/abc-four"},
			{Name: "/ax-hidden"},
		}, ""
	})
	m.setFullText("/")
	m = m.updateSuggestions()
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.firstLine(); got != "/a" {
		t.Fatalf("Tab prefix = %q, want /a (including hidden matches)", got)
	}
}
