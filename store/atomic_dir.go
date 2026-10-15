//go:build !windows

package store

import "os"

// syncDir flushes a directory entry change (the rename) to stable storage. A
// filesystem that does not support directory fsync is only logged: the rename
// already happened, so the caller's write is not in doubt.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		logf("store: fsync %s: %v", dir, err)
	}
	return nil
}
