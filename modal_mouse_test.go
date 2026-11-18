package main

import (
	"strings"
	"testing"

	"fuckyourcli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func modalCell(t *testing.T, m chatModel, label string) (int, int) {
	t.Helper()
	for row, line := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(line)
		if pos := strings.Index(plain, label); pos >= 0 {
			return ansi.StringWidth(plain[:pos]), row
		}
	}
	t.Fatalf("no %q in frame", label)
	return 0, 0
}

func TestModalMouseAnswersWithoutSelectingTranscript(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {24, 5}} {
		m := newChatModel(nil, nil, nil, nil)
		m.width, m.height = size[0], size[1]
		m.enableAltScreen([]string{"previous output"})
		m.statsProviderInfo = "provider · Mode: manual"
		m.statsTokenText = "tokens"
		m, reply := openModal(t, m, []string{"Allow", "Deny"})
		x, y := modalCell(t, m, "Deny")
		next, cmd := m.Update(press(x, y))
		m = feed(t, next.(chatModel), cmd)
		select {
		case final := <-reply:
			if final.(ui.SelectorModel).Choice != 1 {
				t.Fatal("click chose wrong option")
			}
		default:
			t.Fatal("click not answered")
		}
		if m.modal != nil || m.quitting {
			t.Fatal("modal failed to close cleanly")
		}
		if m.transcript.SelectionText() != "" {
			t.Fatal("modal click selected transcript")
		}
	}
}

func TestModalMouseCancelAndResize(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)
	m, reply := openModal(t, m, []string{"Allow", "Deny"})
	m.View()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 25, Height: 7})
	m = next.(chatModel)
	x, y := modalCell(t, m, "[Cancel]")
	next, cmd := m.Update(press(x, y))
	m = feed(t, next.(chatModel), cmd)
	if final := <-reply; final.(ui.SelectorModel).Choice != ui.SelectorCanceled {
		t.Fatal("clicking cancel approved")
	}
	if m.quitting {
		t.Fatal("cancel quit chat")
	}
}

func TestModalMouseHoverAndOutsideClick(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen([]string{"transcript"})
	m, _ = openModal(t, m, []string{"Allow", "Deny"})
	x, y := modalCell(t, m, "Allow")
	before := m.View()
	next, cmd := m.Update(motion(x, y))
	m = next.(chatModel)
	if cmd != nil || m.modal == nil {
		t.Fatal("hover activated modal")
	}
	if ansi.Strip(before) != ansi.Strip(m.View()) {
		t.Fatal("hover moved controls")
	}
	next, cmd = m.Update(press(0, 0))
	m = next.(chatModel)
	if cmd != nil || m.modal == nil {
		t.Fatal("outside press activated modal")
	}
}

func TestHostedViewerMouseCloseAfterResize(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)
	reply := make(chan tea.Model, 1)
	next, _ := m.Update(ui.StreamModelMsg{Model: ui.TextViewerModel{Title: "Details"}, Reply: reply})
	m = next.(chatModel)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})
	m = next.(chatModel)
	x, y := modalCell(t, m, "[Cancel]")
	next, cmd := m.Update(press(x, y))
	m = feed(t, next.(chatModel), cmd)
	select {
	case final := <-reply:
		if _, ok := final.(ui.TextViewerModel); !ok {
			t.Fatal("wrapper escaped")
		}
	default:
		t.Fatal("viewer did not close")
	}
	if m.quitting {
		t.Fatal("viewer quit chat")
	}
}
