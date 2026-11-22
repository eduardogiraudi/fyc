package main

import (
	"context"
	"time"

	"fuckyourcli/ui"
)

// Turn boundaries keep the alt-screen transcript and the conversation aligned:
// the history says how many turns exist, the transcript knows which blocks each
// one produced, and /undo needs both to drop exactly the rows the history loses.
// The rules:
//
//   - a boundary is recorded when a turn ends (CloseTurn) and only becomes a real
//     split when the next turn starts on top of it, so len(turnStarts) always
//     equals turns-1, whether the turns were produced live or replayed;
//   - every operation that rewrites the history re-aligns the boundaries
//     (RewindTurns, AlignTurns), or the next undo would retract the wrong rows.

// closeTurn records the end of the turn that just finished. It serves both the
// live path and the session replay (before each prompt and at the end): without a
// running program the boundary waits in the alt-screen seed.
func closeTurn(s *ChatSession) {
	if s != nil {
		if ts, ok := s.Sink.(ui.TurnSink); ok {
			ts.CloseTurn()
			return
		}
	}
	ui.PrintTurnBoundary()
}

// alignTurns tells the transcript that the history now holds turns turns: the
// boundaries of the turns that no longer exist are dropped.
func alignTurns(s *ChatSession, turns int) {
	if s == nil {
		return
	}
	if ts, ok := s.Sink.(ui.TurnSink); ok {
		ts.AlignTurns(turns)
	}
}

// rewindTurns retracts the transcript back to the start of turn. ok is false when
// the running frame keeps no transcript of its own (inline REPL, AG-UI), in which
// case the rows already printed cannot be taken back.
func rewindTurns(s *ChatSession, turn int) (rows int, ok bool) {
	if s == nil {
		return 0, false
	}
	ts, isTurnSink := s.Sink.(ui.TurnSink)
	if !isTurnSink {
		return 0, false
	}
	// The frame answers immediately; the timeout is what keeps a shutting-down
	// program from blocking the command forever.
	ctx, cancel := context.WithTimeout(s.runContext(), 5*time.Second)
	defer cancel()
	return ts.RewindTurns(ctx, turn)
}

// ── transcript side ──────────────────────────────────────────────────────────

// CloseTurn records where the turn that just finished ended. Nothing is split
// yet: a boundary only becomes real once another turn starts on top of it, which
// is what keeps the count equal to the history's turn count.
func (t *transcriptState) CloseTurn() {
	if t.turnClosed {
		t.turnStarts = append(t.turnStarts, t.turnEnd)
	}
	t.turnEnd = len(t.blocks)
	t.turnClosed = true
}

// RewindTurns drops the blocks of turn and of every later one, returning how many
// rows disappeared. The boundaries of the surviving turns are kept, so a second
// undo still cuts in the right place.
func (t *transcriptState) RewindTurns(turn int) int {
	// The first turn starts after the preamble (the banner), not at row zero.
	cut := min(t.preamble, len(t.blocks))
	if turn > 0 {
		if turn-1 >= len(t.turnStarts) {
			// No boundary for that turn: the rows it produced were already
			// evicted by the cap, so there is nothing to retract.
			return 0
		}
		cut = t.turnStarts[turn-1]
	}
	if cut > len(t.blocks) {
		cut = len(t.blocks)
	}
	removed := t.rowCount(cut, len(t.blocks))
	t.blocks = append([]transcriptBlock(nil), t.blocks[:cut]...)
	t.lines = t.lines[:0]
	for _, block := range t.blocks {
		t.lines = append(t.lines, block.rows...)
	}
	keep := turn - 1
	if keep < 0 {
		keep = 0
	}
	if keep > len(t.turnStarts) {
		keep = len(t.turnStarts)
	}
	t.turnStarts = append([]int(nil), t.turnStarts[:keep]...)
	t.turnEnd, t.turnClosed = cut, turn > 0 && cut > 0
	t.SelectionClear()
	t.resetHover()
	t.Follow()
	t.trim()
	return removed
}

// AlignTurns reports how many turns the history holds now: only the boundaries of
// the surviving (newest) turns are kept.
func (t *transcriptState) AlignTurns(turns int) {
	keep := turns - 1
	if keep < 0 {
		keep = 0
	}
	if keep >= len(t.turnStarts) {
		return
	}
	t.turnStarts = append([]int(nil), t.turnStarts[len(t.turnStarts)-keep:]...)
}

// rowCount is how many rows the blocks in [from, to) occupy.
func (t *transcriptState) rowCount(from, to int) int {
	n := 0
	for i := from; i < to && i < len(t.blocks); i++ {
		n += len(t.blocks[i].rows)
	}
	return n
}
