package main

import (
	"context"
	"testing"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/types"

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

// The configured delay must not postpone an interrupt: cancelling the turn while
// it is sleeping aborts the turn at once and skips the provider request.
// Regression: applyDelay used time.Sleep, so a ctrl+c in the delay window was
// only honoured after the whole delay elapsed.
func TestDelayHonoursTurnCancellation(t *testing.T) {
	compactionTestConfig(t, false, false, 1000)
	config.SetDelay(5)
	defer config.SetDelay(0)

	s := &ChatSession{Sink: &recordSink{}, RestrictedRoot: t.TempDir(),
		ChatHistory: []types.Message{{Role: "user", Content: "hi"}}}
	called := make(chan struct{}, 1)
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		called <- struct{}{}
		return &types.StreamResult{Content: "ok", FinishReason: "stop"}, nil
	})
	done := make(chan struct{})
	go func() {
		streamProviderLoop(s, streamOptions{}, config.Get(), p, func(context.Context, []types.Message, *ChatSession) (compactionResult, error) {
			return compactionResult{}, nil
		})
		close(done)
	}()

	// The delay starts right after the turn registers its cancel func.
	var cancel context.CancelFunc
	for deadline := time.Now().Add(3 * time.Second); cancel == nil && time.Now().Before(deadline); {
		s.GenerationCancelMu.Lock()
		cancel = s.GenerationCancel
		s.GenerationCancelMu.Unlock()
		if cancel == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if cancel == nil {
		t.Fatal("the turn never registered a cancel func")
	}

	start := time.Now()
	cancel()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("interrupt took %v: the delay was not cancelled", elapsed)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the turn never aborted")
	}
	select {
	case <-called:
		t.Fatal("the provider was called after the interrupt")
	default:
	}
	if s.IsGenerating.Load() != 0 {
		t.Fatal("the generating flag was not cleared")
	}
}
