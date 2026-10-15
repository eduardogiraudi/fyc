//go:build windows

package store

// syncDir is a no-op on Windows: a directory handle cannot be flushed, and
// MoveFileEx (used internally by os.Rename) already persists the rename.
func syncDir(dir string) error { return nil }
