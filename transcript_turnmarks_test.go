package main

import (
	"strings"
	"testing"

	"fuckyourcli/ui"
)

// turnTranscript builds a transcript with three turns of two blocks each.
func turnTranscript() *transcriptState {
	tr := &transcriptState{}
	for i := 1; i <= 3; i++ {
		tr.Append(strings.Repeat("prompt ", 1) + string(rune('0'+i)))
		tr.Append("answer " + string(rune('0'+i)))
		tr.CloseTurn()
	}
	return tr
}

func TestCloseTurnKeepsTheBoundaryCountAtTurnsMinusOne(t *testing.T) {
	tr := turnTranscript()
	if len(tr.turnStarts) != 2 {
		t.Fatalf("boundaries = %v, want one per turn after the first", tr.turnStarts)
	}
	if tr.turnStarts[0] != 2 || tr.turnStarts[1] != 4 {
		t.Fatalf("boundaries = %v, want [2 4]", tr.turnStarts)
	}
}

func TestRewindTurnsRetractsTheRowsOfTheTurn(t *testing.T) {
	tr := turnTranscript()
	if removed := tr.RewindTurns(1); removed != 4 {
		t.Fatalf("removed %d rows, want the second and third turn", removed)
	}
	got := strings.Join(tr.lines, "|")
	if got != "prompt 1|answer 1" {
		t.Fatalf("lines = %q", got)
	}
	if len(tr.turnStarts) != 0 {
		t.Fatalf("boundaries = %v, want none left", tr.turnStarts)
	}
	// Rewinding the first turn removes what is left.
	if removed := tr.RewindTurns(0); removed != 2 {
		t.Fatalf("removed %d rows", removed)
	}
	if len(tr.lines) != 0 || len(tr.blocks) != 0 {
		t.Fatalf("transcript not empty: %v", tr.lines)
	}
}

func TestRewindTurnsOfASecondTimeCutsInTheRightPlace(t *testing.T) {
	tr := turnTranscript()
	tr.RewindTurns(2)
	if removed := tr.RewindTurns(1); removed != 2 {
		t.Fatalf("removed %d rows, want the second turn only", removed)
	}
	if got := strings.Join(tr.lines, "|"); got != "prompt 1|answer 1" {
		t.Fatalf("lines = %q", got)
	}
}

func TestAlignTurnsKeepsTheNewestBoundaries(t *testing.T) {
	tr := turnTranscript()
	tr.AlignTurns(2)
	if len(tr.turnStarts) != 1 || tr.turnStarts[0] != 4 {
		t.Fatalf("boundaries = %v, want the last one", tr.turnStarts)
	}
	tr.AlignTurns(0)
	if len(tr.turnStarts) != 0 {
		t.Fatalf("boundaries = %v, want none", tr.turnStarts)
	}
}

func TestTrimRebasesTheTurnBoundaries(t *testing.T) {
	tr := &transcriptState{
		lines:      make([]string, 3+transcriptMaxLines+1),
		turnStarts: []int{0, 3, transcriptMaxLines + 1},
		turnEnd:    transcriptMaxLines + 1,
		turnClosed: true,
	}
	tr.blocks = []transcriptBlock{
		{rows: make([]string, 3)},
		{rows: make([]string, transcriptMaxLines)},
		{rows: make([]string, 1)},
	}
	tr.trim()
	// The first block and one row of the second are evicted: the boundary that
	// sat in the evicted block collapses onto the first surviving row.
	if len(tr.turnStarts) != 3 || tr.turnStarts[0] != 0 || tr.turnStarts[1] != 2 || tr.turnStarts[2] != transcriptMaxLines {
		t.Fatalf("boundaries = %v", tr.turnStarts)
	}
}

func TestCloneCopiesTheTurnBoundaries(t *testing.T) {
	tr := turnTranscript()
	clone := tr.Clone()
	clone.RewindTurns(1)
	if len(tr.turnStarts) != 2 {
		t.Fatalf("the clone rewrote the original boundaries: %v", tr.turnStarts)
	}
	if got := strings.Join(tr.lines, "|"); !strings.Contains(got, "answer 3") {
		t.Fatalf("the clone rewrote the original rows: %q", got)
	}
}

// The model has to answer the rewind request the command blocks on, and with a
// session that keeps no transcript the answer must be "no".
func TestChatModelAnswersTheTransactionRewind(t *testing.T) {
	m := newChatModel(&ChatSession{}, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)
	m.printCmd("prompt 1")
	m.printCmd("answer 1")
	next, _ := m.Update(generationDoneMsg{startedTurn: true})
	m = next.(chatModel)
	m.printCmd("prompt 2")
	m.printCmd("answer 2")
	next, _ = m.Update(generationDoneMsg{startedTurn: true})
	m = next.(chatModel)

	reply := make(chan int, 1)
	next, _ = m.Update(ui.StreamTurnRewindMsg{Turn: 1, Reply: reply})
	m = next.(chatModel)
	if rows := <-reply; rows != 2 {
		t.Fatalf("removed %d rows, want the second turn", rows)
	}
	if frame := m.View(); strings.Contains(frame, "answer 2") || !strings.Contains(frame, "answer 1") {
		t.Fatalf("frame after the rewind:\n%s", frame)
	}

	plain := newChatModel(&ChatSession{}, nil, nil, nil)
	reply = make(chan int, 1)
	if _, _ = plain.Update(ui.StreamTurnRewindMsg{Turn: 0, Reply: reply}); <-reply != -1 {
		t.Fatal("the inline REPL claimed to retract rows it never kept")
	}
}

// A slash command prints but opens no turn: its rows belong to the turn before.
func TestCommandOutputDoesNotCloseATurn(t *testing.T) {
	m := newChatModel(&ChatSession{}, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen(nil)
	m.printCmd("prompt 1")
	m.printCmd("answer 1")
	next, _ := m.Update(generationDoneMsg{startedTurn: true})
	m = next.(chatModel)
	m.printCmd("/help")
	m.printCmd("commands")
	next, _ = m.Update(generationDoneMsg{startedTurn: false})
	m = next.(chatModel)

	if len(m.transcript.turnStarts) != 0 {
		t.Fatalf("a command closed a turn boundary: %v", m.transcript.turnStarts)
	}
	reply := make(chan int, 1)
	m.Update(ui.StreamTurnRewindMsg{Turn: 0, Reply: reply})
	if rows := <-reply; rows != 4 {
		t.Fatalf("removed %d rows, want the turn and the command output", rows)
	}
}

// The startup banner belongs to no turn: rewinding to the first one keeps it.
func TestRewindToTheFirstTurnKeepsThePreamble(t *testing.T) {
	tr := &transcriptState{}
	tr.Append("banner")
	tr.preamble = len(tr.blocks)
	for i := 1; i <= 2; i++ {
		tr.Append("prompt " + string(rune('0'+i)))
		tr.Append("answer " + string(rune('0'+i)))
		tr.CloseTurn()
	}
	if removed := tr.RewindTurns(0); removed != 4 {
		t.Fatalf("removed %d rows, want both turns and not the banner", removed)
	}
	if got := strings.Join(tr.lines, "|"); got != "banner" {
		t.Fatalf("lines = %q", got)
	}
	// The next turn starts clean: its end must not leave a phantom boundary.
	tr.Append("prompt 1")
	tr.CloseTurn()
	if len(tr.turnStarts) != 0 {
		t.Fatalf("boundaries = %v, want none after the first new turn", tr.turnStarts)
	}
}

func TestChatModelKeepsTheBannerOnRewindToTheFirstTurn(t *testing.T) {
	m := newChatModel(&ChatSession{}, nil, nil, nil)
	m.width, m.height = 60, 20
	m.enableAltScreen([]string{""}, func(int) string { return "BANNER" })
	m.printCmd("prompt 1")
	m.printCmd("answer 1")
	next, _ := m.Update(generationDoneMsg{startedTurn: true})
	m = next.(chatModel)

	reply := make(chan int, 1)
	next, _ = m.Update(ui.StreamTurnRewindMsg{Turn: 0, Reply: reply})
	m = next.(chatModel)
	if rows := <-reply; rows != 2 {
		t.Fatalf("removed %d rows, want the turn only", rows)
	}
	if frame := m.View(); !strings.Contains(frame, "BANNER") || strings.Contains(frame, "answer 1") {
		t.Fatalf("frame after the rewind:\n%s", frame)
	}
}
