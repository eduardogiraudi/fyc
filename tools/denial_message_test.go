package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// TestDeniedToolResultsAreFailures guards the UI contract: a user-cancelled or
// denied tool call must come back as a failure (the "error:" prefix) so the TUI
// renders "failed to ..." instead of a misleading success ("patched ..."/"wrote ...").
func TestDeniedToolResultsAreFailures(t *testing.T) {
	SetInteractor(NewHeadlessInteractor("deny"))
	t.Cleanup(func() { SetInteractor(nil) })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	cases := []struct {
		name string
		run  func() string
	}{
		{"write", func() string {
			return executeWriteTool(ctx, map[string]any{"path": "new.txt", "content": "x"}, dir, nil)
		}},
		{"delete", func() string {
			return executeDeleteTool(ctx, map[string]any{"path": "a.txt"}, dir, nil)
		}},
		{"make_dir", func() string {
			return executeMakeDirTool(ctx, map[string]any{"path": "sub"}, dir, nil)
		}},
		{"copy", func() string {
			return executeCopyTool(ctx, map[string]any{"source": "a.txt", "destination": "b.txt"}, dir, nil)
		}},
		{"rename", func() string {
			return executeRenameTool(ctx, map[string]any{"path": "a.txt", "new_name": "c.txt"}, dir, nil)
		}},
		{"move", func() string {
			return executeMoveTool(ctx, map[string]any{"source": "a.txt", "destination": "d.txt"}, dir, nil)
		}},
		{"sh", func() string {
			return executeShTool(ctx, map[string]any{"command": "true"}, dir, nil)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.run()
			if !strings.HasPrefix(got, "error:") {
				t.Fatalf("%s denial result %q does not start with \"error:\"", tc.name, got)
			}
			if !types.ToolResultFailed(got) {
				t.Fatalf("%s denial result %q is not detected as a failure", tc.name, got)
			}
		})
	}
}
