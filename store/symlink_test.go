package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathKeepsTheMissingTail(t *testing.T) {
	// The expectation is a resolved path, so the base has to be one too: macOS
	// $TMPDIR is /var/folders/… → /private/var/…, and an unresolved base would
	// compare two spellings of the same directory and fail for the very reason the
	// function exists.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"existing directory", link, real},
		{"one missing level", filepath.Join(link, "new"), filepath.Join(real, "new")},
		{"two missing levels", filepath.Join(link, "new", "deep", "a.txt"), filepath.Join(real, "new", "deep", "a.txt")},
	}
	for _, tc := range cases {
		got, err := ResolvePath(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: ResolvePath(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestResolvePathRefusesADanglingLink(t *testing.T) {
	base := t.TempDir()
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "gone"), dangling); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// A restore must not follow a link that points nowhere, and it must not treat
	// it as "the file does not exist yet" either.
	if got, err := ResolvePath(dangling); err == nil {
		t.Fatalf("ResolvePath(%q) = %q, want an error", dangling, got)
	}
}
