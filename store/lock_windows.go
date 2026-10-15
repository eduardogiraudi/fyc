//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File, nonBlocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonBlocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	ol := &windows.Overlapped{}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol)
}

func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}

func isLockedError(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
