package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTryLockReportsConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer first.Unlock()

	if _, err := TryLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("TryLock while held = %v, want ErrLocked", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	second, err := TryLock(path)
	if err != nil {
		t.Fatalf("TryLock after release: %v", err)
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

func TestLockBlocksUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := Lock(path)
		if err != nil {
			t.Errorf("second Lock: %v", err)
			close(acquired)
			return
		}
		second.Unlock()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("Lock returned while the lock was still held")
	case <-time.After(100 * time.Millisecond):
	}

	if err := first.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("Lock did not unblock after release")
	}
}

func TestLockSerializesWriters(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "writer.lock")
	dataPath := filepath.Join(dir, "counter")
	if err := WriteFile(dataPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	const increments = 25
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < increments; j++ {
				l, err := Lock(lockPath)
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				raw, err := os.ReadFile(dataPath)
				if err != nil {
					t.Errorf("read: %v", err)
					l.Unlock()
					return
				}
				n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err := WriteFile(dataPath, []byte(strconv.Itoa(n+1)), 0o600); err != nil {
					t.Errorf("write: %v", err)
				}
				l.Unlock()
			}
		}()
	}
	wg.Wait()

	raw, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if want := goroutines * increments; got != want {
		t.Fatalf("counter = %d, want %d (lost updates: lock not serializing)", got, want)
	}
}

func TestUnlockIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	l, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
	var nilLock *FileLock
	if err := nilLock.Unlock(); err != nil {
		t.Fatalf("nil Unlock: %v", err)
	}
	if nilLock.Path() != "" {
		t.Fatal("nil Path should be empty")
	}
}
