package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression test: a sub-agent spawned in a directory that contains an ignore
// file must NOT have every path flagged as ignored.
//
// buildSubagentIgnore used to return a zero-value IgnoreManager (gitDir and
// gitDirWithSep left empty). isIgnored() blocks any path prefixed by
// gitDirWithSep, and an empty prefix matches EVERY path — so every read/ls/grep
// inside the sub-agent was denied with "listed in an ignore file".
func TestBuildSubagentIgnoreDoesNotDenyWorkspace(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// The subdirectory has its own ignore file → the merge path is taken.
	if err := os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	parent := LoadIgnoreFiles(root)
	merged := buildSubagentIgnore(parent, sub)
	if merged == nil {
		t.Fatal("buildSubagentIgnore returned nil for a non-nil parent")
	}

	// A normal file inside the sub-agent's root must be readable.
	abs := filepath.Join(sub, "main.go")
	if merged.isIgnored(abs, sub) {
		t.Fatalf("%s wrongly flagged as ignored (gitDir=%q)", abs, merged.gitDir)
	}

	// Local rules must still apply.
	if !merged.isIgnored(filepath.Join(sub, "debug.log"), sub) {
		t.Error("local .gitignore rule (*.log) was not honoured")
	}

	// The .git directory of the sub-agent's root must stay blocked.
	if !merged.isIgnored(filepath.Join(sub, ".git", "config"), sub) {
		t.Error(".git inside the sub-agent root must be blocked")
	}
}

// When the subdirectory has no ignore file the parent's rules are kept, but
// the .git guard must still be re-rooted on the sub-agent's own root.
func TestBuildSubagentIgnoreWithoutLocalFiles(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// The nested subdirectory is itself a git repo (has .git) but has no
	// ignore file: LoadIgnoreFiles(sub) returns files == 0.
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	parent := LoadIgnoreFiles(root)
	im := buildSubagentIgnore(parent, sub)
	if im == nil {
		t.Fatal("buildSubagentIgnore returned nil for a non-nil parent")
	}

	// gitDir must point at the sub-agent's root, not the parent's.
	wantGit := filepath.Join(sub, ".git")
	if im.gitDir != wantGit {
		t.Errorf("gitDir = %q, want %q", im.gitDir, wantGit)
	}
	// The sub-agent's own .git must be blocked...
	if !im.isIgnored(filepath.Join(sub, ".git", "HEAD"), sub) {
		t.Error("nested .git of the sub-agent root must be blocked")
	}
	// ...while the parent's .git is unreachable through the sub-agent root and
	// simply must not be relevant here.
	if im.isIgnored(filepath.Join(sub, "plain.txt"), sub) {
		t.Error("a normal file in the sub-agent root was wrongly ignored")
	}
	if got := buildSubagentIgnore(nil, sub); got != nil {
		t.Errorf("expected nil for a nil parent, got %#v", got)
	}
}

// A zero-value IgnoreManager must not deny the whole workspace, even if the
// prefix guard above is ever bypassed.
func TestZeroValueIgnoreManagerDoesNotDenyAll(t *testing.T) {
	var im IgnoreManager
	if im.isIgnored("/any/path/at/all", "/any/path") {
		t.Error("zero-value IgnoreManager denied an arbitrary path")
	}
}
