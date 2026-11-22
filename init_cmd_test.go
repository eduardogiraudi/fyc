package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// /init must never write the file itself: it returns the task for the model and
// the task has to name the workspace root and the target file.
func TestInitReturnsATaskForTheModel(t *testing.T) {
	dir := t.TempDir()
	sink := &recordSink{}
	s := &ChatSession{Sink: sink, RestrictedRoot: dir}

	handled, prompt := handleSlashCommand("/init", s)
	if !handled || prompt == "" {
		t.Fatalf("/init did not produce a task: %q", prompt)
	}
	for _, want := range []string{"AGENTS.md", dir, "get_anchors", "replace_anchors", "No instruction file exists"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("task is missing %q:\n%s", want, prompt)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("/init wrote the file itself")
	}
}

// The argument is a specification: it reaches the model verbatim and replaces
// the default structure.
func TestInitSpecificationIsCarried(t *testing.T) {
	sink := &recordSink{}
	s := &ChatSession{Sink: sink, RestrictedRoot: t.TempDir()}

	handled, prompt := handleSlashCommand("/init  only the release process, in Italian", s)
	if !handled {
		t.Fatal("/init with a specification was not handled")
	}
	if !strings.Contains(prompt, "<specification>\nonly the release process, in Italian\n</specification>") {
		t.Fatalf("the specification was not carried verbatim:\n%s", prompt)
	}
	if !strings.Contains(prompt, "follow it literally") {
		t.Fatalf("the specification is not authoritative:\n%s", prompt)
	}
}

// An existing file must be extended, not overwritten, and an override file has
// to be reported because it would outrank the generated one.
func TestInitReportsExistingInstructions(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"AGENTS.md":          "# AGENTS.md\nkeep me\n",
		"AGENTS.override.md": "override\n",
		"CLAUDE.md":          "claude\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prompt := buildInitPrompt(&ChatSession{RestrictedRoot: dir}, "")
	for _, want := range []string{
		"`AGENTS.md` already exists",
		"`AGENTS.override.md` already exists",
		"These compatibility files exist (CLAUDE.md)",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "<specification>") {
		t.Fatal("an empty specification was emitted")
	}
}

// "/init help" documents the command and starts no turn.
func TestInitHelpStartsNoTurn(t *testing.T) {
	sink := &recordSink{}
	handled, prompt := handleSlashCommand("/init help", &ChatSession{Sink: sink, RestrictedRoot: t.TempDir()})
	if !handled || prompt != "" {
		t.Fatalf("/init help returned a task: %q", prompt)
	}
	if !strings.Contains(sink.text(), "Usage: /init") || !strings.Contains(sink.text(), "AGENTS.md") {
		t.Fatalf("usage is missing: %s", sink.text())
	}
}

// The reload is the reason /init works in the same session: the chain is
// re-read at the start of a turn and the prompt is re-rendered only on a change.
func TestRefreshProjectContextPicksUpAGENTSMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &ChatSession{RestrictedRoot: dir}
	s.ChatHistory = []types.Message{{Role: "system", Content: "stale <project_context>"}}
	refreshProjectContext(s)
	if !strings.Contains(s.ChatHistory[0].Content, "first") {
		t.Fatalf("the chain was not injected:\n%s", s.ChatHistory[0].Content)
	}
	before := s.ChatHistory[0].Content
	refreshProjectContext(s)
	if s.ChatHistory[0].Content != before {
		t.Fatal("the prompt was re-rendered without a change")
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshProjectContext(s)
	if !strings.Contains(s.ChatHistory[0].Content, "second") {
		t.Fatalf("the edited chain was not reloaded:\n%s", s.ChatHistory[0].Content)
	}
}
