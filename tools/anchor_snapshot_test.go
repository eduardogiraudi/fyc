package tools

import (
	"strings"
	"testing"
)

// withFreshState points the package singleton at an empty manager for one test.
func withFreshState(t *testing.T) *AnchorStateManager {
	t.Helper()
	saved := globalAnchorState
	mgr := newTestManager()
	globalAnchorState = mgr
	t.Cleanup(func() { globalAnchorState = saved })
	return mgr
}

func TestAnchorSnapshotRestoresTheBoundary(t *testing.T) {
	mgr := withFreshState(t)
	path := "/t/a.go"
	mgr.EnsureAnchors(path, []string{"one", "two"})
	before, _ := mgr.GetAnnotated(path)

	snap := SnapshotAnchors()

	// What a turn does: an edit that moves the anchors on.
	token, _, _ := strings.Cut(before, "§")
	oldLines, newLines, r, err := mgr.PlanEdit(path, token, token, "one two")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	mgr.CommitEdit(path, r, oldLines, newLines)
	mgr.EnsureAnchors(path, []string{"one", "two", "three"})
	after, _ := mgr.GetAnnotated(path)
	if after == before {
		t.Fatal("the edit did not change the cache, the test proves nothing")
	}

	RestoreAnchors(snap)
	restored, err := mgr.GetAnnotated(path)
	if err != nil {
		t.Fatal(err)
	}
	if restored != before {
		t.Fatalf("restored cache =\n%s\nwant\n%s", restored, before)
	}
	// The snapshot itself must be immune to the mutations that followed it.
	if again, _ := mgr.GetAnnotated(path); again != before {
		t.Fatalf("the snapshot was mutated in place:\n%s", again)
	}
}

func TestAnchorSnapshotKeepsInvalidationsAndAdditions(t *testing.T) {
	mgr := withFreshState(t)
	path := "/t/b.go"
	mgr.EnsureAnchors(path, []string{"a", "b", "c"})
	snap := SnapshotAnchors()

	// An external change reconciles the file: some anchors become stale and the
	// new lines get anchors of their own. None of that may survive the restore.
	mgr.EnsureAnchors(path, []string{"a", "changed", "c"})
	stale := false
	for _, line := range strings.Split(mustAnnotated(t, mgr, path), "\n") {
		if strings.Contains(line, "changed") {
			stale = true
		}
	}
	if !stale {
		t.Fatal("the reconcile did not reach the cache")
	}

	RestoreAnchors(snap)
	// The boundaries are back: the file has three lines and no stale entries, so
	// the first anchor is a valid patch target again.
	token, _, _ := strings.Cut(mustAnnotated(t, mgr, path), "§")
	lines, _, r, err := mgr.PlanEdit(path, token, token, "a")
	if err != nil {
		t.Fatalf("the restored anchors do not work: %v", err)
	}
	if len(lines) != 3 || r.start != 0 {
		t.Fatalf("restored file = %v (%+v)", lines, r)
	}
}

func TestRestoreAnchorsDropsTheFilesThatCouldNotBeReverted(t *testing.T) {
	mgr := withFreshState(t)
	mgr.EnsureAnchors("/t/a.go", []string{"one"})
	mgr.EnsureAnchors("/t/b.go", []string{"two"})
	snap := SnapshotAnchors()

	RestoreAnchors(snap, "/t/b.go")
	if _, err := mgr.GetAnnotated("/t/a.go"); err != nil {
		t.Fatalf("an untouched file lost its anchors: %v", err)
	}
	if _, err := mgr.GetAnnotated("/t/b.go"); err == nil {
		t.Fatal("the kept file still has anchors describing content that is gone")
	}
}

func TestRestoreNilSnapshotClearsTheCache(t *testing.T) {
	mgr := withFreshState(t)
	mgr.EnsureAnchors("/t/a.go", []string{"one"})
	RestoreAnchors(nil)
	if _, err := mgr.GetAnnotated("/t/a.go"); err == nil {
		t.Fatal("a nil snapshot must leave nothing behind")
	}
}

func mustAnnotated(t *testing.T, mgr *AnchorStateManager, path string) string {
	t.Helper()
	out, err := mgr.GetAnnotated(path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
