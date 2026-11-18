package ui

import (
	"fmt"
	"strings"
	"testing"

	"fuckyourcli/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func drawButtons(m tea.Model) string {
	view := m.View()
	SetMouseViewport(m, 0, MouseViewHeight(m))
	return view
}

func clickButtons(m tea.Model, x, y int) (tea.Model, tea.Cmd) {
	return m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
}

func buttonCell(t *testing.T, view, label string) (int, int) {
	t.Helper()
	for row, line := range strings.Split(view, "\n") {
		plain := ansi.Strip(line)
		if pos := strings.Index(plain, label); pos >= 0 {
			return ansi.StringWidth(plain[:pos]), row
		}
	}
	t.Fatalf("missing %q in %s", label, view)
	return 0, 0
}

func TestMouseSelectorHoverDoesNotApprove(t *testing.T) {
	m := WithMouseButtons(SelectorModel{Title: "Permission\ncommand", Options: []string{"Allow", "拒否 Deny"}, SuggestIdx: -1})
	view := drawButtons(m)
	x, y := buttonCell(t, view, "拒否")
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if cmd != nil || WithoutMouseButtons(next).(SelectorModel).quitting {
		t.Fatal("hover approved")
	}
	hovered := drawButtons(next)
	if ansi.Strip(view) != ansi.Strip(hovered) {
		t.Fatal("hover changed geometry")
	}
	next, cmd = clickButtons(next, x, y)
	if cmd == nil || WithoutMouseButtons(next).(SelectorModel).Choice != 1 {
		t.Fatal("click did not deny")
	}
}

func TestMouseSelectorPinnedAndScrolled(t *testing.T) {
	options := []string{"Back", "---"}
	for i := 0; i < 25; i++ {
		options = append(options, fmt.Sprintf("Option %02d", i))
	}
	m := WithMouseButtons(SelectorModel{Title: "Menu", Options: options, Cursor: 14, Offset: 11, PinnedTop: 2, MaxVisible: 4, SuggestIdx: -1})
	view := drawButtons(m)
	x, y := buttonCell(t, view, "Option 12")
	next, cmd := clickButtons(m, x, y)
	if cmd == nil || WithoutMouseButtons(next).(SelectorModel).Choice != 14 {
		t.Fatal("wrong scrolled option")
	}
	m = WithMouseButtons(SelectorModel{Title: "Menu", Options: options, Cursor: 14, Offset: 11, PinnedTop: 2, MaxVisible: 4, SuggestIdx: -1})
	view = drawButtons(m)
	next, cmd = clickButtons(m, 3, 2)
	if cmd != nil || WithoutMouseButtons(next).(SelectorModel).quitting {
		t.Fatal("separator activated")
	}
	x, y = buttonCell(t, view, "Back")
	next, cmd = clickButtons(next, x, y)
	if cmd == nil || WithoutMouseButtons(next).(SelectorModel).Choice != 0 {
		t.Fatal("pinned back unavailable")
	}
}

func TestMouseMultiSelectRequiresSelection(t *testing.T) {
	m := WithMouseButtons(MultiSelectModel{Title: "Delete", Options: []string{"Session", "Delete selected"}, States: []bool{false, false}, RequireSelection: true})
	view := drawButtons(m)
	x, y := buttonCell(t, view, "Delete selected")
	next, cmd := clickButtons(m, x, y)
	if cmd != nil || WithoutMouseButtons(next).(MultiSelectModel).quitting {
		t.Fatal("unarmed delete activated")
	}
	x, y = buttonCell(t, view, "Session")
	next, cmd = clickButtons(next, x, y)
	if cmd != nil || !WithoutMouseButtons(next).(MultiSelectModel).States[0] {
		t.Fatal("checkbox not toggled")
	}
	view = drawButtons(next)
	x, y = buttonCell(t, view, "Delete selected")
	next, cmd = clickButtons(next, x, y)
	if cmd == nil || !WithoutMouseButtons(next).(MultiSelectModel).quitting {
		t.Fatal("armed delete unavailable")
	}
}

func TestMouseSuggestionAndCancel(t *testing.T) {
	m := WithMouseButtons(SelectorModel{Title: "Permission", Options: []string{"Allow", "Suggest"}, SuggestIdx: 1})
	x, y := buttonCell(t, drawButtons(m), "Suggest")
	m, cmd := clickButtons(m, x, y)
	if cmd != nil || !WithoutMouseButtons(m).(SelectorModel).InputMode {
		t.Fatal("not editing suggestion")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("safer command")})
	x, y = buttonCell(t, drawButtons(m), "[Confirm]")
	m, cmd = clickButtons(m, x, y)
	if cmd == nil || WithoutMouseButtons(m).(SelectorModel).Suggestion != "safer command" {
		t.Fatal("suggestion not confirmed")
	}
	m = WithMouseButtons(SelectorModel{Title: "Permission", Options: []string{"Allow", "Deny"}, SuggestIdx: -1})
	x, y = buttonCell(t, drawButtons(m), "[Cancel]")
	m, cmd = clickButtons(m, x, y)
	if cmd == nil || WithoutMouseButtons(m).(SelectorModel).Choice != SelectorCanceled {
		t.Fatal("cancel approved")
	}
}

func TestMouseSettingsAndText(t *testing.T) {
	m := WithMouseButtons(cliSettingsModel{title: "CLI", threshold: 100, step: 10, min: 0, max: 200})
	x, y := buttonCell(t, drawButtons(m), "▶")
	m, _ = clickButtons(m, x, y)
	if WithoutMouseButtons(m).(cliSettingsModel).threshold != 110 {
		t.Fatal("threshold not adjusted")
	}
	x, y = buttonCell(t, drawButtons(m), "Auto Compaction")
	m, _ = clickButtons(m, x, y)
	if !WithoutMouseButtons(m).(cliSettingsModel).enabled {
		t.Fatal("toggle not activated")
	}
}

func TestMouseTextAndEditors(t *testing.T) {
	for _, tc := range []struct {
		model tea.Model
		label string
	}{{hooksSettingsModel{}, "Pre-tool command"}, {statusHookSettingsModel{}, "Command:"}} {
		m := WithMouseButtons(tc.model)
		x, y := buttonCell(t, drawButtons(m), tc.label)
		m, cmd := clickButtons(m, x, y)
		if cmd != nil {
			t.Fatal("editor closed")
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("echo ok")})
		x, y = buttonCell(t, drawButtons(m), "[Confirm]")
		m, _ = clickButtons(m, x, y)
		switch v := WithoutMouseButtons(m).(type) {
		case hooksSettingsModel:
			if v.preCmd != "echo ok" || v.editing {
				t.Fatal("hook not saved")
			}
		case statusHookSettingsModel:
			if v.v.Command != "echo ok" || v.editing {
				t.Fatal("status hook not saved")
			}
		}
	}
	m := WithMouseButtons(textPromptModel{title: "Type yes"})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("yes")})
	x, y := buttonCell(t, drawButtons(m), "[Confirm]")
	m, cmd := clickButtons(m, x, y)
	if cmd == nil || !WithoutMouseButtons(m).(textPromptModel).ok || WithoutMouseButtons(m).(textPromptModel).value != "yes" {
		t.Fatal("text not confirmed")
	}
}

func TestMouseWheelAndClippedViewport(t *testing.T) {
	m := WithMouseButtons(SelectorModel{Title: "Menu", Options: []string{"Allow", "Deny"}, SuggestIdx: -1})
	view := m.View()
	SetMouseViewport(m, -1, 4)
	x, y := buttonCell(t, view, "Deny")
	m, cmd := clickButtons(m, x, y-1)
	if cmd == nil || WithoutMouseButtons(m).(SelectorModel).Choice != 1 {
		t.Fatal("clipped origin not translated")
	}
	m = WithMouseButtons(TextViewerModel{Title: "Details", termWidth: 40, termHeight: 8, Lines: []types.DetailLine{{Text: strings.Repeat("content ", 100)}}})
	drawButtons(m)
	m, _ = m.Update(tea.MouseMsg{X: 0, Y: 2, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if WithoutMouseButtons(m).(TextViewerModel).offset != 1 {
		t.Fatal("wheel did not scroll viewer")
	}
	x, y = buttonCell(t, drawButtons(m), "[Cancel]")
	_, cmd = clickButtons(m, x, y)
	if cmd == nil {
		t.Fatal("viewer did not close")
	}
}

func TestMouseStaleGeometryAndBounds(t *testing.T) {
	m := WithMouseButtons(SelectorModel{Title: "Permission", Options: []string{"Allow", "Deny"}, SuggestIdx: -1})
	view := drawButtons(m)
	x, y := buttonCell(t, view, "Allow")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := clickButtons(m, x, y)
	if cmd != nil {
		t.Fatal("stale geometry approved")
	}
	drawButtons(m)
	for _, p := range [][2]int{{-1, 1}, {100, 1}, {1, -1}, {1, 100}} {
		_, cmd = clickButtons(m, p[0], p[1])
		if cmd != nil {
			t.Fatal("outside click approved")
		}
	}
}
