package main

import (
	"fmt"
	"strings"

	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// A turn is the unit /undo works on: the prompt the user sent, the answer, the
// tools it ran and their results — everything up to the next prompt. Rewinding to
// turn i discards turn i and every later one, so the conversation, the transcript,
// the files and the anchor cache all go back to the moment just before that
// prompt was sent.

// chatTurn is one turn of the history, as a message range.
type chatTurn struct {
	// Start is the first message the turn owns: its prompt, plus the mode
	// instructions injected right before it.
	Start int
	// End is the first message after the turn.
	End int
	// PromptIndex is the prompt's index, or -1 for the head of a conversation
	// whose first prompt was summarized away by compaction.
	PromptIndex int
	// Prompt is the text the user sent ("" for a headless turn).
	Prompt string
}

func (t chatTurn) messages() int { return t.End - t.Start }

// chatTurns partitions the conversation into turns. System/developer messages and
// the compaction summary are not turns; the orphan messages a compaction leaves
// before the first surviving prompt are one (so they can be rewound too).
func chatTurns(history []types.Message) []chatTurn {
	first := 0
	for first < len(history) && (history[first].Role == "system" || history[first].Role == "developer") {
		first++
	}
	var turns []chatTurn
	bound := first
	for i := first; i < len(history); i++ {
		if !isTurnPrompt(history[i]) {
			continue
		}
		start := i
		for start > bound && isModeInstruction(history[start-1]) {
			start--
		}
		if len(turns) == 0 {
			if start > first {
				turns = append(turns, chatTurn{Start: first, End: start, PromptIndex: -1})
			}
		} else {
			turns[len(turns)-1].End = start
		}
		turns = append(turns, chatTurn{Start: start, End: len(history), PromptIndex: i, Prompt: history[i].Content})
		bound = start
	}
	if len(turns) == 0 && first < len(history) {
		turns = append(turns, chatTurn{Start: first, End: len(history), PromptIndex: -1})
	}
	return turns
}

// isTurnPrompt reports whether m starts a turn: a real user prompt, not a mode
// instruction, a compaction summary or an empty message some transport left.
func isTurnPrompt(m types.Message) bool {
	if m.Role != "user" || isModeInstruction(m) {
		return false
	}
	if strings.HasPrefix(m.Content, compactionSummaryPrefix) {
		return false
	}
	return strings.TrimSpace(m.Content) != ""
}

// turnLabel renders one row of the /undo menu.
func turnLabel(index int, t chatTurn, files int) string {
	text := strings.Join(strings.Fields(t.Prompt), " ")
	if text == "" {
		text = "(context kept from before the compaction)"
	}
	if runes := []rune(text); len(runes) > 48 {
		text = strings.TrimSpace(string(runes[:48])) + "…"
	}
	parts := []string{fmt.Sprintf("%d · %s", index+1, text), fmt.Sprintf("%d msgs", t.messages())}
	if files > 0 {
		parts = append(parts, fmt.Sprintf("%d files", files))
	}
	return strings.Join(parts, " · ")
}

// beginTurn freezes the state of the turn about to start: the anchor cache and
// the workspace. Everything a turn changes is then recoverable from here.
func (s *ChatSession) beginTurn() {
	turn := len(chatTurns(s.ChatHistory))
	s.turnStarted = true
	for len(s.anchorTurns) <= turn {
		s.anchorTurns = append(s.anchorTurns, nil)
	}
	s.anchorTurns[turn] = tools.SnapshotAnchors()
	if j := s.ensureUndoJournal(); j != nil {
		j.BeginTurn(turn)
		j.Sync(s.RestrictedRoot, s.GlobalIgnore)
	}
}

// ensureUndoJournal attaches the journal of the active session, opening (or
// reloading) the one on disk so a resumed session can still revert files.
func (s *ChatSession) ensureUndoJournal() *undoJournal {
	if s.Headless {
		return nil
	}
	if s.journal != nil && s.journal.session == s.SessionID && s.journal.root == s.RestrictedRoot {
		return s.journal
	}
	if err := ensureSessionIdentity(s); err != nil {
		return nil
	}
	s.journal = newUndoJournal(s.RestrictedRoot, s.SessionID, undoJournalDir(getChatHistoryDir(), s.SessionID))
	// The tool dispatcher feeds the journal: that is how a turn's changes reach it
	// between the turn's own Sync calls.
	tools.SetTurnJournal(s.journal)
	return s.journal
}

// saveUndoJournal persists the turn records next to the session, so the files of
// a previous process can still be reverted.
func (s *ChatSession) saveUndoJournal() {
	if s.journal == nil {
		return
	}
	if err := s.journal.Save(); err != nil {
		if s.Sink != nil {
			s.Sink.WriteText("Warning: could not save the undo journal: " + err.Error() + "\n")
		}
	}
}

// resetTurnSnapshots drops the per-turn state of a conversation that is being
// replaced (a new chat, a different session, /clear).
func (s *ChatSession) resetTurnSnapshots() {
	s.anchorTurns = nil
	s.journal = nil
	tools.SetTurnJournal(nil)
}

// undoOutcome reports what a rewind changed, so the command can say it.
type undoOutcome struct {
	Turn             int
	DiscardedTurns   int
	DiscardedMsgs    int
	KeptMsgs         int
	FilesAvailable   bool
	FilesIncomplete  bool
	Files            undoFileReport
	TranscriptRows   int
	TranscriptOK     bool
	FirstTurnCleared bool
}

// rewindToTurn discards turn index and everything after it. Files are reverted
// first (in reverse turn order, so the pre-image of the oldest turn wins), then
// the anchor cache is put back to the boundary, then the history is truncated:
// the disk and the model's view of it must agree before the conversation shrinks.
// ask is consulted for a file that changed after the turn it is being reverted
// from, and may be nil (keep such files).
func (s *ChatSession) rewindToTurn(index int, ask func(string) rewindDecision) (undoOutcome, error) {
	turns := chatTurns(s.ChatHistory)
	outcome := undoOutcome{Turn: index}
	if index < 0 || index >= len(turns) {
		return outcome, fmt.Errorf("no turn %d", index+1)
	}
	target := turns[index]
	outcome.DiscardedTurns = len(turns) - index
	for i := index; i < len(turns); i++ {
		outcome.DiscardedMsgs += turns[i].messages()
	}
	outcome.KeptMsgs = target.Start

	// Files first: the anchor cache restored below then matches the disk.
	if s.journal != nil {
		outcome.FilesAvailable = true
		outcome.FilesIncomplete = s.journal.incomplete()
		for i := len(turns) - 1; i >= index; i-- {
			outcome.Files.merge(s.journal.revertTurn(i, ask))
		}
	}

	// Anchors: the exact cache of the boundary, minus the files whose disk
	// content could not be restored (their anchors would describe a file that no
	// longer exists, so the model has to read them again).
	var snapshot *tools.AnchorSnapshot
	if index < len(s.anchorTurns) {
		snapshot = s.anchorTurns[index]
	}
	stale := append(append(append([]string{}, outcome.Files.Kept...), outcome.Files.Failed...), outcome.Files.Skipped...)
	tools.RestoreAnchors(snapshot, stale...)
	if len(s.anchorTurns) > index {
		s.anchorTurns = s.anchorTurns[:index]
	}

	s.ChatHistory = s.ChatHistory[:target.Start]
	s.retryRequest = nil
	s.turnModeOverride = nil
	s.lastProviderTokens, s.lastProviderMessages = 0, 0
	s.SessionTitle = sessionTitle(s.ChatHistory)
	s.refreshModeInjection()
	if index == 0 {
		// Rewinding before the very first prompt leaves a fresh conversation: the
		// same cleanup /clear does, but the session keeps its identity.
		outcome.FirstTurnCleared = true
		s.CurrentMode = nil
		s.persistentModeInjected = ""
	}
	if s.journal != nil {
		s.journal.truncate(index)
	}
	updateSystemPrompt(s)
	s.autosave()
	return outcome, nil
}

// realignTurnState re-indexes the per-turn state after the history was rewritten
// (compaction summarized the older turns, /clear emptied it): only the last
// `turns` records of the anchor cache, of the file journal and of the transcript
// can still describe a surviving turn.
func (s *ChatSession) realignTurnState(turns int) {
	if turns < 0 {
		turns = 0
	}
	if turns == 0 {
		s.anchorTurns = nil
	} else if len(s.anchorTurns) > turns {
		s.anchorTurns = append([]*tools.AnchorSnapshot(nil), s.anchorTurns[len(s.anchorTurns)-turns:]...)
	}
	if s.journal != nil {
		s.journal.keepLastTurns(turns)
	}
	alignTurns(s, turns)
}

// refreshModeInjection keeps the "already injected" flag honest after a rewind:
// the flag stays only when the mode's instruction message survived, otherwise the
// next turn injects it again instead of running without its rules.
func (s *ChatSession) refreshModeInjection() {
	if s.CurrentMode == nil {
		s.persistentModeInjected = ""
		return
	}
	want := "[INSTRUCTIONS]: " + s.CurrentMode.GetInstructions(s.subagentsEnabled())
	for _, m := range s.ChatHistory {
		if isModeInstruction(m) && m.Content == want {
			return
		}
	}
	s.persistentModeInjected = ""
}
