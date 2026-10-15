package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileLock is an exclusive advisory lock on a file, used to serialize writers
// that share state (for example the TUI and `fuckyourcli serve`).
type FileLock struct {
	f    *os.File
	path string
}

// Lock opens (creating it with perm 0600) the file at path and blocks until an
// exclusive advisory lock is held. Release it with Unlock.
func Lock(path string) (*FileLock, error) {
	return acquireLock(path, false)
}

// TryLock is like Lock but never blocks: it returns ErrLocked when the lock is
// already held by someone else.
func TryLock(path string) (*FileLock, error) {
	return acquireLock(path, true)
}

func acquireLock(path string, nonBlocking bool) (*FileLock, error) {
	if err := EnsureDir(filepath.Dir(path), DirPerm); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, FilePerm)
	if err != nil {
		return nil, fmt.Errorf("store: open lock %s: %w", path, err)
	}
	if err := lockFile(f, nonBlocking); err != nil {
		f.Close()
		if nonBlocking && isLockedError(err) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("store: lock %s: %w", path, err)
	}
	return &FileLock{f: f, path: path}, nil
}

// Unlock releases the lock and closes the underlying file. It is safe to call
// more than once.
func (l *FileLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	closeErr := l.f.Close()
	l.f = nil
	switch {
	case err != nil:
		return fmt.Errorf("store: unlock %s: %w", l.path, err)
	case closeErr != nil:
		return fmt.Errorf("store: close lock %s: %w", l.path, closeErr)
	}
	return nil
}

// Path reports the file backing the lock.
func (l *FileLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
