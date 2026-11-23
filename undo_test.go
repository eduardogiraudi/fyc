package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

func undoHistory() []types.Message {
	return []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "[INSTRUCTIONS]: plan mode"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "one"},
		{Role: "tool", Name: "write", Content: "ok"},
		{Role: "user", Content: "[INSTRUCTIONS]: debug mode"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "two", ToolCalls: []types.ToolCall{{ID: "c1", Function: types.ToolCallFunction{Name: "write"}}}},
		{Role: "assistant", Content: "three"},
	}
}

func TestChatTurnsPartitionTheHistory(t *testing.T) {
	turns := chatTurns(undoHistory())
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].Start != 1 || turns[0].End != 5 || turns[0].Prompt != "first" || turns[0].PromptIndex != 2 {
		t.Fatalf("first turn = %+v", turns[0])
	}
	// The mode instructions belong to the turn they precede, or rewinding would
	// leave them dangling in the history.
	if turns[1].Start != 5 || turns[1].End != 9 || turns[1].Prompt != "second" {
		t.Fatalf("second turn = %+v", turns[1])
	}
	if turns[0].messages() != 4 || turns[1].messages() != 4 {
		t.Fatalf("message counts: %d %d", turns[0].messages(), turns[1].messages())
	}
}

func TestChatTurnsSkipsCompactionSummary(t *testing.T) {
	history := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: compactionSummaryPrefix + "summary"},
		{Role: "assistant", Content: compactionAckMessage},
		{Role: "user", Content: "after"},
		{Role: "assistant", Content: "ok"},
	}
	turns := chatTurns(history)
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2 (the orphan head plus the prompt): %+v", len(turns), turns)
	}
	if turns[0].PromptIndex != -1 || turns[0].Start != 1 || turns[0].End != 3 {
		t.Fatalf("head turn = %+v", turns[0])
	}
	if turns[1].Prompt != "after" {
		t.Fatalf("second turn = %+v", turns[1])
	}
}

func TestRewindToTurnTruncatesAndResetsState(t *testing.T) {
	testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/work", ChatHistory: undoHistory()}
	s.retryRequest = &retryRequest{}
	s.persistentModeInjected = "Debug"
	s.SessionTitle = "first"

	out, err := s.rewindToTurn(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ChatHistory) != 5 || s.ChatHistory[len(s.ChatHistory)-1].Role != "tool" {
		t.Fatalf("history not truncated at the second turn: %+v", s.ChatHistory)
	}
	if s.retryRequest != nil {
		t.Fatal("the retry checkpoint survived a rewind")
	}
	if s.SessionTitle != "first" {
		t.Fatalf("title = %q", s.SessionTitle)
	}
	if out.DiscardedTurns != 1 || out.DiscardedMsgs != 4 || out.KeptMsgs != 5 {
		t.Fatalf("outcome = %+v", out)
	}
	// The plan mode was injected in the surviving turn, so it must not be
	// re-injected; the debug mode was not selected through /mode, so its flag is
	// recomputed from the history and cleared.
	if s.persistentModeInjected != "" {
		t.Fatalf("mode injection flag = %q", s.persistentModeInjected)
	}
}

func TestRewindToFirstTurnClearsTheConversation(t *testing.T) {
	testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/work", ChatHistory: undoHistory()}
	s.CurrentMode = nil
	out, err := s.rewindToTurn(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ChatHistory) != 1 || s.ChatHistory[0].Role != "system" {
		t.Fatalf("history = %+v", s.ChatHistory)
	}
	if !out.FirstTurnCleared {
		t.Fatal("the first turn was not reported as cleared")
	}
}

// journalSession returns a session with a journal on a throw-away workspace.
func journalSession(t *testing.T, history []types.Message) (*ChatSession, string) {
	t.Helper()
	testWorkspace(t)
	root := t.TempDir()
	s := &ChatSession{RestrictedRoot: root, ChatHistory: history, SessionID: "20250101-000000-abcdef0123456789"}
	s.journal = newUndoJournal(root, s.SessionID, undoJournalDir(getChatHistoryDir(), s.SessionID))
	return s, root
}

func TestRewindRevertsFilesOfTheDiscardedTurns(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Turn 1 begins with the file as it is now.
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)
	// The turn rewrites it (what any tool, sh included, would do) and then the
	// dispatcher collects the diff.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("ruined\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, nil)

	out, err := s.rewindToTurn(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original\n" {
		t.Fatalf("file = %q, want the pre-turn content", data)
	}
	if len(out.Files.Restored) != 1 || out.Files.Restored[0] != "a.txt" {
		t.Fatalf("reverted files = %+v", out.Files)
	}
	if !out.FilesAvailable {
		t.Fatal("the journal was not reported as available")
	}
}

func TestRewindKeepsAFileEditedAfterTheTurn(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)
	if err := os.WriteFile(path, []byte("model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, nil)
	// The user edits the file after the turn: restoring would discard that.
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.journal.conflicts(1); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("conflicts = %v", got)
	}

	out, err := s.rewindToTurn(1, func(string) rewindDecision { return rewindKeep })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "mine\n" {
		t.Fatalf("file = %q, the user's version was overwritten", data)
	}
	if len(out.Files.Kept) != 1 {
		t.Fatalf("kept = %+v", out.Files)
	}

	// Asked to restore, the pre-turn bytes win.
	s2, root2 := journalSession(t, undoHistory())
	path2 := filepath.Join(root2, "a.txt")
	if err := os.WriteFile(path2, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2.journal.BeginTurn(1)
	s2.journal.Sync(root2, nil)
	if err := os.WriteFile(path2, []byte("model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2.journal.Collect(root2, nil)
	if err := os.WriteFile(path2, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.rewindToTurn(1, func(string) rewindDecision { return rewindRestore }); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path2); string(data) != "original\n" {
		t.Fatalf("file = %q, want the pre-turn content", data)
	}
}

func TestUndoJournalSurvivesASaveAndLoad(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)
	if err := os.WriteFile(path, []byte("model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, nil)
	if err := s.journal.Save(); err != nil {
		t.Fatal(err)
	}

	// A new process loads the journal of the same session and can still revert.
	reloaded := newUndoJournal(root, s.SessionID, undoJournalDir(getChatHistoryDir(), s.SessionID))
	if got := reloaded.turnChangedFiles(1); got != 1 {
		t.Fatalf("reloaded journal has %d changes for turn 1", got)
	}
	report := reloaded.revertTurn(1, nil)
	if len(report.Restored) != 1 {
		t.Fatalf("restored = %+v", report)
	}
	if data, _ := os.ReadFile(path); string(data) != "original\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestUndoCommandFlow(t *testing.T) {
	testWorkspace(t)
	sink := &recordSink{selectQueue: []int{0, 1}} // newest turn first, then "yes, rewind"
	s := &ChatSession{RestrictedRoot: "/work", ChatHistory: undoHistory(), Sink: sink}

	handleUndoCommand(s, "/undo")
	if len(s.ChatHistory) != 5 {
		t.Fatalf("history = %d messages, want the first turn only", len(s.ChatHistory))
	}
	if out := sink.text(); !strings.Contains(out, "Rewound to before turn 2") {
		t.Fatalf("output = %q", out)
	}
}

func TestUndoByNumberSkipsThePicker(t *testing.T) {
	testWorkspace(t)
	sink := &recordSink{selectQueue: []int{1}} // only the confirmation
	s := &ChatSession{RestrictedRoot: "/work", ChatHistory: undoHistory(), Sink: sink}

	handleUndoCommand(s, "/undo 1")
	if len(s.ChatHistory) != 1 || s.ChatHistory[0].Role != "system" {
		t.Fatalf("history = %+v", s.ChatHistory)
	}
	if out := sink.text(); !strings.Contains(out, "Rewound to before turn 1") {
		t.Fatalf("output = %q", out)
	}
}

func TestUndoRefusesAnImpossibleTurnNumber(t *testing.T) {
	testWorkspace(t)
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: "/work", ChatHistory: undoHistory(), Sink: sink}
	handleUndoCommand(s, "/undo 9")
	if len(s.ChatHistory) != 9 {
		t.Fatalf("history = %d messages, want it untouched", len(s.ChatHistory))
	}
	if out := sink.text(); !strings.Contains(out, "Usage: /undo [1-2]") {
		t.Fatalf("output = %q", out)
	}
}

func TestUndoCancelledChangesNothing(t *testing.T) {
	testWorkspace(t)
	sink := &recordSink{selectQueue: []int{-1}}
	s := &ChatSession{RestrictedRoot: "/work", ChatHistory: undoHistory(), Sink: sink}
	handleUndoCommand(s, "/undo")
	if len(s.ChatHistory) != 9 {
		t.Fatalf("a dismissed picker rewound the history: %d messages", len(s.ChatHistory))
	}
	if out := sink.text(); strings.Contains(out, "Rewound") {
		t.Fatalf("a dismissed picker reported a rewind: %q", out)
	}
}

func TestUndoIsNotALiveCommand(t *testing.T) {
	if liveSlashCommand("/undo") {
		t.Fatal("/undo rewrites the conversation and must wait for the running turn")
	}
	if liveSlashCommand("/undo 2") {
		t.Fatal("/undo with an argument must not run beside a generation either")
	}
}

// The dispatcher is what feeds the journal, so a tool call has to show up there
// without any per-tool bookkeeping — this is what makes `sh` revertible too.
func TestTurnJournalCapturesWhatAToolWrites(t *testing.T) {
	s, root := journalSession(t, []types.Message{{Role: "system", Content: "sys"}})
	s.Sink = &recordSink{}
	s.GlobalIgnore = tools.LoadIgnoreFiles(root)
	tools.SetTurnJournal(s.journal)
	t.Cleanup(func() { tools.SetTurnJournal(nil) })
	t.Cleanup(tools.ResetAnchorState)

	previousNoAsk := config.Get().NoAsk
	config.SetNoAsk(true)
	t.Cleanup(func() { config.SetNoAsk(previousNoAsk) })

	s.beginTurn()
	call := types.ToolCall{Function: types.ToolCallFunction{
		Name:      "write",
		Arguments: map[string]interface{}{"path": "made.txt", "content": "hello\n"},
	}}
	res := tools.ExecuteToolWithSpinner(context.Background(), call, s.GlobalIgnore, tools.Workspace{Primary: root}, nil)
	if strings.HasPrefix(res.Text, "error") {
		t.Fatalf("write failed: %s", res.Text)
	}
	// ProcessInput appends the prompt after the turn has been frozen.
	s.ChatHistory = append(s.ChatHistory, types.Message{Role: "user", Content: "create a file"})
	if got := s.journal.turnChangedFiles(0); got != 1 {
		t.Fatalf("the journal recorded %d changes, want the new file", got)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}

	if _, err := s.rewindToTurn(0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !os.IsNotExist(err) {
		t.Fatalf("the file the rewind created is still there: %v", err)
	}
}

// A shell command is a model change like any other: the journal diffs the
// workspace around it, so /undo puts back what `sh` wrote (or overwrote).
func TestUndoRevertsWhatShellCommandsChanged(t *testing.T) {
	s, root := journalSession(t, []types.Message{{Role: "system", Content: "sys"}})
	s.Sink = &recordSink{}
	s.GlobalIgnore = tools.LoadIgnoreFiles(root)
	tools.SetTurnJournal(s.journal)
	t.Cleanup(func() { tools.SetTurnJournal(nil) })

	previousNoAsk := config.Get().NoAsk
	config.SetNoAsk(true)
	t.Cleanup(func() { config.SetNoAsk(previousNoAsk) })

	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(command string) string {
		call := types.ToolCall{Function: types.ToolCallFunction{
			Name:      "sh",
			Arguments: map[string]any{"command": command, "timeout_seconds": float64(20)},
		}}
		return tools.ExecuteToolWithSpinner(context.Background(), call, s.GlobalIgnore, tools.Workspace{Primary: root}, nil).Text
	}

	s.beginTurn()
	out := run("printf changed > keep.txt && printf new > added.txt")
	if strings.Contains(out, "failed to start") || strings.Contains(out, "error") {
		t.Skipf("no usable sandbox here: %s", out)
	}
	// A sandbox that silently refuses the writes would look like a journal that
	// recorded nothing: say so instead of blaming the journal.
	if _, err := os.Stat(filepath.Join(root, "added.txt")); err != nil {
		t.Skipf("the sandbox did not let the command write its workspace: %s", out)
	}
	s.ChatHistory = append(s.ChatHistory, types.Message{Role: "user", Content: "run a command"})

	if got := s.journal.turnChangedFiles(0); got != 2 {
		t.Fatalf("the journal recorded %d changes, want the overwritten and the created file", got)
	}
	if _, err := s.rewindToTurn(0, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "before\n" {
		t.Fatalf("keep.txt = %q (%v), want the pre-turn content", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("the file the shell command created survived the rewind: %v", err)
	}
}

// A file the walk stops seeing without being deleted — a .gitignore written
// during the same turn hides it — is not a deletion: the turn changed nothing,
// and a rewind must leave it alone.
func TestJournalDoesNotRecordAnUnvisitedFileAsDeleted(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("still here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)

	// The turn hides the file behind a new ignore rule.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("keep.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, tools.LoadIgnoreFiles(root))

	if got := s.journal.turnChangedFiles(1); got != 0 {
		t.Fatalf("the journal recorded %d change(s) for a turn that hid a file, want none", got)
	}
	if _, err := s.rewindToTurn(1, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "still here\n" {
		t.Fatalf("keep.txt = %q (%v), want it untouched", data, err)
	}
}

// The guard must not swallow a real deletion: a file that is gone is still
// recorded, and a rewind recreates it.
func TestJournalStillRecordsARealDeletion(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	gone := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(gone, []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, tools.LoadIgnoreFiles(root))

	if got := s.journal.turnChangedFiles(1); got != 1 {
		t.Fatalf("the journal recorded %d change(s), want the deleted file", got)
	}
	if _, err := s.rewindToTurn(1, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(gone); err != nil || string(data) != "bye\n" {
		t.Fatalf("gone.txt = %q (%v), want it recreated", data, err)
	}
}

// A file edited after the turn is never restored silently: the command asks, and
// the answer decides whether the pre-turn bytes come back.
func TestUndoAsksAboutAFileChangedAfterTheTurn(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)
	if err := os.WriteFile(path, []byte("model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, nil)
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pick turn 2, confirm the rewind, then restore the file anyway.
	s.Sink = &recordSink{selectQueue: []int{0, 1, 1}}
	handleUndoCommand(s, "/undo")

	if data, _ := os.ReadFile(path); string(data) != "original\n" {
		t.Fatalf("file = %q, want the pre-turn content", data)
	}
	if out := s.Sink.(*recordSink).text(); !strings.Contains(out, "1 file(s) restored") {
		t.Fatalf("output = %q", out)
	}
}

// A file whose name starts with ".." is a normal workspace file: the walk must
// not mistake its relative path for an escape from the root.
func TestJournalRecordsADotDotFileName(t *testing.T) {
	s, root := journalSession(t, undoHistory())
	path := filepath.Join(root, "..notes.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(root, nil)
	if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(root, nil)

	if got := s.journal.turnChangedFiles(1); got != 1 {
		t.Fatalf("the journal recorded %d changes, want the ..notes.txt edit", got)
	}
	if _, err := s.rewindToTurn(1, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "before\n" {
		t.Fatalf("file = %q, want the pre-turn content", data)
	}
}

// A workspace reached through a symlink is still the workspace: macOS $TMPDIR is
// /var/folders/… → /private/var/…, so comparing a resolved path against an
// unresolved root refused every restore there and /undo silently left the files
// alone.
func TestUndoRestoresThroughASymlinkedWorkspace(t *testing.T) {
	testWorkspace(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	s := &ChatSession{RestrictedRoot: link, ChatHistory: undoHistory(), SessionID: "20250101-000000-abcdef0123456789"}
	s.journal = newUndoJournal(link, s.SessionID, undoJournalDir(getChatHistoryDir(), s.SessionID))

	path := filepath.Join(link, "a.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.BeginTurn(1)
	s.journal.Sync(link, nil)
	if err := os.WriteFile(path, []byte("model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.journal.Collect(link, nil)

	out, err := s.rewindToTurn(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "original\n" {
		t.Fatalf("file = %q, want the pre-turn content", data)
	}
	if len(out.Files.Restored) != 1 || len(out.Files.Failed) != 0 {
		t.Fatalf("files = %+v", out.Files)
	}
}

func TestInsideRootFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	outside := t.TempDir()
	escape := filepath.Join(real, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		root string
		abs  string
		want bool
	}{
		{"symlinked root", link, filepath.Join(link, "a.txt"), true},
		{"resolved root", real, filepath.Join(real, "a.txt"), true},
		{"a file in a subdirectory", link, filepath.Join(link, "sub", "a.txt"), true},
		{"file not created yet", link, filepath.Join(link, "new", "a.txt"), true},
		{"a sibling directory", link, filepath.Join(outside, "a.txt"), false},
		{"a parent traversal", link, filepath.Join(link, "..", "elsewhere.txt"), false},
		{"a link out of the tree", link, filepath.Join(link, "escape", "a.txt"), false},
		{"a name starting with dots", link, filepath.Join(link, "..notes.txt"), true},
	}
	for _, tc := range cases {
		if got := insideRoot(tc.root, tc.abs); got != tc.want {
			t.Errorf("%s: insideRoot(%q, %q) = %v, want %v", tc.name, tc.root, tc.abs, got, tc.want)
		}
	}
}
