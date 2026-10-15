package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// getRoot opens an os.Root for the restricted root directory (traversal-safe).
func getRoot(restrictedRoot string) (*os.Root, error) {
	return os.OpenRoot(restrictedRoot)
}

// validatePathInRoot checks that a requested path is within the restricted root,
// cleans it, and verifies it's not ignored. Returns the safe relative path.
func validatePathInRoot(requestedPath string, restrictedRoot string, globalIgnore *IgnoreManager) (relPath string, err error) {
	if requestedPath == "" {
		return "", fmt.Errorf("empty path")
	}

	cleanPath := filepath.Clean(requestedPath)

	if strings.HasPrefix(cleanPath, "..") {
		return "", fmt.Errorf("access denied: path traversal not allowed")
	}

	if filepath.IsAbs(requestedPath) {
		rootAbs, err := filepath.Abs(restrictedRoot)
		if err != nil {
			return "", fmt.Errorf("cannot resolve restricted root: %w", err)
		}

		if !strings.HasPrefix(requestedPath, rootAbs+string(filepath.Separator)) && requestedPath != rootAbs {
			return "", fmt.Errorf("access denied: %s is outside allowed directory %s", requestedPath, rootAbs)
		}

		relPath, err = filepath.Rel(rootAbs, requestedPath)
		if err != nil {
			return "", fmt.Errorf("cannot compute relative path: %w", err)
		}
		cleanPath = relPath
	}

	if globalIgnore != nil {
		absPath := filepath.Join(restrictedRoot, cleanPath)
		if globalIgnore.isIgnored(absPath, restrictedRoot) {
			return "", fmt.Errorf("access denied: %s is listed in an ignore file", requestedPath)
		}
	}

	return cleanPath, nil
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

	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
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
