package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/modes"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"fuckyourcli/ui"
)

// cdTestConfig points the configuration at a throw-away directory so /cd can
// persist without touching the developer's config.json.
func cdTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	t.Setenv("FYC_AUTH_FILE", "")
	config.Load()
	t.Cleanup(func() {
		restore := t.TempDir()
		_ = os.Setenv("FYC_CONFIG_DIR", restore)
		config.Load()
	})
	return dir
}

func cdSession(t *testing.T) (*ChatSession, *recordSink, string) {
	t.Helper()
	cdTestConfig(t)
	testWorkspace(t)
	root := t.TempDir()
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: root, Sink: sink, ChatHistory: []types.Message{{Role: "system", Content: "sys"}}}
	s.ws = newWorkspaceForTest(root, nil)
	// No test opens the directory picker: a nested Bubble Tea program would wait
	// for a terminal that is not there.
	oldPick := pickDirCD
	pickDirCD = func(string, string, []string) (string, bool) { return "", false }
	t.Cleanup(func() { pickDirCD = oldPick })
	return s, sink, root
}

// TestCDAddsARootForTheSessionOnly drives "/cd <path>" answering "only for this
// session": no config write, and the extra root is live immediately.
func TestCDAddsARootForTheSessionOnly(t *testing.T) {
	s, sink, root := cdSession(t)
	extra := t.TempDir()
	sink.selectQueue = []int{0}

	if handled, _ := handleSlashCommand("/cd "+extra, s); !handled {
		t.Fatal("/cd was not handled")
	}
	if len(s.ExtraRoots) != 1 || s.ExtraRoots[0] != config.ResolveDir(extra) {
		t.Fatalf("ExtraRoots = %v, want the new root", s.ExtraRoots)
	}
	if s.workspaceRoots()[1] != config.ResolveDir(extra) {
		t.Fatalf("workspaceRoots() = %v", s.workspaceRoots())
	}
	if len(config.SavedWorkspaces()) != 0 {
		t.Fatalf("a session-only root was persisted: %+v", config.SavedWorkspaces())
	}
	if !strings.Contains(sink.text(), "Allowed") {
		t.Fatalf("no confirmation printed: %q", sink.text())
	}
	// The primary root is still the one relative paths resolve against.
	if s.workspace().Primary != config.ResolveDir(root) {
		t.Fatalf("primary root moved: %q", s.workspace().Primary)
	}

	sink.selectQueue = nil
	if handled, _ := handleSlashCommand("/cd remove "+extra, s); !handled {
		t.Fatal("/cd remove was not handled")
	}
	if len(s.ExtraRoots) != 0 {
		t.Fatalf("the root was not removed: %v", s.ExtraRoots)
	}
}

// TestCDSavesTheRootSet asks for the "save" answer and expects the set in
// config.json, reloadable through a fresh Load.
func TestCDSavesTheRootSet(t *testing.T) {
	s, sink, root := cdSession(t)
	extra := t.TempDir()
	sink.selectQueue = []int{1} // save the whole set

	if handled, _ := handleSlashCommand("/cd "+extra, s); !handled {
		t.Fatal("/cd was not handled")
	}
	groups := config.SavedWorkspaces()
	if len(groups) != 1 || len(groups[0].Roots) != 2 {
		t.Fatalf("saved workspaces = %+v", groups)
	}
	if groups[0].Roots[0] != config.ResolveDir(root) || groups[0].Roots[1] != config.ResolveDir(extra) {
		t.Fatalf("saved roots = %v", groups[0].Roots)
	}

	config.Load()
	if len(config.SavedWorkspaces()) != 1 {
		t.Fatalf("the set did not survive a reload: %+v", config.SavedWorkspaces())
	}
}

// TestStartupWorkspaceOfferLoadsTheSavedRoots answers the startup question with
// the saved set and checks that it becomes the session's extra roots.
func TestStartupWorkspaceOfferLoadsTheSavedRoots(t *testing.T) {
	cdTestConfig(t)
	testWorkspace(t)
	root, other := t.TempDir(), t.TempDir()
	config.UpsertSavedWorkspace("pair", []string{root, other})
	if err := config.SaveE(); err != nil {
		t.Fatal(err)
	}
	config.Load()

	oldInteractive, oldSelect := cdInteractive, selectCD
	cdInteractive = func() bool { return true }
	selectCD = func(string, []string) int { return 0 }
	t.Cleanup(func() { cdInteractive, selectCD = oldInteractive, oldSelect })

	s := &ChatSession{RestrictedRoot: root}
	startupWorkspaceOffer(s)

	if len(s.ExtraRoots) != 1 || s.ExtraRoots[0] != config.ResolveDir(other) {
		t.Fatalf("ExtraRoots = %v, want the saved second root", s.ExtraRoots)
	}
}

// TestStartupWorkspaceOfferDeclinedChangesNothing covers the "start with only
// this folder" row.
func TestStartupWorkspaceOfferDeclinedChangesNothing(t *testing.T) {
	cdTestConfig(t)
	testWorkspace(t)
	root, other := t.TempDir(), t.TempDir()
	config.UpsertSavedWorkspace("pair", []string{root, other})

	oldInteractive, oldSelect := cdInteractive, selectCD
	cdInteractive = func() bool { return true }
	selectCD = func(_ string, options []string) int { return len(options) - 1 }
	t.Cleanup(func() { cdInteractive, selectCD = oldInteractive, oldSelect })

	s := &ChatSession{RestrictedRoot: root}
	startupWorkspaceOffer(s)
	if len(s.ExtraRoots) != 0 {
		t.Fatalf("a declined offer still allowed %v", s.ExtraRoots)
	}
}

// TestStartupWorkspaceOfferDismissalQuits mirrors the chat picker: closing the
// startup question with q/Esc/Ctrl+C quits the program instead of starting a
// chat.
func TestStartupWorkspaceOfferDismissalQuits(t *testing.T) {
	cdTestConfig(t)
	testWorkspace(t)
	root, other := t.TempDir(), t.TempDir()
	config.UpsertSavedWorkspace("pair", []string{root, other})

	oldInteractive, oldSelect := cdInteractive, selectCD
	cdInteractive = func() bool { return true }
	selectCD = func(string, []string) int { return ui.SelectorCanceled }
	t.Cleanup(func() { cdInteractive, selectCD = oldInteractive, oldSelect })

	s := &ChatSession{RestrictedRoot: root}
	if !startupWorkspaceOffer(s) {
		t.Fatal("a dismissed startup question must report that the caller has to quit")
	}
	if len(s.ExtraRoots) != 0 {
		t.Fatalf("a dismissed question still allowed %v", s.ExtraRoots)
	}
}

// TestSystemPromptListsEveryAllowedRoot: a single root keeps the historical
// prompt byte for byte, multiple roots have to be named.
func TestSystemPromptListsEveryAllowedRoot(t *testing.T) {
	testWorkspace(t)
	root, second, third := t.TempDir(), t.TempDir(), t.TempDir()

	single := &ChatSession{RestrictedRoot: root}
	single.ws = newWorkspaceForTest(root, nil)
	if prompt := buildSystemPrompt("today", "", single); strings.Contains(prompt, "Workspace roots:") {
		t.Fatal("a single-root session gained a multi-root rule")
	}

	multi := &ChatSession{RestrictedRoot: root}
	multi.ws = newWorkspaceForTest(root, []string{second, third})
	prompt := buildSystemPrompt("today", "", multi)
	for _, want := range []string{"Workspace roots:", second, third, "absolute path"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not mention %q:\n%s", want, prompt)
		}
	}
}

// TestUndoRevertsFilesInAnExtraRoot is the /undo guarantee for /cd: a turn that
// wrote outside the primary root is reverted too.
func TestUndoRevertsFilesInAnExtraRoot(t *testing.T) {
	s, primary := journalSession(t, []types.Message{{Role: "system", Content: "sys"}})
	extra := t.TempDir()
	s.setExtraRoots([]string{extra})
	s.Sink = &recordSink{}
	s.GlobalIgnore = loadWorkspaceIgnoreForTest(s)
	s.journal.SetRoots(s.workspaceRoots(), s.GlobalIgnore)
	s.journal.Sync(primary, s.GlobalIgnore)

	if got := s.journal.turnChangedFiles(0); got != 0 {
		t.Fatalf("the baseline recorded %d changes, want none", got)
	}

	previousNoAsk := config.Get().NoAsk
	config.SetNoAsk(true)
	t.Cleanup(func() { config.SetNoAsk(previousNoAsk) })
	tools.SetTurnJournal(s.journal)
	t.Cleanup(func() { tools.SetTurnJournal(nil) })

	s.beginTurn()
	target := filepath.Join(extra, "created.txt")
	res := executeToolForTest(s, "write", map[string]any{"path": target, "content": "x\n"})
	if strings.HasPrefix(res, "error") {
		t.Fatalf("write into the extra root failed: %s", res)
	}
	s.ChatHistory = append(s.ChatHistory, types.Message{Role: "user", Content: "write outside"})
	if _, err := s.rewindToTurn(0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the file in the extra root survived the rewind: %v", err)
	}
}

// TestCDRefusesAFolderInsideAnAllowedRoot: a subfolder of an allowed root grants
// nothing new, and saying "allowed" without adding it would be a lie.
func TestCDRefusesAFolderInsideAnAllowedRoot(t *testing.T) {
	s, sink, root := cdSession(t)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if handled, _ := handleSlashCommand("/cd "+sub, s); !handled {
		t.Fatal("/cd was not handled")
	}
	if len(s.ExtraRoots) != 0 {
		t.Fatalf("a nested folder was added as a root: %v", s.ExtraRoots)
	}
	if !strings.Contains(sink.text(), "already inside") {
		t.Fatalf("no explanation printed: %q", sink.text())
	}
	// The same folder is still reachable through the primary root.
	if len(s.workspaceRoots()) != 1 {
		t.Fatalf("workspaceRoots() = %v", s.workspaceRoots())
	}
}

// TestCDRefusesAnAlreadyAllowedRoot asks twice for the same folder.
func TestCDRefusesAnAlreadyAllowedRoot(t *testing.T) {
	s, sink, _ := cdSession(t)
	extra := t.TempDir()
	sink.selectQueue = []int{0}
	handleSlashCommand("/cd "+extra, s)
	sink.selectQueue = nil
	sink.reset()

	handleSlashCommand("/cd "+extra, s)
	if len(s.ExtraRoots) != 1 {
		t.Fatalf("ExtraRoots = %v", s.ExtraRoots)
	}
	if !strings.Contains(sink.text(), "already allowed") {
		t.Fatalf("no duplicate notice: %q", sink.text())
	}
}

// TestCDRemovesThroughTheMenu ticks one root in the checklist and expects only
// the others to survive.
func TestCDRemovesThroughTheMenu(t *testing.T) {
	s, sink, _ := cdSession(t)
	first, second := t.TempDir(), t.TempDir()
	sink.selectQueue = []int{0, 0} // add the first, session-only
	handleSlashCommand("/cd "+first, s)
	handleSlashCommand("/cd "+second, s)
	if len(s.ExtraRoots) != 2 {
		t.Fatalf("ExtraRoots = %v, want both roots", s.ExtraRoots)
	}

	old := multiSelectCD
	multiSelectCD = func(_ string, options []string, _ []bool) []bool {
		out := make([]bool, len(options))
		for i, o := range options {
			out[i] = config.ResolveDir(o) == config.ResolveDir(first)
		}
		return out
	}
	t.Cleanup(func() { multiSelectCD = old })

	sink.selectQueue = []int{1, 3} // menu → Remove a folder… → Done
	handleSlashCommand("/cd", s)
	if len(s.ExtraRoots) != 1 || s.ExtraRoots[0] != config.ResolveDir(second) {
		t.Fatalf("ExtraRoots after the removal menu = %v", s.ExtraRoots)
	}
}

// TestCDIsListedInTheCommands: /help and the suggestion window are built from
// modes.GetCommands, so /cd has to be there.
func TestCDIsListedInTheCommands(t *testing.T) {
	for _, cmd := range modes.GetCommands(true) {
		if cmd.Name == "/cd" {
			if cmd.Description == "" {
				t.Fatal("/cd has no description")
			}
			return
		}
	}
	t.Fatal("/cd is missing from the command list")
}

// TestCDIsNotALiveCommand: /cd rewrites the system prompt and the sandbox, so it
// must wait for the turn in flight.
func TestCDIsNotALiveCommand(t *testing.T) {
	if liveSlashCommand("/cd") || liveSlashCommand("/cd /tmp") {
		t.Fatal("/cd must not run beside a generation")
	}
}
