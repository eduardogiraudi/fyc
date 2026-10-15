package tools

import (
	"strings"
	"testing"
)

// ── lineHash ────────────────────────────────────────────────────────────────

func TestLineHash(t *testing.T) {
	h1 := lineHash("package main")
	h2 := lineHash("  package main  ") // trimmed same
	h3 := lineHash("package main // different")
	h4 := lineHash("")

	if h1 != h2 {
		t.Errorf("lineHash should ignore surrounding whitespace: %q vs %q", h1, h2)
	}
	if len(h1) != 2 {
		t.Errorf("lineHash should be exactly 2 hex chars, got %q", h1)
	}
	if h1 == h3 {
		t.Logf("note: hash collision between %q and %q (both %s)",
			"package main", "package main // different", h1)
	}
	if h4 == "" {
		t.Error("lineHash of empty string should not be empty")
	}
}

// ── stripAnchors ────────────────────────────────────────────────────────────

func TestStripAnchors(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Agate:a3§package main", "package main"},
		{"Agate:a3§package main\nBeryl:42§import (", "package main\nimport ("},
		{"plain line without anchor", "plain line without anchor"},
		{"", ""},
		{"Agate:a3§", ""},
		{"§leading delimiter", "leading delimiter"},
	}
	for _, tc := range tests {
		got := stripAnchors(tc.input)
		if got != tc.expected {
			t.Errorf("stripAnchors(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// ── slicesEqual ─────────────────────────────────────────────────────────────

func TestSlicesEqual(t *testing.T) {
	if !slicesEqual(nil, nil) {
		t.Error("nil slices should be equal")
	}
	if !slicesEqual([]string{}, []string{}) {
		t.Error("empty slices should be equal")
	}
	if slicesEqual([]string{"a"}, []string{"b"}) {
		t.Error("different elements should not be equal")
	}
	if slicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Error("different lengths should not be equal")
	}
	if !slicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("identical slices should be equal")
	}
}

// ── lcsDiff ─────────────────────────────────────────────────────────────────

func TestLcsDiff_Equal(t *testing.T) {
	a := []string{"a", "b", "c"}
	ops := myersDiff(a, a)
	for _, op := range ops {
		if op.opType != diffEqual {
			t.Errorf("expected all Equal ops, got %v", op)
		}
	}
	if len(ops) != len(a) {
		t.Errorf("expected %d Equal ops, got %d", len(a), len(ops))
	}
}

func TestLcsDiff_InsertAtBeginning(t *testing.T) {
	old := []string{"b", "c"}
	new := []string{"a", "b", "c"}
	ops := myersDiff(old, new)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d: %+v", len(ops), ops)
	}
	if ops[0].opType != diffInsert || ops[0].text != "a" {
		t.Errorf("first op should be Insert 'a', got %+v", ops[0])
	}
	if ops[1].opType != diffEqual || ops[1].text != "b" {
		t.Errorf("second op should be Equal 'b', got %+v", ops[1])
	}
}

func TestLcsDiff_DeleteAtEnd(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{"a", "b"}
	ops := myersDiff(old, new)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d: %+v", len(ops), ops)
	}
	if ops[2].opType != diffDelete || ops[2].text != "c" {
		t.Errorf("last op should be Delete 'c', got %+v", ops[2])
	}
}

func TestMyersDiff_ReplaceMiddle(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{"a", "X", "c"}
	ops := myersDiff(old, new)
	if len(ops) != 4 {
		t.Fatalf("expected 4 ops, got %d: %+v", len(ops), ops)
	}
	if ops[0].opType != diffEqual || ops[0].text != "a" {
		t.Errorf("op[0] should be Equal 'a', got %+v", ops[0])
	}
	if ops[1].opType != diffDelete || ops[1].text != "b" {
		t.Errorf("op[1] should be Delete 'b', got %+v", ops[1])
	}
	if ops[2].opType != diffInsert || ops[2].text != "X" {
		t.Errorf("op[2] should be Insert 'X', got %+v", ops[2])
	}
	if ops[3].opType != diffEqual || ops[3].text != "c" {
		t.Errorf("op[3] should be Equal 'c', got %+v", ops[3])
	}
}

func TestMyersDiff_OldEmpty(t *testing.T) {
	ops := myersDiff(nil, []string{"a", "b"})
	if len(ops) != 2 {
		t.Fatalf("expected 2 Insert ops, got %d", len(ops))
	}
	for i, op := range ops {
		if op.opType != diffInsert {
			t.Errorf("op[%d] should be Insert, got %+v", i, op)
		}
	}
}

func TestMyersDiff_NewEmpty(t *testing.T) {
	ops := myersDiff([]string{"a", "b"}, nil)
	if len(ops) != 2 {
		t.Fatalf("expected 2 Delete ops, got %d", len(ops))
	}
	for i, op := range ops {
		if op.opType != diffDelete {
			t.Errorf("op[%d] should be Delete, got %+v", i, op)
		}
	}
}

func TestMyersDiff_BothEmpty(t *testing.T) {
	ops := myersDiff(nil, nil)
	if len(ops) != 0 {
		t.Errorf("expected 0 ops for both empty, got %d", len(ops))
	}
}

// ── buildAnnotated ──────────────────────────────────────────────────────────

func TestBuildAnnotated(t *testing.T) {
	fs := &FileState{
		lines:        []string{"package main", "import ("},
		lineToAnchor: map[int]string{0: "Agate", 1: "Beryl"},
		anchorToLine: map[string]int{"Agate": 0, "Beryl": 1},
	}
	out := buildAnnotated(fs)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for i, l := range lines {
		if !strings.Contains(l, ":") || !strings.Contains(l, "§") {
			t.Errorf("line %d: missing anchor delimiter in %q", i, l)
		}
		parts := strings.SplitN(l, "§", 2)
		if len(parts) != 2 {
			t.Errorf("line %d: bad format %q", i, l)
			continue
		}
		if parts[1] != fs.lines[i] {
			t.Errorf("line %d: content mismatch: %q vs %q", i, parts[1], fs.lines[i])
		}
	}
}

// ── AnchorStateManager integration ──────────────────────────────────────────

// newTestManager creates a fresh AnchorStateManager for testing isolation.
func newTestManager() *AnchorStateManager {
	return &AnchorStateManager{
		files: make(map[string]*FileState),
	}
}

func TestEnsureAnchorsAndGetAnnotated(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	lines := []string{"line1", "line2", "line3"}

	mgr.EnsureAnchors(path, lines)

	annotated, err := mgr.GetAnnotated(path)
	if err != nil {
		t.Fatalf("GetAnnotated failed: %v", err)
	}

	outLines := strings.Split(annotated, "\n")
	if len(outLines) != 3 {
		t.Fatalf("expected 3 annotated lines, got %d", len(outLines))
	}

	for i, l := range outLines {
		if !strings.Contains(l, "§") {
			t.Errorf("line %d missing delimiter: %q", i, l)
		}
		parts := strings.SplitN(l, "§", 2)
		if len(parts) != 2 || parts[1] != lines[i] {
			t.Errorf("line %d content wrong: %q", i, l)
		}
	}
}

func TestGetAnnotated_NotLoaded(t *testing.T) {
	mgr := newTestManager()
	_, err := mgr.GetAnnotated("/nonexistent")
	if err == nil {
		t.Error("expected error for unloaded file")
	}
}

func TestPreviewEdit_SingleLine(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	lines := []string{"a", "b", "c"}
	mgr.EnsureAnchors(path, lines)

	fs := mgr.files[path]
	anchor := fs.lineToAnchor[1] + ":" + lineHash(fs.lines[1])

	oldLines, newLines, err := mgr.PreviewEdit(path, anchor, anchor, "X")
	if err != nil {
		t.Fatalf("PreviewEdit failed: %v", err)
	}
	if len(newLines) != 3 {
		t.Fatalf("expected 3 new lines, got %d: %v", len(newLines), newLines)
	}
	if newLines[1] != "X" {
		t.Errorf("expected line 1 to be 'X', got %q", newLines[1])
	}
	if len(oldLines) != 3 || oldLines[1] != "b" {
		t.Errorf("oldLines should be a copy of original, got %v", oldLines)
	}
}

func TestPreviewEdit_Range(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	lines := []string{"a", "b", "c", "d"}
	mgr.EnsureAnchors(path, lines)
	fs := mgr.files[path]

	startA := fs.lineToAnchor[1] + ":" + lineHash(fs.lines[1])
	endA := fs.lineToAnchor[2] + ":" + lineHash(fs.lines[2])

	_, newLines, err := mgr.PreviewEdit(path, startA, endA, "X\nY")
	if err != nil {
		t.Fatalf("PreviewEdit failed: %v", err)
	}
	expected := []string{"a", "X", "Y", "d"}
	if !slicesEqual(newLines, expected) {
		t.Errorf("expected %v, got %v", expected, newLines)
	}
}

func TestPreviewEdit_DeleteRange(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	lines := []string{"a", "b", "c", "d"}
	mgr.EnsureAnchors(path, lines)
	fs := mgr.files[path]

	startA := fs.lineToAnchor[1] + ":" + lineHash(fs.lines[1])
	endA := fs.lineToAnchor[2] + ":" + lineHash(fs.lines[2])

	_, newLines, err := mgr.PreviewEdit(path, startA, endA, "")
	if err != nil {
		t.Fatalf("PreviewEdit failed: %v", err)
	}
	expected := []string{"a", "d"}
	if !slicesEqual(newLines, expected) {
		t.Errorf("expected %v, got %v", expected, newLines)
	}
}

func TestPreviewEdit_InvalidAnchor(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})

	_, _, err := mgr.PreviewEdit(path, "NonExistent:aa", "NonExistent:aa", "X")
	if err == nil {
		t.Error("expected error for invalid anchor")
	}
}

func TestPreviewEdit_ContentMismatch(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	fs := mgr.files[path]
	anchor := fs.lineToAnchor[1] // line "b"

	// Tamper the line content so the hash won't match
	fs.lines[1] = "MODIFIED"

	_, _, err := mgr.PreviewEdit(path, anchor+":", anchor+":", "X")
	if err == nil {
		t.Error("expected error for content mismatch")
	}
}

func TestPreviewEdit_StartAfterEnd(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	fs := mgr.files[path]

	startA := fs.lineToAnchor[2] + ":" // "c"
	endA := fs.lineToAnchor[0] + ":"   // "a"

	_, _, err := mgr.PreviewEdit(path, startA, endA, "X")
	if err == nil {
		t.Error("expected error when start_anchor > end_anchor")
	}
}

func TestCommitEdit_PreservesAnchors(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	original := []string{"a", "b", "c", "d"}
	mgr.EnsureAnchors(path, original)
	fs := mgr.files[path]

	origAnchors := make([]string, 4)
	for i := 0; i < 4; i++ {
		origAnchors[i] = fs.lineToAnchor[i]
	}

	oldLines := original
	newLines := []string{"a", "X", "c", "d"}

	_, insertedOnly := mgr.CommitEdit(path, oldLines, newLines)

	fs2 := mgr.files[path]
	if fs2.lineToAnchor[0] != origAnchors[0] {
		t.Errorf("line 0 should keep anchor %s, got %s", origAnchors[0], fs2.lineToAnchor[0])
	}
	if fs2.lineToAnchor[2] != origAnchors[2] {
		t.Errorf("line 2 should keep anchor %s, got %s", origAnchors[2], fs2.lineToAnchor[2])
	}
	if fs2.lineToAnchor[3] != origAnchors[3] {
		t.Errorf("line 3 should keep anchor %s, got %s", origAnchors[3], fs2.lineToAnchor[3])
	}

	// Line 1 ("X") is new — should have a fresh anchor
	newA := fs2.lineToAnchor[1]
	for _, oldA := range origAnchors {
		if newA == oldA {
			t.Errorf("new line should get a fresh anchor, but got %s which is an old anchor", newA)
		}
	}

	if !strings.Contains(insertedOnly, "X") {
		t.Errorf("insertedOnly should contain 'X', got %q", insertedOnly)
	}
}

func TestCommitEdit_DeleteLines(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	original := []string{"a", "b", "c"}
	mgr.EnsureAnchors(path, original)
	fs := mgr.files[path]
	origA := fs.lineToAnchor[0]

	oldLines := original
	newLines := []string{"a"}

	_, insertedOnly := mgr.CommitEdit(path, oldLines, newLines)

	fs2 := mgr.files[path]
	if fs2.lineToAnchor[0] != origA {
		t.Errorf("line 0 should keep anchor %s, got %s", origA, fs2.lineToAnchor[0])
	}
	if len(fs2.lines) != 1 {
		t.Errorf("expected 1 line after commit, got %d", len(fs2.lines))
	}

	if insertedOnly != "" {
		t.Errorf("expected empty insertedOnly for deletions, got %q", insertedOnly)
	}
}

func TestCommitEdit_InsertLines(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	original := []string{"a", "b"}
	mgr.EnsureAnchors(path, original)

	oldLines := original
	newLines := []string{"a", "X", "Y", "b"}

	_, insertedOnly := mgr.CommitEdit(path, oldLines, newLines)

	fs := mgr.files[path]
	if len(fs.lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(fs.lines))
	}

	insertLines := strings.Split(insertedOnly, "\n")
	if len(insertLines) != 2 {
		t.Errorf("expected 2 inserted lines, got %d: %q", len(insertLines), insertedOnly)
	}
}

func TestCommitEdit_FileNotLoaded(t *testing.T) {
	mgr := newTestManager()
	full, inserted := mgr.CommitEdit("/new/file.go", nil, []string{"hello"})
	if full == "" {
		t.Error("expected non-empty fullAnnotated")
	}
	if inserted == "" {
		t.Error("expected non-empty insertedOnly for brand new file")
	}
}

// ── EnsureAnchors ───────────────────────────────────────────────────────────

func TestEnsureAnchors_Fresh(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b"})

	annotated, err := mgr.GetAnnotated(path)
	if err != nil {
		t.Fatalf("GetAnnotated failed: %v", err)
	}
	lines := strings.Split(annotated, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 annotated lines, got %d", len(lines))
	}
}

func TestEnsureAnchors_ReuseOnSameContent(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b"})

	firstPoolIdx := mgr.poolIndex

	mgr.EnsureAnchors(path, []string{"a", "b"})

	if mgr.poolIndex != firstPoolIdx {
		t.Errorf("pool index should not advance on same content: was %d, now %d",
			firstPoolIdx, mgr.poolIndex)
	}
}

func TestEnsureAnchors_ReconcileOnChangedContent(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})

	fs := mgr.files[path]
	anchorA := fs.lineToAnchor[0]
	anchorB := fs.lineToAnchor[1]

	mgr.EnsureAnchors(path, []string{"a", "X", "c"})

	fs2 := mgr.files[path]
	if fs2.lineToAnchor[0] != anchorA {
		t.Errorf("anchor for 'a' should be preserved, got %s", fs2.lineToAnchor[0])
	}
	if len(fs2.lines) > 2 && fs2.lineToAnchor[2] != fs.lineToAnchor[2] {
		t.Errorf("anchor for 'c' should be preserved")
	}
	if fs2.lineToAnchor[1] == anchorB {
		t.Errorf("new line 'X' should NOT reuse old anchor for 'b'")
	}
}

// ── resolveAnchor edge cases ────────────────────────────────────────────────

func TestResolveAnchor_BareWord(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"alpha", "beta", "gamma"})
	fs := mgr.files[path]
	anchor2 := fs.lineToAnchor[2] // bare word, no hash

	_, newLines, err := mgr.PreviewEdit(path, anchor2, anchor2, "replaced")
	if err != nil {
		t.Fatalf("bare-word PreviewEdit failed: %v", err)
	}
	if newLines[2] != "replaced" {
		t.Errorf("expected line 2 to be 'replaced', got %q", newLines[2])
	}
}

func TestResolveAnchor_UnknownWord(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a"})

	_, _, err := mgr.PreviewEdit(path, "ZZNotAWord:aa", "ZZNotAWord:aa", "X")
	if err == nil {
		t.Error("expected error for unknown anchor word")
	}
}
