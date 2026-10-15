//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File, nonBlocking bool) error {
	how := syscall.LOCK_EX
	if nonBlocking {
		how |= syscall.LOCK_NB
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func isLockedError(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
