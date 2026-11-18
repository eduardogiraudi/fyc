package ui

import (
	"strings"
	"testing"

	"fuckyourcli/internal/mentions"
	tea "github.com/charmbracelet/bubbletea"
)

func mentionInput() inputModel {
	return newInputModel(nil, func(prefix string) ([]Suggestion, string) {
		if _, ok := mentions.Active(prefix); ok {
			return []Suggestion{{Name: "@test.py"}, {Name: "@\"my dir/file.py\""}}, "files"
		}
		return nil, ""
	})
}

func TestMentionCompletionPreservesMultilinePrompt(t *testing.T) {
	m := mentionInput()
	m.setFullText("α first line\ninspect @te suffix")
	m.col = len([]rune("inspect @te"))
	m = m.updateSuggestions()
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.fullText(); got != "α first line\ninspect @test.py  suffix" {
		t.Fatalf("prompt overwritten: %q", got)
	}
	if m.row != 1 || m.submitted {
		t.Fatal("completion changed row or submitted")
	}
}

func TestMentionCompletionReplacesWholeToken(t *testing.T) {
	m := mentionInput()
	m.setFullText("inspect @testing.py suffix")
	m.col = len([]rune("inspect @te"))
	m = m.updateSuggestions()
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.fullText(); got != "inspect @test.py  suffix" {
		t.Fatalf("left token suffix: %q", got)
	}
}

func TestMentionEnterAndDismiss(t *testing.T) {
	m := mentionInput()
	m.setFullText("inspect @te")
	m = m.updateSuggestions()
	m.suggSelected = 1
	m, cmd := m.handleEnter()
	if cmd != nil || m.submitted || !strings.Contains(m.fullText(), "@\"my dir/file.py\" ") {
		t.Fatalf("Enter didn't insert: %q", m.fullText())
	}
	m.setFullText("inspect @te")
	m = m.updateSuggestions()
	m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.suggestions) != 0 {
		t.Fatal("Esc didn't dismiss")
	}
	m, cmd = m.handleEnter()
	if !m.submitted || cmd == nil {
		t.Fatal("dismissed popup prevented submit")
	}
}

func TestMentionCursorRefresh(t *testing.T) {
	m := mentionInput()
	m.setFullText("@te suffix")
	m = m.updateSuggestions()
	if len(m.suggestions) != 0 {
		t.Fatal("popup away from mention")
	}
	for i := 0; i < len(" suffix"); i++ {
		m, _ = m.UpdateComponent(tea.KeyMsg{Type: tea.KeyLeft})
	}
	if len(m.suggestions) != 2 {
		t.Fatal("cursor movement didn't refresh popup")
	}
}

func TestMentionQuotedDirectoryCompletion(t *testing.T) {
	m := newInputModel(nil, func(prefix string) ([]Suggestion, string) {
		return []Suggestion{{Name: "@\"my dir/\""}}, ""
	})
	m.setFullText("inspect @my")
	m = m.updateSuggestions()
	m = m.completeSuggestion()
	m = m.insertRunes([]rune("file.py"))
	if got := m.fullText(); got != "inspect @\"my dir/file.py\"" {
		t.Fatalf("directory completion left cursor outside quotes: %q", got)
	}
}
