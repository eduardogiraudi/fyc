package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sibling directory that merely shares the root's name prefix must not pass
// as "inside" the workspace: /tmp/x2 is not /tmp/x.
func TestSubagentRejectsSiblingWithSharedPrefix(t *testing.T) {
	root := t.TempDir()
	sibling := root + "2"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sibling) })

	res := RunSubagent(context.Background(), SubagentConfig{ID: "x", Subdirectory: sibling, Instructions: "noop", MaxIterations: 1}, nil, Workspace{Primary: root}, nil, nil)
	if !strings.Contains(res, "outside the restricted root") {
		t.Fatalf("the sibling directory was accepted as a subdirectory: %s", res)
	}
}

// os.Stat follows symlinks and the subdirectory becomes the tools' restricted
// root, so a link inside the workspace must not re-root the sub-agent outside.
func TestSubagentRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "logs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := RunSubagent(context.Background(), SubagentConfig{ID: "x", Subdirectory: "logs", Instructions: "noop", MaxIterations: 1}, nil, Workspace{Primary: root}, nil, nil)
	if !strings.Contains(res, "resolves outside the restricted root") {
		t.Fatalf("the symlinked directory was accepted as a subdirectory: %s", res)
	}
}
