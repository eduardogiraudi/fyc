package ui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// StreamModelMsg asks the chat program to host a whole Bubble Tea model inside
// its own frame instead of taking over the terminal. It is used in alt-screen
// mode, where releasing the terminal would drop the user out of the full-screen
// TUI with no scrollback to come back to. The chat answers exactly once on
// Reply, with the model as the user left it.
type StreamModelMsg struct {
	Model tea.Model
	Reply chan tea.Model
}

// inAppPrompt routes the interactive helpers into the long-lived chat program.
// It stays disabled everywhere else: outside a running chat there is no frame to
// draw into, and the inline REPL prefers the nested program, whose output
// scrolls with the conversation.
var inAppPrompt struct {
	mu   sync.Mutex
	prog *tea.Program
}

// EnableInAppPrompts makes the interactive helpers host their model inside the
// chat program instead of running a nested one.
func EnableInAppPrompts() {
	promptCoordinator.Lock()
	p := promptCoordinator.program
	promptCoordinator.Unlock()

	inAppPrompt.mu.Lock()
	inAppPrompt.prog = p
	inAppPrompt.mu.Unlock()
}

// DisableInAppPrompts restores the nested-program behaviour.
func DisableInAppPrompts() {
	inAppPrompt.mu.Lock()
	inAppPrompt.prog = nil
	inAppPrompt.mu.Unlock()
}

// runInApp hosts model inside the running chat program and blocks until the user
// finishes it. ok is false when no in-app host is available, so the caller falls
// back to its own program.
//
// It must be called from the goroutine that drives the turn — never from
// inside the program's Update, which is the goroutine that draws it.
func runInApp(model tea.Model) (tea.Model, bool) {
	inAppPrompt.mu.Lock()
	p := inAppPrompt.prog
	inAppPrompt.mu.Unlock()
	if p == nil {
		return nil, false
	}
	reply := make(chan tea.Model, 1)
	p.Send(StreamModelMsg{Model: model, Reply: reply})
	return <-reply, true
}
