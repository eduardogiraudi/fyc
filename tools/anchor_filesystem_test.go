package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
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
	}, root, nil).Text
}

func assertAnchorFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file = %q, %v; want %q", data, err, want)
	}
}

var nowAnchorRe = regexp.MustCompile(`\(now ([a-z0-9]+)\)`)

func TestReplaceAnchorsExternalChanges(t *testing.T) {
	const original = "first\ntarget\nlast\n"
	for _, tc := range []struct {
		name, external, replacement string
		// target: the anchored line itself changed; neighbour: only a bordering line changed;
		// newline: only the final newline changed.
		kind string
		want string // expected substring of the refusal
	}{
		{"target changed", "first\nuser\nlast\n", "agent", "target", `expected "target" but got "user"`},
		{"target removed", "first\nlast\n", "agent", "target", `line removed on disk: expected "target"`},
		{"whitespace", "first\n  target\nlast\n", "agent", "target", `but got "  target"`},
		{"line endings", "first\r\ntarget\r\nlast\r\n", "agent", "target", `expected "target"`},
		{"unrelated line", "user\ntarget\nlast\n", "agent", "neighbour", "neighbouring lines changed on disk"},
		{"insertion", "first\ninserted\ntarget\nlast\n", "agent", "neighbour", "inserted"},
		{"deletion", "target\nlast\n", "agent", "neighbour", "neighbouring lines changed on disk"},
		{"stale no-op", "user\ntarget\nlast\n", "target", "neighbour", "neighbouring lines changed on disk"},
		{"final newline", "first\ntarget\nlast", "agent", "newline", "get_anchors again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := setupAnchorFile(t, original)
			anchor := readAnchorToken(t, root, 1)
			writeAnchorTestFile(t, path, tc.external)
			called := false
			SetInteractor(anchorApprovalFunc(func(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
				called = true
				return InteractionResponse{Choice: req.AllowIndex}, nil
			}))
			res := patchAnchorFile(context.Background(), root, anchor, tc.replacement)
			if !types.ToolResultFailed(res) || !strings.Contains(res, "on disk") || !strings.Contains(res, tc.want) || !strings.Contains(res, "no changes written") {
				t.Fatalf("expected actionable conflict containing %q, got %s", tc.want, res)
			}
			if called {
				t.Fatal("stale diff was offered for approval")
			}
			assertAnchorFile(t, path, tc.external)

			switch tc.kind {
			case "newline":
				if _, err := globalAnchorState.GetAnnotated(path); err == nil {
					t.Fatal("a final-newline conflict must drop the cache and require a re-read")
				}
				fresh := readAnchorToken(t, root, 1)
				if res = patchAnchorFile(context.Background(), root, fresh, "reconsidered"); !strings.HasPrefix(res, "updated") {
					t.Fatalf("patch after explicit re-read: %s", res)
				}
			case "neighbour":
				// The warning is shown once; the same anchor then works without a re-read.
				res = patchAnchorFile(context.Background(), root, anchor, tc.replacement)
				if !strings.HasPrefix(res, "updated") && !strings.HasPrefix(res, "no changes needed") {
					t.Fatalf("retry after the neighbour warning: %s", res)
				}
			case "target":
				// A retired anchor stays refused, and the refusal names the way forward.
				if again := patchAnchorFile(context.Background(), root, anchor, tc.replacement); !types.ToolResultFailed(again) {
					t.Fatalf("retired anchor accepted on retry: %s", again)
				}
				fresh := ""
				if m := nowAnchorRe.FindStringSubmatch(res); m != nil {
					fresh = m[1]
				} else {
					fresh = readAnchorToken(t, root, 1)
				}
				if res = patchAnchorFile(context.Background(), root, fresh, "reconsidered"); !strings.HasPrefix(res, "updated") {
					t.Fatalf("patch with the anchor from the refusal: %s", res)
				}
			}
		})
	}
}

func TestReplaceAnchorsDistantExternalChangeDoesNotBlock(t *testing.T) {
	root, path := setupAnchorFile(t, "a\nb\ntarget\nd\ne\n")
	anchor := readAnchorToken(t, root, 2)
	writeAnchorTestFile(t, path, "A\nb\ntarget\nd\ne\n")
	if res := patchAnchorFile(context.Background(), root, anchor, "agent"); !strings.HasPrefix(res, "updated") {
		t.Fatalf("an unrelated external edit away from the target must not block the patch: %s", res)
	}
	assertAnchorFile(t, path, "A\nb\nagent\nd\ne\n")
}

func TestReplaceAnchorsWritesSeparatorContentVerbatim(t *testing.T) {
	for _, tc := range []struct{ name, replacement string }{
		{"leading separator", "§ Section 3"},
		{"separator in a string", `x := "a § b"`},
		{"lone separator", "§"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := setupAnchorFile(t, "first\ntarget\nlast\n")
			anchor := readAnchorToken(t, root, 1)
			if res := patchAnchorFile(context.Background(), root, anchor, tc.replacement); !strings.HasPrefix(res, "updated") {
				t.Fatalf("patch failed: %s", res)
			}
			assertAnchorFile(t, path, "first\n"+tc.replacement+"\nlast\n")
		})
	}
}

func TestReplaceAnchorsStripsCopiedPrefixes(t *testing.T) {
	root, path := setupAnchorFile(t, "first\ntarget\nlast\n")
	first := readAnchorToken(t, root, 0)
	target := readAnchorToken(t, root, 1)
	res := executeReplaceAnchorsTool(context.Background(), map[string]any{
		"path": "file.txt", "start_anchor": first, "end_anchor": target,
		"replacement": first + "§first\n§ added\nnew",
	}, root, nil).Text
	if !strings.HasPrefix(res, "updated") {
		t.Fatalf("patch failed: %s", res)
	}
	assertAnchorFile(t, path, "first\n§ added\nnew\nlast\n")
}

func TestGetAnchorsPrintsTokensWithCheckCharacters(t *testing.T) {
	root, _ := setupAnchorFile(t, "first\ntarget\nlast\n")
	out := executeGetAnchorsTool(map[string]any{"path": "file.txt"}, root, nil)
	for i, line := range strings.Split(out, "\n") {
		tok, _, ok := strings.Cut(line, anchorDelimiter)
		if !ok {
			t.Fatalf("line %d has no separator: %q", i, line)
		}
		if _, valid := splitAnchorToken(tok); !valid {
			t.Fatalf("line %d: %q is not a token with a valid check", i, tok)
		}
	}
}

func TestReplaceAnchorsRefusesWrongCheckCharacters(t *testing.T) {
	root, path := setupAnchorFile(t, "first\ntarget\nlast\n")
	tok := readAnchorToken(t, root, 1)
	id := tok[:len(tok)-anchorCheckLen]
	called := false
	SetInteractor(anchorApprovalFunc(func(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
		called = true
		return InteractionResponse{Choice: req.AllowIndex}, nil
	}))
	for name, bad := range map[string]string{
		"wrong check":      id + wrongCheck(id),
		"id only":          id,
		"truncated token":  tok[:len(tok)-1],
		"legacy word:hash": "Agate:a3",
	} {
		res := patchAnchorFile(context.Background(), root, bad, "agent")
		if !types.ToolResultFailed(res) || !strings.Contains(res, "no changes written") {
			t.Fatalf("%s: expected a refusal, got %s", name, res)
		}
		assertAnchorFile(t, path, "first\ntarget\nlast\n")
	}
	if called {
		t.Fatal("a refused anchor must never reach the approval prompt")
	}
	if res := patchAnchorFile(context.Background(), root, tok, "agent"); !strings.HasPrefix(res, "updated") {
		t.Fatalf("the right token must work after the refusals: %s", res)
	}
	assertAnchorFile(t, path, "first\nagent\nlast\n")
}

func TestReplaceAnchorsIdenticalLinesKeepTheirOwnAnchors(t *testing.T) {
	root, path := setupAnchorFile(t, "a\nx\nx\nb\n")
	first := readAnchorToken(t, root, 1)
	second := readAnchorToken(t, root, 2)

	if res := patchAnchorFile(context.Background(), root, first, "y"); !strings.HasPrefix(res, "updated") {
		t.Fatalf("first patch: %s", res)
	}
	// The second x was never touched: its anchor must still work, with no re-read.
	if res := patchAnchorFile(context.Background(), root, second, "z"); !strings.HasPrefix(res, "updated") {
		t.Fatalf("anchor of the untouched identical line was refused: %s", res)
	}
	assertAnchorFile(t, path, "a\ny\nz\nb\n")
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
	if err := mgr.syncWithDisk("file", "original"); err != nil {
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
	if err := mgr.syncWithDisk("file", "original\n"); err == nil {
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

func TestReplaceAnchorsSuccessSummary(t *testing.T) {
	for _, tc := range []struct {
		name, content, replacement, summary, want string
	}{
		{"replace", "first\ntarget\nlast\n", "changed", "updated (+1 -1 lines)", "first\nchanged\nlast\n"},
		{"insert", "first\ntarget\nlast\n", "target\nadded", "updated (+1 -0 lines)", "first\ntarget\nadded\nlast\n"},
		{"delete", "first\ntarget\nlast\n", "", "updated (+0 -1 lines)", "first\nlast\n"},
		{"unchanged", "first\ntarget\nlast\n", "target", "no changes needed", "first\ntarget\nlast\n"},
		{"empty line", "first\ntarget\nlast\n", "target\n\nadded", "updated (+2 -0 lines)", "first\ntarget\n\nadded\nlast\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := setupAnchorFile(t, tc.content)
			anchor := readAnchorToken(t, root, 1)
			res := patchAnchorFile(context.Background(), root, anchor, tc.replacement)
			if !strings.HasPrefix(res, tc.summary) {
				t.Fatalf("result = %q; want summary %q", res, tc.summary)
			}
			assertAnchorFile(t, path, tc.want)
		})
	}
}
