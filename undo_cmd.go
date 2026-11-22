package main

import (
	"fmt"
	"strconv"
	"strings"

	"fuckyourcli/types"
	"fuckyourcli/ui"
)

// /undo rewinds the whole turn: the conversation, the transcript, the files the
// turn changed and the anchor cache the model keeps between edits. The menu lists
// the turns newest first, so the usual "undo the last exchange" is one key.
func handleUndoCommand(s *ChatSession, input string) {
	sink := s.commandSink()
	turns := chatTurns(s.ChatHistory)
	if len(turns) == 0 {
		sinkLine(sink, ui.InfoStyle.Render("Nothing to undo: this conversation has no turns yet."))
		return
	}

	index, ok := undoChoice(s, sink, strings.TrimSpace(strings.TrimPrefix(input, "/undo")), turns)
	if !ok {
		return
	}

	// A turn is destructive: it discards messages and files. Say exactly what.
	title := fmt.Sprintf("Rewind to before turn %d? %s", index+1, undoScope(s, index, turns))
	if sink.SelectOption(title, []string{"No, keep the conversation", "Yes, rewind"}) != 1 {
		sinkLine(sink, ui.InfoStyle.Render("Nothing changed."))
		return
	}

	ask := undoConflictAsker(s, index)
	outcome, err := s.rewindToTurn(index, ask)
	if err != nil {
		sinkLine(sink, ui.ErrorStyle.Render("✗ Cannot rewind: "+err.Error()))
		return
	}
	outcome.TranscriptRows, outcome.TranscriptOK = rewindTurns(s, index)
	writeUndoRecap(sink, outcome)
}

// undoChoice resolves which turn to rewind to: the argument (/undo 3) or the
// menu. ok is false when the user dismissed it, which never picks anything.
func undoChoice(s *ChatSession, sink types.OutputSink, arg string, turns []chatTurn) (int, bool) {
	if arg != "" {
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(turns) {
			sinkLine(sink, ui.InfoStyle.Render(fmt.Sprintf("Usage: /undo [1-%d]", len(turns))))
			return 0, false
		}
		return n - 1, true
	}

	labels := make([]string, 0, len(turns)+1)
	order := make([]int, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		labels = append(labels, turnLabel(i, turns[i], s.journal.turnChangedFiles(i)))
		order = append(order, i)
	}
	labels = append(labels, "Cancel")
	pick := sink.SelectOption("Rewind to before which turn?", labels)
	if pick < 0 || pick >= len(order) {
		return 0, false
	}
	return order[pick], true
}

// undoScope describes what a rewind to turn index discards, for the confirmation.
func undoScope(s *ChatSession, index int, turns []chatTurn) string {
	discarded, later := 0, len(turns)-index-1
	for i := index; i < len(turns); i++ {
		discarded += turns[i].messages()
	}
	scope := fmt.Sprintf("%d message(s)", discarded)
	if later > 0 {
		scope = fmt.Sprintf("this turn and %d after it (%s)", later, scope)
	}
	if s.journal != nil {
		files := 0
		for i := index; i < len(turns); i++ {
			files += s.journal.turnChangedFiles(i)
		}
		if files > 0 {
			scope += fmt.Sprintf(", %d file(s) reverted", files)
		}
	}
	return scope
}

// undoConflictAsker builds the callback that decides, per file, what to do with a
// file the user changed after the turn. Nothing is restored over the user's own
// edits without asking.
func undoConflictAsker(s *ChatSession, index int) func(string) rewindDecision {
	if s.journal == nil {
		return nil
	}
	paths := s.journal.conflicts(index)
	if len(paths) == 0 {
		return nil
	}
	sink := s.commandSink()
	decisions := map[string]rewindDecision{}
	// A single file is asked about directly; a bulk answer is only offered when
	// there is more than one, so nothing is ever restored over the user's edits
	// without a question.
	bulk := rewindDecision(-1)
	if len(paths) > 1 {
		choice := sink.SelectOption(fmt.Sprintf("%d files changed after turn %d", len(paths), index+1),
			[]string{"Ask me for each", "Restore all of them", "Keep all of them"})
		switch choice {
		case 1:
			bulk = rewindRestore
		case 2:
			bulk = rewindKeep
		default:
			bulk = -1 // ask per file
		}
	}
	return func(path string) rewindDecision {
		if d, ok := decisions[path]; ok {
			return d
		}
		if bulk >= 0 {
			return bulk
		}
		choice := sink.SelectOption("Changed after the turn: "+path,
			[]string{"Keep my current version", "Restore the version before the turn"})
		d := rewindKeep
		if choice == 1 {
			d = rewindRestore
		}
		decisions[path] = d
		return d
	}
}

// writeUndoRecap states what actually happened, including what could not be
// reverted: the scrollback (inline REPL), the files outside the snapshot and the
// versions the user chose to keep.
func writeUndoRecap(sink types.OutputSink, out undoOutcome) {
	sinkLine(sink, ui.OkStyle.Render(fmt.Sprintf("✓ Rewound to before turn %d · %d message(s) discarded, %d left",
		out.Turn+1, out.DiscardedMsgs, out.KeptMsgs)))
	if out.FirstTurnCleared {
		sinkLine(sink, ui.InfoStyle.Render("  the conversation is empty: a new prompt starts a fresh chat in this session"))
	}

	switch {
	case !out.TranscriptOK:
		sinkLine(sink, ui.TipStyle.Render("  the terminal scrollback keeps the discarded turns: only the alt-screen transcript can be retracted"))
	case out.TranscriptRows > 0:
		sinkLine(sink, ui.InfoStyle.Render(fmt.Sprintf("  %d row(s) removed from the transcript", out.TranscriptRows)))
	}

	if !out.FilesAvailable {
		sinkLine(sink, ui.TipStyle.Render("  no workspace snapshot for this session: files were left as they are"))
		return
	}
	if n := len(out.Files.Restored); n > 0 {
		sinkLine(sink, ui.OkStyle.Render(fmt.Sprintf("  %d file(s) restored", n))+" "+ui.DimStyle.Render(undoPathList(out.Files.Restored)))
	}
	if n := len(out.Files.Kept); n > 0 {
		sinkLine(sink, ui.InfoStyle.Render(fmt.Sprintf("  %d file(s) kept as you asked", n))+" "+ui.DimStyle.Render(undoPathList(out.Files.Kept)))
	}
	if n := len(out.Files.Skipped); n > 0 {
		sinkLine(sink, ui.TipStyle.Render(fmt.Sprintf("  %d file(s) changed but not snapshotted (size or budget limit)", n))+" "+ui.DimStyle.Render(undoPathList(out.Files.Skipped)))
	}
	if out.FilesIncomplete {
		sinkLine(sink, ui.TipStyle.Render("  the workspace snapshot is partial: a file above the size limit or above the file budget was not captured"))
	}
	if n := len(out.Files.Failed); n > 0 {
		sinkLine(sink, ui.ErrorStyle.Render(fmt.Sprintf("  %d file(s) could not be restored", n))+" "+ui.DimStyle.Render(undoPathList(out.Files.Failed)))
	}
}

// undoPathList renders up to a few paths for the recap.
func undoPathList(paths []string) string {
	const max = 4
	sorted := sortedPaths(paths)
	if len(sorted) <= max {
		return strings.Join(sorted, ", ")
	}
	return strings.Join(sorted[:max], ", ") + fmt.Sprintf(", +%d more", len(sorted)-max)
}
