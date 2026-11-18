package main

import (
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// openModal asks the chat for a selector, exactly like a permission prompt does
// from the turn goroutine.
func openModal(t *testing.T, m chatModel, options []string) (chatModel, chan tea.Model) {
	t.Helper()
	reply := make(chan tea.Model, 1)
	next, _ := m.Update(ui.StreamModelMsg{
		Model: ui.SelectorModel{Title: "Permission", Options: options, SuggestIdx: -1},
		Reply: reply,
	})
	return next.(chatModel), reply
}

// runMsgs executes a command chain and returns the last message it produced.
// Bubble Tea keeps sequences unexported, so a chain of commands is walked as a
// slice of commands, which covers both tea.Sequence and tea.Batch.
func runMsgs(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	cmds, ok := cmdList(msg)
	if !ok {
		return msg
	}
	var last tea.Msg
	for _, c := range cmds {
		if m := runMsgs(c); m != nil {
			last = m
		}
	}
	return last
}

// feed runs the command Update returned and hands its messages back to the chat,
// the way the program loop does. A tea.Quit reaching the loop would end the
// whole chat, so any that survives the modal wrapper fails the test.
func feed(t *testing.T, m chatModel, cmd tea.Cmd) chatModel {
	t.Helper()
	for i := 0; i < 16 && cmd != nil; i++ {
		msg := runMsgs(cmd)
		if msg == nil {
			return m
		}
		if _, quit := msg.(tea.QuitMsg); quit {
			t.Fatal("a hosted modal quit the whole chat")
		}
		next, nextCmd := m.Update(msg)
		m, cmd = next.(chatModel), nextCmd
	}
	return m
}

func cmdList(msg tea.Msg) ([]tea.Cmd, bool) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch, true
	}
	val := reflect.ValueOf(msg)
	if !val.IsValid() || val.Kind() != reflect.Slice || val.Type().Elem() != reflect.TypeOf(tea.Cmd(nil)) {
		return nil, false
	}
	out := make([]tea.Cmd, 0, val.Len())
	for i := 0; i < val.Len(); i++ {
		out = append(out, val.Index(i).Interface().(tea.Cmd))
	}
	return out, true
}

// A selector asked for while the chat owns the terminal is drawn inside the
// frame: the input row is replaced, the frame is still exactly as tall as the
// terminal and nothing is printed into the scrollback.
func TestModalIsDrawnInTheFrame(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen([]string{"banner"})
	m, _ = openModal(t, m, []string{"Allow", "Deny", "Allow always"})

	rows := strings.Split(m.View(), "\n")
	if len(rows) != m.height {
		t.Fatalf("frame has %d rows, want %d", len(rows), m.height)
	}
	view := strings.Join(rows, "\n")
	for _, want := range []string{"Permission", "Allow", "Deny", "Allow always"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the modal is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(rows[len(rows)-1], "❯") {
		t.Fatalf("the input is still drawn under the modal: %q", rows[len(rows)-1])
	}
}

// The answer travels back through the reply channel and the frame returns to the
// input without the chat quitting.
func TestModalAnswersThroughReplyChannel(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)
	m, reply := openModal(t, m, []string{"Allow", "Deny"})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(chatModel)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = feed(t, next.(chatModel), cmd)

	select {
	case final := <-reply:
		sel := final.(ui.SelectorModel)
		if sel.Choice != 1 {
			t.Fatalf("answered %d, want the Deny row", sel.Choice)
		}
	default:
		t.Fatal("the modal never answered")
	}
	if m.modal != nil {
		t.Fatal("the modal is still open after answering")
	}
	if rows := strings.Split(m.View(), "\n"); !strings.Contains(rows[len(rows)-1], "❯") {
		t.Fatalf("the input did not come back: %q", rows[len(rows)-1])
	}
}

// While the modal is open it owns the keyboard: ctrl+c dismisses the prompt
// (a denial) instead of interrupting the turn or quitting, and the keys never
// reach the chat input.
func TestModalOwnsTheKeyboard(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)
	m, reply := openModal(t, m, []string{"Allow", "Deny"})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = next.(chatModel)
	if got := m.input.Value(); got != "" {
		t.Fatalf("typing while a modal is open reached the input: %q", got)
	}
	if m.generating {
		t.Fatal("a key started a turn while a modal was open")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = feed(t, next.(chatModel), cmd)
	if m.quitting {
		t.Fatal("ctrl+c on a modal quit the chat")
	}
	select {
	case final := <-reply:
		if final.(ui.SelectorModel).Choice != ui.SelectorCanceled {
			t.Fatal("dismissing the modal must deny, not pick a row")
		}
	default:
		t.Fatal("ctrl+c did not answer the modal")
	}
}

// A typed confirmation is hosted the same way and reads back what was typed.
func TestModalTextPrompt(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)

	reply := make(chan tea.Model, 1)
	next, _ := m.Update(ui.StreamModelMsg{Model: ui.NewTextPrompt("Type yes to continue"), Reply: reply})
	m = next.(chatModel)
	if !strings.Contains(m.View(), "Type yes to continue") {
		t.Fatalf("the text prompt is not drawn:\n%s", m.View())
	}
	for _, r := range "yes" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(chatModel)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = feed(t, next.(chatModel), cmd)

	select {
	case final := <-reply:
		if got := ui.TextPromptValue(final); got != "yes" {
			t.Fatalf("answered %q, want %q", got, "yes")
		}
	default:
		t.Fatal("the text prompt never answered")
	}
}
