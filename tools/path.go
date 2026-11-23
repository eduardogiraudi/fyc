package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/store"
)

// getRoot opens an os.Root for the restricted root directory (traversal-safe).
func getRoot(restrictedRoot string) (*os.Root, error) {
	return os.OpenRoot(restrictedRoot)
}

// validatePathInRoot checks that a requested path is within the restricted root,
// cleans it, and verifies it's not ignored. Returns the safe relative path.
func validatePathInRoot(requestedPath string, restrictedRoot string, globalIgnore *IgnoreManager) (relPath string, err error) {
	// The single-root spelling of Workspace.resolve: every caller that only ever
	// sees the primary root keeps working unchanged.
	_, relPath, err = Workspace{Primary: restrictedRoot}.resolve(requestedPath, globalIgnore)
	return relPath, err
}

// escapesRoot reports whether a cleaned relative path leaves its base. A name
// that merely starts with ".." (..notes.txt) is a normal file, not an escape.
func escapesRoot(rel string) bool {
	return filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// relPathInRoot turns an absolute path into the relative name a caller can act
// on. Containment is decided on the fully resolved path — a link must not lead
// out of the workspace — while the returned name keeps its final component as it
// was spelled: delete and rename address the link, never its target. The
// asymmetry with a relative path (a link pointing outside is refused here, but
// deletable by name) is deliberate: the strict side is the one that can be
// handed to a plain OS call.
func relPathInRoot(requestedPath, rootAbs string) (string, error) {
	p := filepath.Clean(requestedPath)
	// landsOnRoot is what makes the root addressable through its own symlink; it is
	// only a fallback, never a name: a link inside the workspace that points at the
	// root ("self" → ".") must keep its own name, or deleting it would wipe the
	// workspace instead of removing the link.
	landsOnRoot := false
	target, err := store.ResolvePath(p)
	if err != nil {
		// A link that resolves to nothing (dangling or looping) has no landing site
		// to check, and it must stay addressable so it can be deleted or renamed.
		if !isSymlink(p) {
			return "", err
		}
	} else {
		rel, rerr := filepath.Rel(rootAbs, target)
		if rerr != nil || escapesRoot(rel) {
			return "", fmt.Errorf("%s is outside the workspace %s", requestedPath, rootAbs)
		}
		landsOnRoot = rel == "."
	}
	parent, err := store.ResolvePath(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, filepath.Join(parent, filepath.Base(p)))
	if err == nil && !escapesRoot(rel) {
		return rel, nil
	}
	if landsOnRoot {
		// The root spelled through its own link (~/code → /Volumes/ext/code): the
		// name lives outside itself, but it is the root.
		return ".", nil
	}
	return "", fmt.Errorf("%s is outside the workspace %s", requestedPath, rootAbs)
}

// isSymlink reports whether p itself is a symlink (Lstat, so it is never
// followed).
func isSymlink(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// realPathWithinRoot resolves every symlink component of absPath — which must
// already be lexically inside restrictedRoot — and verifies that the resolved
// path is still inside the root. It returns the resolved absolute path.
//
// os.Root protects every open/read/write performed through it, but tools that
// hand an absolute path back to the plain OS (fastwalk, os.OpenRoot, os.Stat,
// external LSP servers) follow a symlink wherever it points. Those call sites
// must check the path with this helper, otherwise a single link inside the
// workspace (e.g. "logs -> /var/log") re-roots them outside the sandbox.
func realPathWithinRoot(absPath, restrictedRoot string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(restrictedRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve restricted root: %w", err)
	}

	real, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", err
	}

	// The root itself must stay reachable even when it is "/", where a prefix
	// check would look for a doubled separator.
	if rel, err := filepath.Rel(realRoot, real); err != nil || escapesRoot(rel) {
		return "", fmt.Errorf("access denied: %s resolves outside allowed directory %s", absPath, realRoot)
	}
	return real, nil
}

// deletePathInRoot recursively deletes a file or directory within an os.Root.
func deletePathInRoot(root *os.Root, path string) error {
	info, err := root.Lstat(path)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return root.Remove(path)
	}

	dirFile, err := root.Open(path)
	if err != nil {
		return err
	}
	entries, err := dirFile.ReadDir(0)
	dirFile.Close()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())
		if err := deletePathInRoot(root, entryPath); err != nil {
			return err
		}
	}

	return root.Remove(path)
}

// copyPathInRoot recursively copies a file or directory within an os.Root.
// Directories are created at the destination as needed.
func copyPathInRoot(root *os.Root, srcRel, dstRel string) error {
	info, err := root.Lstat(srcRel)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}

	if !info.IsDir() {
		return copyFileInRoot(root, srcRel, dstRel)
	}

	// Create destination directory
	if err := root.MkdirAll(dstRel, 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	// Walk source directory
	dirFile, err := root.Open(srcRel)
	if err != nil {
		return err
	}
	entries, err := dirFile.ReadDir(-1)
	dirFile.Close()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcChild := filepath.Join(srcRel, entry.Name())
		dstChild := filepath.Join(dstRel, entry.Name())
		if err := copyPathInRoot(root, srcChild, dstChild); err != nil {
			return err
		}
	}
	return nil
}

// copyFileInRoot copies a single file from srcRel to dstRel within an os.Root.
func copyFileInRoot(root *os.Root, srcRel, dstRel string) error {
	srcFile, err := root.Open(srcRel)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	data, err := io.ReadAll(srcFile)
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	parent := filepath.Dir(dstRel)
	if parent != "." {
		if err := root.MkdirAll(parent, 0755); err != nil {
			return fmt.Errorf("creating parent dir: %w", err)
		}
	}

	dstFile, err := root.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := dstFile.Write(data); err != nil {
		return err
	}
	return nil
}

// renameInRoot renames (or moves) a file/directory within the restricted root.
// Both the destination parent creation and the rename itself go through the
// sandboxed Root: os.Rename on joined absolute paths would follow a symlinked
// parent directory and move files across the workspace boundary.
func renameInRoot(root *os.Root, oldRel, newRel string) error {
	if parent := filepath.Dir(newRel); parent != "." && parent != "" {
		if err := root.MkdirAll(parent, 0755); err != nil {
			return fmt.Errorf("creating parent dir: %w", err)
		}
	}

	if err := root.Rename(oldRel, newRel); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
