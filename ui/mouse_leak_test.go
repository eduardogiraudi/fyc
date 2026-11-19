package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func altBracket() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true}
}

func feed(t *testing.T, keys ...tea.KeyMsg) string {
	t.Helper()
	m := newInputModel(nil, nil)
	for _, k := range keys {
		m, _ = m.UpdateComponent(k)
	}
	return m.fullText()
}

// The shapes below are what bubbletea v1.3.10 emits when a stdin chunk ends
// inside an SGR mouse report (verified against its detectOneMsg).
func TestMouseLeakSplitReportsAreDropped(t *testing.T) {
	cases := map[string][]tea.KeyMsg{
		"alt-bracket then whole remainder": {altBracket(), runes("<35;16;5M")},
		"alt-bracket then pieces":          {altBracket(), runes("<35;1"), runes("6;5M")},
		"alt-bracket then lone lt":         {altBracket(), runes("<"), runes("35;16;5M")},
		"lone esc then bracket remainder":  {{Type: tea.KeyEsc}, runes("[<35;16;5M")},
		"lone esc then split remainder":    {{Type: tea.KeyEsc}, runes("["), runes("<35;16;5m")},
		"several in a row": {
			altBracket(), runes("<35;16;5M"),
			altBracket(), runes("<35;15;7M"),
			altBracket(), runes("<35;14;9M"),
		},
	}
	for name, keys := range cases {
		t.Run(name, func(t *testing.T) {
			if got := feed(t, keys...); got != "" {
				t.Fatalf("leaked into the prompt: %q", got)
			}
		})
	}
}

func TestMouseLeakKeepsRealTyping(t *testing.T) {
	if got := feed(t, runes("hello"), runes(" [<3]")); got != "hello [<3]" {
		t.Fatalf("typed text was altered: %q", got)
	}
	// Text right after a finished report is kept.
	if got := feed(t, altBracket(), runes("<35;16;5Mabc")); got != "abc" {
		t.Fatalf("text after a report must survive, got %q", got)
	}
	// A cut that is not a mouse report: only the useless Alt+[ is dropped.
	if got := feed(t, altBracket(), runes("A")); got != "A" {
		t.Fatalf("got %q", got)
	}
	// Esc alone still dismisses and does not swallow what the user types next.
	if got := feed(t, tea.KeyMsg{Type: tea.KeyEsc}, runes("x")); got != "x" {
		t.Fatalf("got %q", got)
	}
	// Pasted text is never touched.
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;16;5M"), Paste: true}
	if got := feed(t, paste); got != "[<35;16;5M" {
		t.Fatalf("paste was altered: %q", got)
	}
}

func TestMouseLeakStateExpires(t *testing.T) {
	var f mouseLeakFilter
	now := time.Now()
	f, _, drop := f.filter(altBracket(), now)
	if !drop {
		t.Fatal("Alt+[ must be dropped")
	}
	// A human-speed follow-up is real typing.
	_, _, drop = f.filter(runes("<35;16;5M"), now.Add(mouseLeakWindow+time.Millisecond))
	if drop {
		t.Fatal("a stale filter state must not eat typed text")
	}
}
