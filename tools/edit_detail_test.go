package tools

import (
	"context"
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

// A huge patch is capped: the detail is a hint, not the whole file.
func TestEditDetailCapsAndSeparatesHunks(t *testing.T) {
	var ops []diffOp
	for i := 0; i < 60; i++ {
		ops = append(ops, diffOp{diffEqual, "line"})
	}
	ops = append(ops, diffOp{diffDelete, "old"}, diffOp{diffInsert, "new"})
	for i := 0; i < 60; i++ {
		ops = append(ops, diffOp{diffEqual, "tail"})
	}
	detail := editDetail(ops)
	lines := strings.Split(detail, "\n")
	if !strings.Contains(detail, "- 61 old") || !strings.Contains(detail, "+ 61 new") {
		t.Fatalf("the change is missing its line numbers: %q", detail)
	}
	if !strings.Contains(detail, "  59 line") {
		t.Fatalf("the context lines lost their numbers: %q", detail)
	}
	if len(lines) > editDetailMaxRows+1 {
		t.Fatalf("detail was not capped: %d rows", len(lines))
	}
}
