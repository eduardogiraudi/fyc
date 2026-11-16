package tools

import (
	"strings"
	"testing"
)

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
		lineToAnchor: map[int]string{0: "k7f", 1: "m2n"},
		anchorToLine: map[string]int{"k7f": 0, "m2n": 1},
	}
	want := anchorToken("k7f") + "§package main\n" + anchorToken("m2n") + "§import ("
	if got := buildAnnotated(fs); got != want {
		t.Fatalf("buildAnnotated = %q, want %q", got, want)
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
	anchor := anchorToken(fs.lineToAnchor[1])

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

	startA := anchorToken(fs.lineToAnchor[1])
	endA := anchorToken(fs.lineToAnchor[2])

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

	startA := anchorToken(fs.lineToAnchor[1])
	endA := anchorToken(fs.lineToAnchor[2])

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

func TestPreviewEdit_RefusesAnchorChangedOnDisk(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c", "d", "e"})
	anchor := anchorToken(mgr.files[path].lineToAnchor[3]) // "d"

	if err := mgr.syncWithDisk(path, JoinLines([]string{"a", "b", "c", "D2", "e"})); err != nil {
		t.Fatal(err)
	}
	_, _, err := mgr.PreviewEdit(path, anchor, anchor, "X")
	if err == nil {
		t.Fatal("expected a stale-anchor error")
	}
	for _, want := range []string{anchor, `expected "d"`, `but got "D2"`, "no changes written"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestPreviewEdit_StartAfterEnd(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	fs := mgr.files[path]

	startA := anchorToken(fs.lineToAnchor[2]) // "c"
	endA := anchorToken(fs.lineToAnchor[0])   // "a"

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

	_, insertedOnly := mgr.CommitEdit(path, editRange{1, 1, 1}, oldLines, newLines)

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

	_, insertedOnly := mgr.CommitEdit(path, editRange{1, 2, 0}, oldLines, newLines)

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

	// "Insert after a": replace line 0 with itself plus two new lines.
	_, insertedOnly := mgr.CommitEdit(path, editRange{0, 0, 3}, oldLines, newLines)

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
	full, inserted := mgr.CommitEdit("/new/file.go", editRange{}, nil, []string{"hello"})
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

	firstIssued := mgr.issued

	mgr.EnsureAnchors(path, []string{"a", "b"})

	if mgr.issued != firstIssued {
		t.Errorf("issued counter should not advance on same content: was %d, now %d",
			firstIssued, mgr.issued)
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

func TestResolveAnchor_Token(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"alpha", "beta", "gamma"})
	fs := mgr.files[path]
	anchor2 := anchorToken(fs.lineToAnchor[2]) // id + check characters

	_, newLines, err := mgr.PreviewEdit(path, anchor2, anchor2, "replaced")
	if err != nil {
		t.Fatalf("PreviewEdit with a full token failed: %v", err)
	}
	if newLines[2] != "replaced" {
		t.Errorf("expected line 2 to be 'replaced', got %q", newLines[2])
	}
}

func TestResolveAnchor_UnknownWord(t *testing.T) {
	mgr := newTestManager()
	path := "/test/file.go"
	mgr.EnsureAnchors(path, []string{"a"})

	_, _, err := mgr.PreviewEdit(path, "zzzzz", "zzzzz", "X")
	if err == nil {
		t.Error("expected error for unknown anchor word")
	}
}

// ── Anchor ids ──────────────────────────────────────────────────────────────

func TestAnchorID_UniqueAndShaped(t *testing.T) {
	seen := make(map[string]uint64, 70000)
	for n := uint64(0); n < 70000; n++ {
		id := anchorID(n)
		if !isAnchorShape(id) {
			t.Fatalf("anchorID(%d) = %q is not a valid id", n, id)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("anchorID(%d) == anchorID(%d) == %q", n, prev, id)
		}
		seen[id] = n
		wantLen := 3
		if n >= 29791 {
			wantLen = 4
		}
		if len(id) != wantLen {
			t.Fatalf("anchorID(%d) = %q, want length %d", n, id, wantLen)
		}
	}
}

func TestAnchorID_NotSequential(t *testing.T) {
	first := map[byte]bool{}
	for n := uint64(0); n < 31; n++ {
		first[anchorID(n)[0]] = true
	}
	if len(first) < 15 {
		t.Errorf("first characters of 31 consecutive ids cover only %d values; ids look sequential", len(first))
	}
}

func TestAnchorID_AvoidsLookAlikes(t *testing.T) {
	for n := uint64(0); n < 5000; n++ {
		if strings.ContainsAny(anchorID(n), "il01o") {
			t.Fatalf("anchorID(%d) = %q contains a look-alike character", n, anchorID(n))
		}
	}
}

// ── Separator handling ──────────────────────────────────────────────────────

func TestReplacementKeepsSeparatorInContent(t *testing.T) {
	mgr := newTestManager()
	path := "/test/doc.md"
	mgr.EnsureAnchors(path, []string{"# Title", "body", "end"})
	anchor := anchorToken(mgr.files[path].lineToAnchor[1])
	knownID := mgr.files[path].lineToAnchor[0]
	known := anchorToken(knownID)

	for _, tc := range []struct{ name, in, want string }{
		{"line starting with separator", "§ Section 3", "§ Section 3"},
		{"separator inside a string", `x := "a § b"`, `x := "a § b"`},
		{"only separator", "§", "§"},
		{"shape of an id but unknown", "abc§text", "abc§text"},
		{"known token prefix is stripped", known + "§# Title", "# Title"},
		{"known token prefix in capitals is stripped", strings.ToUpper(known) + "§# Title", "# Title"},
		{"known id with wrong check is kept", knownID + wrongCheck(knownID) + "§# Title", knownID + wrongCheck(knownID) + "§# Title"},
		{"known bare id without check is kept", knownID + "§# Title", knownID + "§# Title"},
		{"mixed copied and new lines", known + "§# Title\n§ new", "# Title\n§ new"},
		{"known id followed by a separator line", known + "§§ Section", "§ Section"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, newLines, err := mgr.PreviewEdit(path, anchor, anchor, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(newLines[1:len(newLines)-1], "\n")
			if got != tc.want {
				t.Errorf("replacement written as %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewEdit_ToleratesFullAnnotatedAnchors(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	id := anchorToken(mgr.files[path].lineToAnchor[1])
	for _, form := range []string{id, id + "§b", " " + strings.ToUpper(id) + " "} {
		_, newLines, err := mgr.PreviewEdit(path, form, form, "X")
		if err != nil || newLines[1] != "X" {
			t.Errorf("form %q: newLines=%v err=%v", form, newLines, err)
		}
	}
}

func TestPreviewEdit_ReplacementNewlines(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	id := anchorToken(mgr.files[path].lineToAnchor[1])
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"X", []string{"a", "X", "c"}},
		{"X\n", []string{"a", "X", "c"}},       // one trailing newline is not a line
		{"X\n\n", []string{"a", "X", "", "c"}}, // a second one is a blank line
		{"\n", []string{"a", "", "c"}},         // a lone newline is one blank line
		{"", []string{"a", "c"}},               // empty means delete
	} {
		_, newLines, err := mgr.PreviewEdit(path, id, id, tc.in)
		if err != nil || !slicesEqual(newLines, tc.want) {
			t.Errorf("replacement %q: got %q err=%v, want %q", tc.in, newLines, err, tc.want)
		}
	}
}

// ── External changes: expected / got ────────────────────────────────────────

func TestSync_RetiredAnchorReportsExpectedAndGot(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c", "d", "e", "f"})
	fs := mgr.files[path]
	changed, removed := fs.lineToAnchor[1], fs.lineToAnchor[4]

	if err := mgr.syncWithDisk(path, JoinLines([]string{"a", "B2", "c", "d", "f"})); err != nil {
		t.Fatal(err)
	}
	if st := fs.stale[changed]; !strings.Contains(st.msg, `expected "b" but got "B2"`) || len(st.reveals) != 1 {
		t.Errorf("changed line: %+v", st)
	}
	if st := fs.stale[removed]; !strings.Contains(st.msg, `line removed on disk: expected "e"`) {
		t.Errorf("removed line: %+v", st)
	}
	// The anchor named in the message must be usable straight away.
	now := fs.stale[changed].reveals[0]
	_, err := func() ([]string, error) {
		_, nl, err := mgr.PreviewEdit(path, anchorToken(changed), anchorToken(changed), "X")
		return nl, err
	}()
	if err == nil || !strings.Contains(err.Error(), "(now "+anchorToken(now)+")") {
		t.Fatalf("expected a stale error naming %s, got %v", anchorToken(now), err)
	}
	if _, nl, err := mgr.PreviewEdit(path, anchorToken(now), anchorToken(now), "X"); err != nil || nl[1] != "X" {
		t.Fatalf("anchor from the message not usable: %v %v", nl, err)
	}
}

func TestSync_NeighbourWarningIsOneShot(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c", "d", "e"})
	fs := mgr.files[path]
	before, after, far := anchorToken(fs.lineToAnchor[1]), anchorToken(fs.lineToAnchor[3]), anchorToken(fs.lineToAnchor[0])

	// External edit changes line "c" only; "b" and "d" border the change.
	if err := mgr.syncWithDisk(path, JoinLines([]string{"a", "b", "C2", "d", "e"})); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.PreviewEdit(path, far, far, "X"); err != nil {
		t.Fatalf("distant anchor must stay usable: %v", err)
	}
	for _, id := range []string{before, after} {
		_, _, err := mgr.PreviewEdit(path, id, id, "X")
		if err == nil || !strings.Contains(err.Error(), "neighbouring lines changed on disk") {
			t.Fatalf("anchor %s next to the change should warn once, got %v", id, err)
		}
		if _, _, err := mgr.PreviewEdit(path, id, id, "X"); err != nil {
			t.Fatalf("retry after the warning must pass: %v", err)
		}
	}
}

func TestPreviewEdit_RangeCannotSwallowUnseenLines(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c", "d"})
	fs := mgr.files[path]
	start, end := anchorToken(fs.lineToAnchor[0]), anchorToken(fs.lineToAnchor[3])

	if err := mgr.syncWithDisk(path, JoinLines([]string{"a", "b", "SECRET", "c", "d"})); err != nil {
		t.Fatal(err)
	}
	_, _, err := mgr.PreviewEdit(path, start, end, "all new")
	if err == nil || !strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("range over an unseen line must be refused and show it, got %v", err)
	}
	if _, _, err := mgr.PreviewEdit(path, start, end, "all new"); err != nil {
		t.Fatalf("retry after the warning must pass: %v", err)
	}
}

func TestMarkSeen_ClearsUnseenAndWarnings(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	fs := mgr.files[path]
	start, end := anchorToken(fs.lineToAnchor[0]), anchorToken(fs.lineToAnchor[2])

	_ = mgr.syncWithDisk(path, JoinLines([]string{"a", "NEW", "b", "c"}))
	full, _ := mgr.GetAnnotated(path)
	mgr.MarkSeen(path, full)
	if _, _, err := mgr.PreviewEdit(path, start, end, "X"); err != nil {
		t.Fatalf("after a full re-read nothing should be pending: %v", err)
	}
}

func TestSync_FinalNewlineOnlyRequiresReread(t *testing.T) {
	mgr := newTestManager()
	mgr.EnsureContent("file", "original\n")
	if err := mgr.syncWithDisk("file", "original"); err == nil {
		t.Fatal("a final-newline-only change must drop the cache and require get_anchors")
	}
	if _, err := mgr.GetAnnotated("file"); err == nil {
		t.Fatal("cache must be dropped")
	}
}

func TestCommitEdit_ConsumedAnchorsAreRefused(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b"})
	old := anchorToken(mgr.files[path].lineToAnchor[1])
	mgr.CommitEdit(path, editRange{1, 1, 1}, []string{"a", "b"}, []string{"a", "X"})
	_, _, err := mgr.PreviewEdit(path, old, old, "Y")
	if err == nil || !strings.Contains(err.Error(), "earlier edit") {
		t.Fatalf("consumed anchor must be refused with an explanation, got %v", err)
	}
}

// ── Check characters ────────────────────────────────────────────────────────

// wrongCheck returns two characters that are not the check of id.
func wrongCheck(id string) string {
	for _, c := range []string{"aa", "bb", "cc"} {
		if c != anchorCheck(id) {
			return c
		}
	}
	return "dd"
}

func TestAnchorToken_RoundTripAndShape(t *testing.T) {
	for n := uint64(0); n < 40000; n += 7 {
		id := anchorID(n)
		tok := anchorToken(id)
		if len(tok) != len(id)+anchorCheckLen {
			t.Fatalf("token %q for id %q has the wrong length", tok, id)
		}
		if strings.ContainsAny(tok, "il01o") {
			t.Fatalf("token %q contains a look-alike character", tok)
		}
		got, ok := splitAnchorToken(tok)
		if !ok || got != id {
			t.Fatalf("splitAnchorToken(%q) = %q, %v; want %q, true", tok, got, ok, id)
		}
	}
}

func TestAnchorCheck_CatchesEveryOtherLinesToken(t *testing.T) {
	// The point of the check: the id of another valid line plus the check
	// characters of this one must not resolve. Over 3-character ids, the
	// check of one id matches another id's only for about 1 pair in 961.
	const ids = 600
	hits := 0
	for a := uint64(0); a < ids; a++ {
		idA := anchorID(a)
		for b := uint64(0); b < ids; b++ {
			if a == b {
				continue
			}
			if _, ok := splitAnchorToken(anchorID(b) + anchorCheck(idA)); ok {
				hits++
			}
		}
	}
	pairs := ids * (ids - 1)
	if rate := float64(hits) / float64(pairs); rate > 3.0/961 {
		t.Fatalf("check lets through %.4f of mismatched tokens (%d of %d), expected about 1/961", rate, hits, pairs)
	}
}

func TestPreviewEdit_RefusesWrongCheck(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	id := mgr.files[path].lineToAnchor[1]
	good := anchorToken(id)
	bad := id + wrongCheck(id)

	_, _, err := mgr.PreviewEdit(path, bad, bad, "X")
	if err == nil || !strings.Contains(err.Error(), "wrong check characters") || !strings.Contains(err.Error(), "no changes written") {
		t.Fatalf("wrong check must be refused without writing, got %v", err)
	}
	if strings.Contains(err.Error(), good) {
		t.Fatalf("the refusal must not reveal the correct token %q: %v", good, err)
	}
	// Either end of a range is checked on its own.
	if _, _, err := mgr.PreviewEdit(path, good, bad, "X"); err == nil || !strings.Contains(err.Error(), "end_anchor") {
		t.Fatalf("bad end anchor must be refused, got %v", err)
	}
	if _, _, err := mgr.PreviewEdit(path, bad, good, "X"); err == nil || !strings.Contains(err.Error(), "start_anchor") {
		t.Fatalf("bad start anchor must be refused, got %v", err)
	}
	if _, nl, err := mgr.PreviewEdit(path, good, good, "X"); err != nil || nl[1] != "X" {
		t.Fatalf("the right token must still work: %v %v", nl, err)
	}
}

func TestPreviewEdit_WrongCheckDoesNotHitAnotherLine(t *testing.T) {
	// A token made of line 2's id with line 1's check characters is refused
	// instead of landing on line 2 (or on line 1).
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	fs := mgr.files[path]
	mixed := fs.lineToAnchor[2] + anchorCheck(fs.lineToAnchor[1])
	if _, _, err := mgr.PreviewEdit(path, mixed, mixed, "X"); err == nil {
		t.Fatal("an id with another line's check characters must be refused")
	}
}

func TestPreviewEdit_BareIDWithoutCheckIsRefusedWithHint(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	id := mgr.files[path].lineToAnchor[1]
	_, _, err := mgr.PreviewEdit(path, id, id, "X")
	if err == nil || !strings.Contains(err.Error(), "missing its 2 check characters") || !strings.Contains(err.Error(), anchorToken(id)) {
		t.Fatalf("a bare id must be refused and the full token named, got %v", err)
	}
}

func TestPreviewEdit_LegacyWordHashIsNotAnAnchor(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	id := mgr.files[path].lineToAnchor[1]
	form := id + ":a3"
	if _, _, err := mgr.PreviewEdit(path, form, form, "X"); err == nil {
		t.Fatal("the old word:hash form must not be accepted")
	}
}

func TestPreviewEdit_BadTokenOnBothEndsReportsOnce(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	bad := "zzzzz"
	_, _, err := mgr.PreviewEdit(path, bad, bad, "X")
	if err == nil || strings.Contains(err.Error(), "end_anchor") {
		t.Fatalf("the same bad anchor on both ends should be reported once, got %v", err)
	}
}

func TestMarkSeen_IgnoresLinesWithBadToken(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	fs := mgr.files[path]
	start, end := anchorToken(fs.lineToAnchor[0]), anchorToken(fs.lineToAnchor[2])

	_ = mgr.syncWithDisk(path, JoinLines([]string{"a", "NEW", "b", "c"}))
	newID := fs.lineToAnchor[1]
	// A line printed with a corrupted token must not count as shown.
	mgr.MarkSeen(path, newID+wrongCheck(newID)+"§NEW")
	if _, _, err := mgr.PreviewEdit(path, start, end, "X"); err == nil || !strings.Contains(err.Error(), "NEW") {
		t.Fatalf("a line shown with a bad token must still count as unseen, got %v", err)
	}
}

// ── Identical neighbouring lines keep their own anchors ─────────────────────

// patch runs PlanEdit + CommitEdit the way replace_anchors does.
func patch(t *testing.T, mgr *AnchorStateManager, path, anchor, replacement string) string {
	t.Helper()
	old, nw, r, err := mgr.PlanEdit(path, anchor, anchor, replacement)
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	_, inserted := mgr.CommitEdit(path, r, old, nw)
	return inserted
}

func TestCommitEdit_IdenticalNeighboursDoNotSwapAnchors(t *testing.T) {
	// [a, x, x, b]: the model edits the FIRST x. The second x was not touched,
	// so it must keep its anchor, and the consumed one must not reappear on it.
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "x", "x", "b"})
	fs := mgr.files[path]
	first, second := fs.lineToAnchor[1], fs.lineToAnchor[2]

	inserted := patch(t, mgr, path, anchorToken(first), "y")

	fs = mgr.files[path]
	if got := fs.lineToAnchor[2]; got != second {
		t.Fatalf("untouched twin changed anchor: %s -> %s", second, got)
	}
	if _, live := fs.anchorToLine[first]; live {
		t.Fatalf("consumed anchor %s must not point at any line", first)
	}
	if !strings.HasSuffix(inserted, "§y") || strings.Count(inserted, "\n") != 0 {
		t.Fatalf("only the new line should be returned, got %q", inserted)
	}
	// The anchor the model already holds for the untouched twin still works.
	if _, nl, err := mgr.PreviewEdit(path, anchorToken(second), anchorToken(second), "Z"); err != nil || nl[2] != "Z" {
		t.Fatalf("untouched twin's anchor refused: %v %v", nl, err)
	}
	// The consumed one is refused with the usual explanation.
	if _, _, err := mgr.PreviewEdit(path, anchorToken(first), anchorToken(first), "Q"); err == nil || !strings.Contains(err.Error(), "earlier edit") {
		t.Fatalf("consumed anchor must be refused, got %v", err)
	}
}

func TestCommitEdit_DeletingOneTwinKeepsTheOther(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "}", "}", "b"})
	fs := mgr.files[path]
	first, second := fs.lineToAnchor[1], fs.lineToAnchor[2]

	if inserted := patch(t, mgr, path, anchorToken(first), ""); inserted != "" {
		t.Fatalf("a deletion returns no anchors, got %q", inserted)
	}
	fs = mgr.files[path]
	if len(fs.lines) != 3 || fs.lineToAnchor[1] != second {
		t.Fatalf("the surviving brace must keep its own anchor %s, got lines=%v anchors=%v", second, fs.lines, fs.lineToAnchor)
	}
	if _, live := fs.anchorToLine[first]; live {
		t.Fatal("the deleted line's anchor must be gone")
	}
}

func TestCommitEdit_DuplicatingALineKeepsTheOriginalAnchor(t *testing.T) {
	mgr := newTestManager()
	path := "/test/f.go"
	mgr.EnsureAnchors(path, []string{"a", "x", "b"})
	orig := mgr.files[path].lineToAnchor[1]

	inserted := patch(t, mgr, path, anchorToken(orig), "x\nx")

	fs := mgr.files[path]
	if fs.lineToAnchor[1] != orig {
		t.Fatalf("the line that stayed identical must keep its anchor, got %s", fs.lineToAnchor[1])
	}
	if strings.Count(inserted, "\n") != 0 || !strings.HasSuffix(inserted, "§x") {
		t.Fatalf("only the added copy should be returned, got %q", inserted)
	}
}

func TestEditOps_FallsBackOnInconsistentRange(t *testing.T) {
	old := []string{"a", "b", "c"}
	nw := []string{"a", "X", "c"}
	for _, r := range []editRange{{}, {5, 6, 1}, {1, 1, 7}, {2, 2, 1}} {
		ops := editOps(r, old, nw)
		var kept []string
		for _, op := range ops {
			if op.opType == diffEqual {
				kept = append(kept, op.text)
			}
		}
		if !slicesEqual(kept, []string{"a", "c"}) {
			t.Errorf("range %+v: equal lines = %v, want [a c] from the whole-file diff", r, kept)
		}
	}
}
