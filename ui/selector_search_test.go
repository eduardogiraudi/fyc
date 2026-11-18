package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func searchUpdate(m SelectorModel, msg tea.Msg) SelectorModel {
	next, _ := m.Update(msg)
	return next.(SelectorModel)
}

func TestSearchableSelectorFiltersAndMapsChoice(t *testing.T) {
	m := newSearchableSelector("Models", []string{"first", "Qwen-small", "other", "qwen-large"})
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("qwEN")})
	if len(m.Options) != 2 || m.quitting {
		t.Fatalf("filter = %q, quitting = %v", m.Options, m.quitting)
	}
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyDown})
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.Choice != 3 || !m.quitting {
		t.Fatalf("choice = %d, quitting = %v", m.Choice, m.quitting)
	}
}

func TestSearchableSelectorEmptyAndClear(t *testing.T) {
	m := newSearchableSelector("Models", []string{"alpha", "模型"})
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("missing")})
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.quitting || m.Choice != SelectorCanceled || !strings.Contains(m.View(), "No matching models") {
		t.Fatal("empty results accepted a selection")
	}
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if len(m.Options) != 2 || m.Offset != 0 || m.Cursor != 0 {
		t.Fatal("clearing did not restore the list")
	}
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("模型")})
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.search.Value() != "模" || len(m.Options) != 1 {
		t.Fatal("Unicode editing failed")
	}
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlC} {
		cancelled := searchUpdate(m, tea.KeyMsg{Type: key})
		if cancelled.Choice != SelectorCanceled || !cancelled.quitting {
			t.Fatal("dismissal selected an option")
		}
	}
}

func TestSearchableSelectorSearchStaysAboveScrollOnResize(t *testing.T) {
	options := make([]string, 50)
	for i := range options {
		options[i] = fmt.Sprintf("model-%02d", i)
	}
	m := newSearchableSelector("Models", options)
	m = searchUpdate(m, tea.WindowSizeMsg{Width: 50, Height: 16})
	before := strings.Split(ansi.Strip(m.View()), "\n")
	for i := 0; i < 30; i++ {
		m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m = searchUpdate(m, tea.WindowSizeMsg{Width: 35, Height: 14})
	after := strings.Split(ansi.Strip(m.View()), "\n")
	if m.Offset == 0 || !strings.Contains(after[1], "Search:") || !strings.Contains(before[1], "Search:") {
		t.Fatal("search moved into the scrolled list")
	}
	if !strings.Contains(m.View(), "model-30") {
		t.Fatal("resize hid the selected row")
	}
	for _, row := range after {
		if ansi.StringWidth(row) > 35 {
			t.Fatal("row exceeded terminal width")
		}
	}
}

func TestSearchableSelectorHostedMouseUsesOriginalIndex(t *testing.T) {
	m := newSearchableSelector("Models", []string{"alpha", "beta", "beta-large"})
	m = searchUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("beta")})
	hosted := WithMouseButtons(m)
	view := drawButtons(hosted)
	x, y := buttonCell(t, view, "Search:")
	next, cmd := clickButtons(hosted, x, y)
	if cmd != nil || WithoutMouseButtons(next).(SelectorModel).quitting {
		t.Fatal("search bar click accepted a model")
	}
	view = drawButtons(next)
	x, y = buttonCell(t, view, "beta-large")
	next, cmd = clickButtons(next, x, y)
	if cmd == nil || WithoutMouseButtons(next).(SelectorModel).Choice != 2 {
		t.Fatal("mouse selected the filtered index instead of the original index")
	}
}
