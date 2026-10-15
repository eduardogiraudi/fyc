package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

type anchorApprovalFunc func(context.Context, InteractionRequest) (InteractionResponse, error)

func (f anchorApprovalFunc) Interact(ctx context.Context, req InteractionRequest) (InteractionResponse, error) {
	return f(ctx, req)
}

func setupAnchorFile(t *testing.T, content string) (string, string) {
	t.Helper()
	prevState, prevInteractor, prevNoAsk := globalAnchorState, getInteractor(), config.Get().NoAsk
	globalAnchorState = newTestManager()
	config.SetNoAsk(false)
	SetInteractor(NewHeadlessInteractor("auto"))
	t.Cleanup(func() {
		globalAnchorState = prevState
		SetInteractor(prevInteractor)
		config.SetNoAsk(prevNoAsk)
	})
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	writeAnchorTestFile(t, path, content)
	return root, path
}

func writeAnchorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readAnchorToken(t *testing.T, root string, line int) string {
	t.Helper()
	out := executeGetAnchorsTool(map[string]any{"path": "file.txt"}, root, nil)
	lines := strings.Split(out, "\n")
	if line >= len(lines) {
		t.Fatalf("missing line %d: %s", line, out)
	}
	anchor, _, ok := strings.Cut(lines[line], anchorDelimiter)
	if !ok {
		t.Fatalf("get_anchors failed: %s", out)
	}
	return anchor
}

func patchAnchorFile(ctx context.Context, root, anchor, replacement string) string {
	return executeReplaceAnchorsTool(ctx, map[string]any{
		"path": "file.txt", "start_anchor": anchor, "end_anchor": anchor, "replacement": replacement,
	}, root, nil)
}

func assertAnchorFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file = %q, %v; want %q", data, err, want)
	}
}

func TestReplaceAnchorsRejectsExternalChanges(t *testing.T) {
	for _, tc := range []struct {
		name, external, replacement string
	}{
		{"target", "first\nuser\nlast\n", "agent"},
		{"unrelated line", "user\ntarget\nlast\n", "agent"},
		{"insertion", "first\ninserted\ntarget\nlast\n", "agent"},
		{"deletion", "target\nlast\n", "agent"},
		{"whitespace", "first\n  target\nlast\n", "agent"},
		{"final newline", "first\ntarget\nlast", "agent"},
		{"line endings", "first\r\ntarget\r\nlast\r\n", "agent"},
		{"stale no-op", "user\ntarget\nlast\n", "target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := setupAnchorFile(t, "first\ntarget\nlast\n")
			anchor := readAnchorToken(t, root, 1)
			writeAnchorTestFile(t, path, tc.external)
			called := false
			SetInteractor(anchorApprovalFunc(func(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
				called = true
				return InteractionResponse{Choice: req.AllowIndex}, nil
			}))
			res := patchAnchorFile(context.Background(), root, anchor, tc.replacement)
			if !types.ToolResultFailed(res) || !strings.Contains(res, "file changed on disk") || !strings.Contains(res, "get_anchors again") {
				t.Fatalf("expected actionable conflict, got %s", res)
			}
			if called {
				t.Fatal("stale diff was offered for approval")
			}
			assertAnchorFile(t, path, tc.external)
			if _, err := globalAnchorState.GetAnnotated(path); err == nil {
				t.Fatal("conflict must invalidate the cached file, not silently refresh it")
			}
			fresh := readAnchorToken(t, root, 0)
			res = patchAnchorFile(context.Background(), root, fresh, "reconsidered")
			if !strings.HasPrefix(res, "updated") {
				t.Fatalf("patch after explicit re-read: %s", res)
			}
		})
	}
}

func TestReplaceAnchorsRejectsChangesDuringApproval(t *testing.T) {
	for _, action := range []string{"edit", "remove", "reset", "reset and re-read", "another patch"} {
		t.Run(action, func(t *testing.T) {
			root, path := setupAnchorFile(t, "first\ntarget\nlast\n")
			anchor := readAnchorToken(t, root, 1)
			want := "first\ntarget\nlast\n"
			SetInteractor(anchorApprovalFunc(func(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
				switch action {
				case "edit":
					want = "user\ntarget\nlast\n"
					writeAnchorTestFile(t, path, want)
				case "remove":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "reset", "reset and re-read":
					ResetAnchorState()
					if action == "reset and re-read" {
						if fresh := readAnchorToken(t, root, 1); fresh == anchor {
							t.Fatal("reset must not recycle stale anchors")
						}
					}
				case "another patch":
					SetInteractor(NewHeadlessInteractor("auto"))
					res := patchAnchorFile(context.Background(), root, anchor, "other agent")
					if !strings.HasPrefix(res, "updated") {
						t.Fatalf("concurrent patch: %s", res)
					}
					want = "first\nother agent\nlast\n"
				}
				return InteractionResponse{Choice: req.AllowIndex}, nil
			}))
			res := patchAnchorFile(context.Background(), root, anchor, "agent")
			if !types.ToolResultFailed(res) || !strings.Contains(res, "get_anchors") {
				t.Fatalf("expected rejection after approval, got %s", res)
			}
			if action == "remove" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("removed file was recreated: %v", err)
				}
			} else {
				assertAnchorFile(t, path, want)
			}
		})
	}
}

func TestReplaceAnchorsRejectsMissingFile(t *testing.T) {
	root, path := setupAnchorFile(t, "original\n")
	anchor := readAnchorToken(t, root, 0)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res := patchAnchorFile(context.Background(), root, anchor, "agent")
	if !types.ToolResultFailed(res) {
		t.Fatalf("expected failure, got %s", res)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed file was recreated: %v", err)
	}
}

func TestReplaceAnchorsSuccessfulEditsKeepStableAnchors(t *testing.T) {
	for _, content := range []string{"first\ntarget\nlast\n", "first\ntarget\nlast"} {
		t.Run(content, func(t *testing.T) {
			root, path := setupAnchorFile(t, content)
			stable := readAnchorToken(t, root, 0)
			anchor := readAnchorToken(t, root, 1)
			res := patchAnchorFile(context.Background(), root, anchor, "agent")
			_, inserted, ok := strings.Cut(res, "new anchors:\n")
			if !ok {
				t.Fatalf("missing new anchors: %s", res)
			}
			fresh, _, _ := strings.Cut(inserted, anchorDelimiter)
			if fresh == anchor {
				t.Fatal("edited line reused its retired anchor")
			}
			assertAnchorFile(t, path, "first\nagent\nlast\n")
			if res = patchAnchorFile(context.Background(), root, stable, "kept"); !strings.HasPrefix(res, "updated") {
				t.Fatalf("unchanged anchor not usable: %s", res)
			}
			if res = patchAnchorFile(context.Background(), root, fresh, "again"); !strings.HasPrefix(res, "updated") {
				t.Fatalf("new anchor not usable without re-read: %s", res)
			}
			assertAnchorFile(t, path, "kept\nagain\nlast\n")
			if res = patchAnchorFile(context.Background(), root, fresh, "stale"); !types.ToolResultFailed(res) {
				t.Fatalf("retired anchor accepted: %s", res)
			}
		})
	}
}

func TestReplaceAnchorsCancelledOrDeniedDoesNotWrite(t *testing.T) {
	for _, deny := range []bool{false, true} {
		root, path := setupAnchorFile(t, "original\n")
		anchor := readAnchorToken(t, root, 0)
		ctx, cancel := context.WithCancel(context.Background())
		SetInteractor(anchorApprovalFunc(func(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
			if deny {
				return InteractionResponse{Choice: req.DenyIndex}, nil
			}
			cancel()
			return InteractionResponse{Choice: req.AllowIndex}, nil
		}))
		res := patchAnchorFile(ctx, root, anchor, "agent")
		cancel()
		if !types.ToolResultFailed(res) {
			t.Fatalf("expected failure, got %s", res)
		}
		assertAnchorFile(t, path, "original\n")
	}
}

func TestAnchorStateResetAndExactSnapshot(t *testing.T) {
	mgr := newTestManager()
	mgr.EnsureContent("file", "original")
	if err := mgr.validateContent("file", "original"); err != nil {
		t.Fatal(err)
	}
	first, _ := mgr.GetAnnotated("file")
	mgr.Reset()
	if len(mgr.files) != 0 {
		t.Fatal("reset retained cached files")
	}
	mgr.EnsureContent("file", "original")
	second, _ := mgr.GetAnnotated("file")
	if first == second {
		t.Fatal("reset reused anchors from the cleared context")
	}
	if err := mgr.validateContent("file", "original\n"); err == nil {
		t.Fatal("final newline change must invalidate the exact snapshot")
	}
}

func TestAnchorStateConcurrentResetAndReads(t *testing.T) {
	mgr := newTestManager()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				mgr.EnsureContent("file", "content\n")
				mgr.GetAnnotated("file")
				mgr.Reset()
			}
		}()
	}
	wg.Wait()
}
