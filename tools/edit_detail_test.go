package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A successful anchored edit reports what it removed and what it added, with a
// couple of context lines, as a UI-only detail: the model still gets the anchors.
func TestReplaceAnchorsReportsTheChangedLines(t *testing.T) {
	root, _ := setupAnchorFile(t, "first\nsecond\nthird\nfourth\n")
	second := readAnchorToken(t, root, 1)

	res := executeReplaceAnchorsTool(context.Background(), map[string]any{
		"path": "file.txt", "start_anchor": second, "end_anchor": second, "replacement": "TWO",
	}, root, nil)
	if !strings.HasPrefix(res.Text, "updated (+1 -1 lines)") {
		t.Fatalf("patch failed: %s", res.Text)
	}
	want := " 1 first\n-2 second\n+2 TWO\n 3 third\n 4 fourth"
	if res.Detail != want {
		t.Fatalf("detail = %q, want %q", res.Detail, want)
	}
}

// Nothing changed means nothing to show, and a failure must not pretend a patch
// happened.
func TestReplaceAnchorsDetailOnlyOnSuccess(t *testing.T) {
	root, _ := setupAnchorFile(t, "first\nsecond\n")
	second := readAnchorToken(t, root, 1)

	same := executeReplaceAnchorsTool(context.Background(), map[string]any{
		"path": "file.txt", "start_anchor": second, "end_anchor": second, "replacement": "second",
	}, root, nil)
	if same.Detail != "" {
		t.Fatalf("a no-op edit produced a detail: %q", same.Detail)
	}

	failed := executeReplaceAnchorsTool(context.Background(), map[string]any{
		"path": "missing.txt", "start_anchor": "aaaaa", "end_anchor": "aaaaa", "replacement": "x",
	}, root, nil)
	if failed.Detail != "" || !strings.HasPrefix(failed.Text, "error:") {
		t.Fatalf("a failed edit produced %#v", failed)
	}
}

// A huge patch is shown whole — the chat keeps it collapsed until the user
// clicks, so a cut would hide part of it forever — while distant hunks stay
// separated by a gap mark and keep their line numbers.
func TestEditDetailShowsTheWholePatchAndSeparatesHunks(t *testing.T) {
	var ops []diffOp
	for i := 0; i < 60; i++ {
		ops = append(ops, diffOp{diffEqual, "line"})
	}
	ops = append(ops, diffOp{diffDelete, "old"}, diffOp{diffInsert, "new"})
	for i := 0; i < 60; i++ {
		ops = append(ops, diffOp{diffEqual, "tail"})
	}
	for i := 0; i < 40; i++ {
		ops = append(ops, diffOp{diffInsert, fmt.Sprintf("added %d", i)})
	}
	detail := editDetail(ops)
	if !strings.Contains(detail, "- 61 old") || !strings.Contains(detail, "+ 61 new") {
		t.Fatalf("the change is missing its line numbers: %q", detail)
	}
	if !strings.Contains(detail, "  59 line") {
		t.Fatalf("the context lines lost their numbers: %q", detail)
	}
	if !strings.Contains(detail, "⋯") {
		t.Fatalf("the hunks are not separated by a gap mark: %q", detail)
	}
	if !strings.Contains(detail, "added 39") {
		t.Fatalf("the tail of the patch was cut: %q", detail)
	}
	if strings.Contains(detail, "more changed lines") {
		t.Fatalf("the detail hid part of the patch: %q", detail)
	}
}
