package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePathInRootKeepsDotDotNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"..notes.txt", "..cache", "main.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A name that merely starts with ".." is an ordinary file: only a real
	// traversal ("..", "../x") leaves the root.
	if rel, err := validatePathInRoot("..notes.txt", root, nil); err != nil || rel != "..notes.txt" {
		t.Fatalf("validatePathInRoot(..notes.txt) = %q, %v", rel, err)
	}
	for _, bad := range []string{"..", "../outside.txt", "../", "a/../../outside.txt"} {
		if _, err := validatePathInRoot(bad, root, nil); err == nil {
			t.Errorf("validatePathInRoot(%q) was allowed", bad)
		}
	}
}

func TestValidatePathInRootAcceptsAbsolutePathsOfTheSameTree(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The workspace can be spelled either way (macOS $TMPDIR is /var → /private/var):
	// both are inside, and the answer is relative to the root.
	rel, err := validatePathInRoot(filepath.Join(real, "a.txt"), link, nil)
	if err != nil || rel != "a.txt" {
		t.Fatalf("resolved spelling = %q, %v", rel, err)
	}
	if rel, err := validatePathInRoot(filepath.Join(link, "a.txt"), link, nil); err != nil || rel != "a.txt" {
		t.Fatalf("symlinked spelling = %q, %v", rel, err)
	}
	if _, err := validatePathInRoot(filepath.Join(t.TempDir(), "a.txt"), link, nil); err == nil {
		t.Fatal("a path in another tree was allowed")
	}
}

func TestValidatePathInRootRefusesALinkOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	if _, err := validatePathInRoot(filepath.Join(root, "escape", "secret.txt"), root, nil); err == nil {
		t.Fatal("a link out of the root was followed")
	}
}

func TestValidatePathInRootAddressesALinkByItsOwnName(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	// Delete and rename act on the name they are given: the link must stay the
	// link, and the relative and absolute spellings must agree.
	for _, p := range []string{"link.txt", filepath.Join(root, "link.txt")} {
		rel, err := validatePathInRoot(p, root, nil)
		if err != nil || rel != "link.txt" {
			t.Fatalf("validatePathInRoot(%q) = %q, %v; want the link's own name", p, rel, err)
		}
	}
}

func TestValidatePathInRootKeepsADanglingLinkAddressable(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken.txt")
	if err := os.Symlink(filepath.Join(root, "gone.txt"), broken); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// A link that points nowhere still has to be deletable by its own name.
	for _, p := range []string{"broken.txt", broken} {
		if rel, err := validatePathInRoot(p, root, nil); err != nil || rel != "broken.txt" {
			t.Fatalf("validatePathInRoot(%q) = %q, %v", p, rel, err)
		}
	}
}

func TestGitPathValidationFollowsTheSameRootSpelling(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	rel, err := validateGitPath(filepath.Join(real, "a.go"), link)
	if err != nil || rel != "a.go" {
		t.Fatalf("validateGitPath(resolved spelling) = %q, %v", rel, err)
	}
	if _, err := validateGitPath(filepath.Join(t.TempDir(), "a.go"), link); err == nil {
		t.Fatal("a path in another tree was allowed")
	}
}

func TestValidatePathInRootAcceptsTheRootItself(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// A model asking about its own workspace may spell it either way.
	for _, p := range []string{link, real} {
		if rel, err := validatePathInRoot(p, link, nil); err != nil || rel != "." {
			t.Fatalf("validatePathInRoot(%q) = %q, %v; want the root itself", p, rel, err)
		}
	}
}

func TestValidatePathInRootKeepsALinkToTheRootAsALink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(".", filepath.Join(root, "self")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// Deleting "self" must remove the link, never resolve to the workspace root.
	for _, p := range []string{"self", filepath.Join(root, "self")} {
		if rel, err := validatePathInRoot(p, root, nil); err != nil || rel != "self" {
			t.Fatalf("validatePathInRoot(%q) = %q, %v; want the link's own name", p, rel, err)
		}
	}
}
