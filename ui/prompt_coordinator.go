package ui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

var promptCoordinator = struct {
	sync.Mutex
	program *tea.Program
	depth   int
	paused  bool
}{}

// SetPromptProgram registers the long-lived chat Bubble Tea program so nested
// prompts can pause it and exclusively take over stdin/stdout.
func SetPromptProgram(p *tea.Program) {
	promptCoordinator.Lock()
	promptCoordinator.program = p
	promptCoordinator.Unlock()
}

// PauseForExternalPrompt flushes the chat UI and releases the terminal before a
// nested prompt starts. Calls are serialized with ResumeExternalPrompt.
func PauseForExternalPrompt() {
	promptCoordinator.Lock()
	if promptCoordinator.depth > 0 {
		promptCoordinator.depth++
		promptCoordinator.Unlock()
		return
	}
	promptCoordinator.depth = 1
	p := promptCoordinator.program
	if p == nil {
		promptCoordinator.Unlock()
		return
	}
	ready := make(chan struct{})
	p.Send(StreamPauseMsg{Ready: ready})
	<-ready
	_ = p.ReleaseTerminal()
	promptCoordinator.paused = true
	promptCoordinator.Unlock()
}

// ResumeExternalPrompt restores the long-lived chat UI after a nested prompt.
func ResumeExternalPrompt() {
	promptCoordinator.Lock()
	if promptCoordinator.depth == 0 {
		promptCoordinator.Unlock()
		return
	}
	promptCoordinator.depth--
	if promptCoordinator.depth > 0 {
		promptCoordinator.Unlock()
		return
	}
	p := promptCoordinator.program
	wasPaused := promptCoordinator.paused
	promptCoordinator.paused = false
	promptCoordinator.Unlock()

	if p != nil && wasPaused {
		_ = p.RestoreTerminal()
		p.Send(StreamResumeMsg{})
	}
}

func withExternalPrompt[T any](fn func() T) T {
	PauseForExternalPrompt()
	defer ResumeExternalPrompt()
	return fn()
}
