package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// twoRoots builds a primary root with a file and an allowed extra root with its
// own file, returning both.
func twoRoots(t *testing.T) (primary, extra string) {
	t.Helper()
	primary, extra = t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "inner.txt"), []byte("inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "outer.txt"), []byte("outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return primary, extra
}

func runTool(t *testing.T, ws Workspace, name string, args map[string]any) types.ToolResult {
	t.Helper()
	previous := config.Get().NoAsk
	config.SetNoAsk(true)
	t.Cleanup(func() { config.SetNoAsk(previous) })
	call := types.ToolCall{Function: types.ToolCallFunction{Name: name, Arguments: args}}
	return ExecuteToolWithSpinner(context.Background(), call, nil, ws, nil)
}

func TestWorkspaceReadsAndWritesEveryAllowedRoot(t *testing.T) {
	primary, extra := twoRoots(t)
	ws := NewWorkspace(primary, []string{extra})

	got := runTool(t, ws, "read", map[string]any{"path": filepath.Join(extra, "outer.txt")}).Text
	if got != "outer\n" {
		t.Fatalf("read of an extra root = %q, want the file content", got)
	}
	if res := runTool(t, ws, "write", map[string]any{"path": filepath.Join(extra, "new.txt"), "content": "x\n"}); res.Text != "written" {
		t.Fatalf("write into an extra root = %q", res.Text)
	}
	if data, err := os.ReadFile(filepath.Join(extra, "new.txt")); err != nil || string(data) != "x\n" {
		t.Fatalf("the file was not written in the extra root: %q, %v", data, err)
	}
	if res := runTool(t, ws, "delete", map[string]any{"path": filepath.Join(extra, "new.txt")}); res.Text != "deleted" {
		t.Fatalf("delete in an extra root = %q", res.Text)
	}
	if res := runTool(t, ws, "ls", map[string]any{"path": extra}); !strings.Contains(res.Text, "outer.txt") {
		t.Fatalf("ls of an extra root = %q", res.Text)
	}
}

func TestWorkspaceGrepAndSnapshotReachAnExtraRoot(t *testing.T) {
	primary, extra := twoRoots(t)
	ws := NewWorkspace(primary, []string{extra})

	if out := runTool(t, ws, "grep", map[string]any{"pattern": "outer", "path": extra}).Text; !strings.Contains(out, "outer.txt") {
		t.Fatalf("grep in an extra root = %q", out)
	}
	if err := os.WriteFile(filepath.Join(extra, "sample.go"), []byte("package sample\n\nfunc Outer() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runTool(t, ws, "context_snapshot", map[string]any{"path": extra}).Text; !strings.Contains(out, "Outer") {
		t.Fatalf("context_snapshot of an extra root = %q", out)
	}
}

func TestWorkspaceRefusesPathsOutsideEveryRoot(t *testing.T) {
	primary, extra := twoRoots(t)
	ws := NewWorkspace(primary, []string{extra})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := runTool(t, ws, "read", map[string]any{"path": filepath.Join(outside, "secret.txt")}); !strings.Contains(res.Text, "outside the workspace") {
		t.Fatalf("a path outside every root was accepted: %q", res.Text)
	}
	if res := runTool(t, ws, "read", map[string]any{"path": "../../etc/passwd"}); !strings.Contains(res.Text, "traversal") {
		t.Fatalf("a relative escape was accepted: %q", res.Text)
	}
}

func TestWorkspaceCopiesAndMovesAcrossRoots(t *testing.T) {
	primary, extra := twoRoots(t)
	ws := NewWorkspace(primary, []string{extra})

	if res := runTool(t, ws, "copy", map[string]any{
		"source":      filepath.Join(primary, "inner.txt"),
		"destination": filepath.Join(extra, "copy.txt"),
	}); res.Text != "copied" {
		t.Fatalf("cross-root copy = %q", res.Text)
	}
	if data, err := os.ReadFile(filepath.Join(extra, "copy.txt")); err != nil || string(data) != "inner\n" {
		t.Fatalf("cross-root copy result = %q, %v", data, err)
	}

	if res := runTool(t, ws, "move", map[string]any{
		"source":      filepath.Join(extra, "copy.txt"),
		"destination": filepath.Join(primary, "moved.txt"),
	}); res.Text != "moved" {
		t.Fatalf("cross-root move = %q", res.Text)
	}
	if _, err := os.Stat(filepath.Join(extra, "copy.txt")); !os.IsNotExist(err) {
		t.Fatalf("the moved file is still in the source root: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(primary, "moved.txt")); err != nil || string(data) != "inner\n" {
		t.Fatalf("cross-root move result = %q, %v", data, err)
	}
}

func TestNewWorkspaceDropsNestedAndDuplicateRoots(t *testing.T) {
	primary := t.TempDir()
	nested := filepath.Join(primary, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	ws := NewWorkspace(primary, []string{nested, primary, other, other})
	roots := ws.Roots()
	if len(roots) != 2 || roots[0] == roots[1] {
		t.Fatalf("roots = %v, want the primary and the one unrelated root", roots)
	}
}

func TestShSensitivePathsCoverEveryRoot(t *testing.T) {
	primary, extra := twoRoots(t)
	paths := workspaceSensitivePaths(sandboxRoots(sandboxConfig{
		RestrictedRoot: primary,
		Roots:          NewWorkspace(primary, []string{extra}).Roots(),
	})...)
	if len(paths) != 2 {
		t.Fatalf("masked paths = %v, want <root>/.git of both roots", paths)
	}
}

func TestSubagentCanBeSpawnedInAnExtraRoot(t *testing.T) {
	primary, extra := twoRoots(t)
	ws := NewWorkspace(primary, []string{extra})

	// The run stops at the provider factory: what matters is that the folder was
	// accepted as a working directory instead of being called "outside".
	res := RunSubagent(context.Background(), SubagentConfig{
		ID: "x", Subdirectory: extra, Instructions: "noop", MaxIterations: 1,
	}, nil, ws, nil, nil)
	if strings.Contains(res, "outside the restricted root") {
		t.Fatalf("an allowed root was refused as a sub-agent directory: %s", res)
	}
}

func TestBindPathKeepsTheToolArgumentsIntact(t *testing.T) {
	primary, extra := twoRoots(t)
	ws := NewWorkspace(primary, []string{extra})

	args := map[string]any{"path": filepath.Join(extra, "outer.txt"), "limit": float64(5)}
	bound, root, err := bindPath(args, "path", ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The owning root comes back resolved, like every root of the workspace.
	if root != config.ResolveDir(extra) || bound["path"] != "outer.txt" {
		t.Fatalf("bindPath = %q %v", root, bound)
	}
	if args["path"] == bound["path"] {
		t.Fatal("bindPath mutated the caller's arguments")
	}
}
