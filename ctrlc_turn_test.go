package main

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ctrl+c during a turn (for example while a tool is running) must abort the
// turn — killing the running command through the turn context — and never quit
// the program. Regression: IsGenerating is cleared after the provider stream,
// so the old check fell through to tea.Quit while a tool was executing.
func TestCtrlCWhileGeneratingCancelsInsteadOfQuitting(t *testing.T) {
	s := &ChatSession{}
	ctx := newGenerationContext(s)
	defer finishGeneration(s)
	m := newChatModel(s, nil, nil, nil)
	m.generating = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if updated.(chatModel).quitting {
		t.Fatal("ctrl+c during a turn must not quit the program")
	}
	if ctx.Err() == nil {
		t.Fatal("ctrl+c during a turn must cancel the turn context")
	}
}

// ctrl+c while the program is idle keeps the historical behaviour: it quits.
func TestCtrlCWhenIdleQuits(t *testing.T) {
	s := &ChatSession{}
	m := newChatModel(s, nil, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !updated.(chatModel).quitting {
		t.Fatal("ctrl+c while idle must quit")
	}
	if cmd == nil {
		t.Fatal("quitting must schedule a command (tea.Quit)")
	}
}

// The turn context lives for the whole turn and tools reach it via runContext,
// so cancelling it stops a tool that is running.
func TestTurnContextSpansTheWholeTurn(t *testing.T) {
	s := &ChatSession{}
	ctx := newGenerationContext(s)
	if s.IsGenerating.Load() != 1 {
		t.Fatal("newGenerationContext must set the generating flag")
	}
	if got := s.runContext(); got != ctx {
		t.Fatal("runContext must return the in-flight turn context")
	}

	s.GenerationCancelMu.Lock()
	cancel := s.GenerationCancel
	s.GenerationCancelMu.Unlock()
	if cancel == nil {
		t.Fatal("newGenerationContext must store a cancel func")
	}
	cancel()
	if ctx.Err() == nil {
		t.Fatal("cancelling the turn must abort the turn context")
	}

	finishGeneration(s)
	if s.IsGenerating.Load() != 0 {
		t.Fatal("finishGeneration must clear the generating flag")
	}
	if s.runContext().Err() != nil {
		t.Fatal("after the turn, runContext must be a live background context")
	}
}

// A turn runs inside the active run context, so a client disconnect (which
// cancels RunCtx) aborts the turn too.
func TestTurnContextDerivesFromRunContext(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	s := &ChatSession{RunCtx: runCtx}
	ctx := newGenerationContext(s)
	defer finishGeneration(s)

	cancelRun()
	if ctx.Err() == nil {
		t.Fatal("cancelling the run context must cancel the turn context")
	}
}
