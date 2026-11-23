package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
)

// A read-only git call without a path answers for every allowed root that is a
// repository, each section prefixed by its root.
func TestGitReadToolsAnswerForEveryAllowedRoot(t *testing.T) {
	repoA := newGitTestRepo(t)
	repoB := newGitTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoB, "only-b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(repoA, []string{repoB})

	out := gitReadOnWorkspace(ws, map[string]any{"count": float64(5)}, executeGitLog)
	// The sections name the roots as the workspace resolves them: the paths the
	// caller typed and the canonical ones differ on macOS and on Windows (8.3).
	if !strings.Contains(out, "## "+config.ResolveDir(repoA)) || !strings.Contains(out, "## "+config.ResolveDir(repoB)) {
		t.Fatalf("git_log did not cover both roots:\n%s", out)
	}

	status := gitReadOnWorkspace(ws, map[string]any{}, executeGitStatus)
	if !strings.Contains(status, "only-b.txt") {
		t.Fatalf("git_status missed the file of the extra root:\n%s", status)
	}
}

// An explicit path pins the call to the root that owns it, with no section
// header (the answer is unambiguous).
func TestGitReadToolWithAPathStaysOnItsRoot(t *testing.T) {
	repoA := newGitTestRepo(t)
	repoB := newGitTestRepo(t)
	ws := NewWorkspace(repoA, []string{repoB})

	out := gitReadOnWorkspace(ws, map[string]any{"path": filepath.Join(repoB, "base.txt")}, executeGitBlame)
	if strings.Contains(out, "## ") {
		t.Fatalf("a path-scoped call was labelled:\n%s", out)
	}
	if !strings.Contains(out, "hello") && !strings.Contains(out, "fyc test") {
		t.Fatalf("git_blame of the extra root = %q", out)
	}
}

func TestGitWorkspaceRootPicksTheOwnerOfAnExplicitPath(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	ws := NewWorkspace(primary, []string{extra})

	if got := gitWorkspaceRoot(map[string]any{}, ws); got != ws.primaryRoot() {
		t.Fatalf("gitWorkspaceRoot() = %q, want the primary root", got)
	}
	if got := gitWorkspaceRoot(map[string]any{"path": filepath.Join(extra, "x")}, ws); got != ws.Roots()[1] {
		t.Fatalf("gitWorkspaceRoot(explicit) = %q, want %q", got, ws.Roots()[1])
	}
	if got := gitWorkspaceRoot(map[string]any{"paths": []any{filepath.Join(extra, "y")}}, ws); got != ws.Roots()[1] {
		t.Fatalf("gitWorkspaceRoot(paths) = %q, want %q", got, ws.Roots()[1])
	}
}
