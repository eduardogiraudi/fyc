package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// copyBetweenRoots copies a file or directory between two different allowed
// roots. Both ends stay inside their own os.Root: the bytes travel through this
// process, so no plain absolute path is ever written and a symlinked component
// cannot re-root either side.
func copyBetweenRoots(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string) error {
	info, err := srcRoot.Lstat(srcRel)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}

	if !info.IsDir() {
		return copyFileBetweenRoots(srcRoot, srcRel, dstRoot, dstRel)
	}

	if err := dstRoot.MkdirAll(dstRel, 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	dirFile, err := srcRoot.Open(srcRel)
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
		if err := copyBetweenRoots(srcRoot, srcChild, dstRoot, dstChild); err != nil {
			return err
		}
	}
	return nil
}

// copyFileBetweenRoots copies a single regular file between two roots, keeping
// the source mode.
func copyFileBetweenRoots(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string) error {
	info, err := srcRoot.Lstat(srcRel)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}

	srcFile, err := srcRoot.Open(srcRel)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if parent := filepath.Dir(dstRel); parent != "." && parent != "" {
		if err := dstRoot.MkdirAll(parent, 0755); err != nil {
			return fmt.Errorf("creating parent dir: %w", err)
		}
	}

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0644
	}
	dstFile, err := dstRoot.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return nil
}
