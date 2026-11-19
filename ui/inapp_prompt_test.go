package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The in-app host stays off unless a running chat installs it: everywhere else
// the interactive helpers must fall back to a nested program.
func TestRunInAppIsDisabledByDefault(t *testing.T) {
	if _, ok := runInApp(SelectorModel{Options: []string{"Allow", "Deny"}, SuggestIdx: -1}); ok {
		t.Fatal("a model was hosted without a running chat")
	}
}

// Hosting draws the very same model the nested program would: a long list keeps
// the cursor row on screen and shows its label.
func TestHostedSelectorKeepsCursorVisible(t *testing.T) {
	options := make([]string, 30)
	for i := range options {
		options[i] = fmt.Sprintf("row-%d", i)
	}
	m := SelectorModel{Title: "Pick", Options: options, SuggestIdx: -1, MaxVisible: 5}
	for i := 0; i < 20; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(SelectorModel)
	}
	if m.Cursor != 20 || m.Offset == 0 {
		t.Fatalf("cursor %d, offset %d: the list did not scroll", m.Cursor, m.Offset)
	}
	if !strings.Contains(m.View(), options[20]) {
		t.Fatalf("the cursor row is not drawn:\n%s", m.View())
	}
}

// A block rendered before the chat program exists (the resumed conversation is
// replayed at start-up) goes to the registered sink instead of stdout: the
// alternate screen would hide anything printed before it.
func TestPrintRenderedBlockGoesToTheRegisteredSink(t *testing.T) {
	var got []string
	SetPreProgramBlockSink(func(block SeedBlock) { got = append(got, block.Text) })
	defer SetPreProgramBlockSink(nil)

	sink := &TerminalOutputSink{}
	sink.PrintBlock("resumed turn")
	PrintRenderedBlock("another")
	if len(got) != 2 || got[0] != "resumed turn" || got[1] != "another" {
		t.Fatalf("blocks handed to the seed = %q", got)
	}
}
