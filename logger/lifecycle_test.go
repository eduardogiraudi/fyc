package logger

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

func TestCrossProcessActiveLeaseAndCrashRecovery(t *testing.T) {
	if os.Getenv("FYC_LOG_LEASE_CHILD") == "1" {
		r, err := begin(scope{session: "leased", role: "main"}, Metadata{})
		if err != nil {
			t.Fatal(err)
		}
		defer r.release()
		fmt.Println("lease-ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	root := setupLogs(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessActiveLeaseAndCrashRecovery$")
	cmd.Env = append(os.Environ(), "FYC_LOG_LEASE_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "lease-ready" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child exited without a lease")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child lease timeout")
	}
	if err := RemoveSession("leased"); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("cross-process active logs deleted: %v", err)
	}
	lock, err := store.Lock(filepath.Join(root, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	err = cleanupOldLogs(root, time.Now().Add(60*24*time.Hour), MaxLogAge, 1)
	_ = lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "leased", "events.jsonl")); err != nil {
		t.Fatal("retention deleted cross-process active logs")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := RemoveSession("leased"); err != nil {
		t.Fatalf("crash lease not reclaimed: %v", err)
	}
}

func TestProviderPanicDoesNotLeakLease(t *testing.T) {
	root := setupLogs(t)
	p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		panic("provider panic")
	}), Metadata{})
	func() {
		defer func() {
			if recover() != "provider panic" {
				t.Fatal("provider panic changed")
			}
		}()
		_, _ = p.StreamChat(WithSession(context.Background(), "panic"), nil, nil, nil)
	}()
	if len(events(t, root, "panic")) != 1 {
		t.Fatal("panic should leave only the start event")
	}
	if err := RemoveSession("panic"); err != nil {
		t.Fatalf("panic leaked lease: %v", err)
	}
}

func TestDebugDumpFailurePreservesProviderResult(t *testing.T) {
	root := setupLogs(t)
	t.Setenv("FYC_LOG_DEBUG", "1")
	want := &types.StreamResult{Content: "ok"}
	p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		if err := os.WriteFile(filepath.Join(root, "failure", "responses"), []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		return want, nil
	}), Metadata{})
	var notices int
	store.SetLogger(func(string, ...any) { notices++ })
	t.Cleanup(func() { store.SetLogger(nil) })
	result, err := p.StreamChat(WithSession(context.Background(), "failure"), nil, nil, nil)
	if err != nil || result != want || notices == 0 || len(events(t, root, "failure")) != 2 {
		t.Fatal("dump failure altered provider result or diagnostics")
	}
	if err := RemoveSession("failure"); err != nil {
		t.Fatalf("dump failure leaked lease: %v", err)
	}
}

func TestRootLockTimeoutIsNonFatal(t *testing.T) {
	for _, phase := range []string{"begin", "http", "finish", "remove"} {
		t.Run(phase, func(t *testing.T) {
			// Fake time keeps scheduler and filesystem delays out of the timeout assertion.
			synctest.Test(t, func(t *testing.T) {
				root := setupLogs(t)
				const id = "lock-timeout"
				ctx := WithSession(context.Background(), id)
				var r *request
				if phase != "begin" {
					var err error
					r, err = begin(scope{session: id, role: "main"}, Metadata{})
					if err != nil {
						t.Fatal(err)
					}
					defer r.release()
					ctx = context.WithValue(ctx, requestKey, r)
					if phase == "remove" {
						r.release()
					}
				}
				held, err := store.TryLock(filepath.Join(root, ".lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer held.Unlock()
				var notices int
				store.SetLogger(func(string, ...any) { notices++ })
				t.Cleanup(func() { store.SetLogger(nil) })
				want := &types.StreamResult{Content: "ok"}
				wantErr := errors.New("provider failure")
				start := time.Now()
				done := make(chan error, 1)
				go func() {
					switch phase {
					case "begin":
						p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
							return want, wantErr
						}), Metadata{})
						result, err := p.StreamChat(ctx, nil, nil, nil)
						if result != want || err != wantErr {
							done <- fmt.Errorf("lock timeout altered provider result: %v %v", result, err)
							return
						}
						done <- nil
					case "http":
						HTTPAttempt(ctx, 200, 0, false)
						done <- nil
					case "finish":
						done <- r.finish(ctx, want, wantErr, nil)
					case "remove":
						done <- RemoveSession(id)
					}
				}()
				select {
				case err = <-done:
				case <-time.After(rootLockTimeout + time.Second):
					_ = held.Unlock()
					<-done
					t.Fatal("logger did not stop waiting for the root lock")
				}
				if elapsed := time.Since(start); elapsed != rootLockTimeout {
					t.Fatalf("logger waited %v, want %v", elapsed, rootLockTimeout)
				}
				if phase == "finish" || phase == "remove" {
					if !errors.Is(err, store.ErrLocked) {
						t.Fatalf("want lock timeout, got %v", err)
					}
				} else if err != nil || notices != 1 {
					t.Fatalf("logging failure changed behavior or was silent: %v, notices=%d", err, notices)
				}
				if phase == "begin" {
					if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
						t.Fatal("failed begin created conversation logs")
					}
				} else {
					if len(events(t, root, id)) != 1 {
						t.Fatal("lock timeout wrote an event or removed logs")
					}
					if phase == "finish" {
						active, err := activeRequests(r.dir())
						if err != nil || active {
							t.Fatalf("finish timeout leaked lease: active=%v, err=%v", active, err)
						}
					}
					if err := held.Unlock(); err != nil {
						t.Fatal(err)
					}
					r.release()
					if err := RemoveSession(id); err != nil {
						t.Fatalf("logs not removable after lock release: %v", err)
					}
				}
			})
		})
	}
}

func TestRootLockRetriesUntilReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := setupLogs(t)
		held, err := store.TryLock(filepath.Join(root, ".lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer held.Unlock()
		type outcome struct {
			lock *store.FileLock
			err  error
		}
		done := make(chan outcome, 1)
		go func() {
			lock, err := lockRoot()
			done <- outcome{lock, err}
		}()
		synctest.Wait()
		select {
		case got := <-done:
			_ = got.lock.Unlock()
			t.Fatalf("root lock returned while held: %v", got.err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := held.Unlock(); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if err := got.lock.Unlock(); err != nil {
				t.Fatal(err)
			}
		case <-time.After(rootLockTimeout + time.Second):
			t.Fatal("root lock did not retry after release")
		}
	})
}

func TestRootLockDoesNotRetryIOErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := setupLogs(t)
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		lock, err := lockRoot()
		defer lock.Unlock()
		if lock != nil || err == nil || errors.Is(err, store.ErrLocked) {
			t.Fatalf("want filesystem error, got lock=%v, err=%v", lock, err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("root lock retried a filesystem error for %v", elapsed)
		}
	})
}

func TestRetentionThrottleAcrossProcesses(t *testing.T) {
	call := func() {
		t.Helper()
		p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			return &types.StreamResult{Content: "ok"}, nil
		}), Metadata{})
		if _, err := p.StreamChat(WithSession(context.Background(), "retention"), nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("FYC_RETENTION_CHILD") == "1" {
		call()
		return
	}
	root := setupLogs(t)
	expired := filepath.Join(root, "expired")
	makeExpired := func() {
		t.Helper()
		if err := store.EnsureDir(expired, store.DirPerm); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(expired, "events.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), store.FilePerm); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-MaxLogAge - time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	makeExpired()
	call()
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatal("first request did not prune expired logs")
	}
	var last time.Time
	if err := store.ReadJSON(filepath.Join(root, ".retention"), &last); err != nil {
		t.Fatal(err)
	}
	makeExpired()
	call()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRetentionThrottleAcrossProcesses$")
	cmd.Env = append(os.Environ(), "FYC_RETENTION_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	if _, err := os.Stat(expired); err != nil {
		t.Fatalf("retention ran again within five minutes: %v", err)
	}
	lock, err := lockRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if err := cleanupOldLogsIfDue(root, last.Add(cleanupInterval-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); err != nil {
		t.Fatal("retention ran before interval elapsed")
	}
	if err := cleanupOldLogsIfDue(root, last.Add(cleanupInterval)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatal("retention did not run at interval boundary")
	}
}

func TestRetentionThrottleRecoversInvalidTimestamp(t *testing.T) {
	for _, state := range []string{"missing", "corrupt", "zero", "future"} {
		t.Run(state, func(t *testing.T) {
			root := setupLogs(t)
			lock, err := lockRoot()
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Unlock()
			now := time.Now().UTC()
			path := filepath.Join(root, ".retention")
			switch state {
			case "corrupt":
				err = os.WriteFile(path, []byte("invalid"), store.FilePerm)
			case "zero":
				err = store.WriteJSON(path, time.Time{}, store.FilePerm, false)
			case "future":
				err = store.WriteJSON(path, now.Add(time.Hour), store.FilePerm, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := cleanupOldLogsIfDue(root, now); err != nil {
				t.Fatal(err)
			}
			var last time.Time
			if err := store.ReadJSON(path, &last); err != nil || !last.Equal(now) {
				t.Fatalf("timestamp not recovered: last=%v, err=%v", last, err)
			}
		})
	}
}

func TestActiveRequestsDirectoryValidation(t *testing.T) {
	for _, state := range []string{"missing", "empty", "file"} {
		t.Run(state, func(t *testing.T) {
			root := setupLogs(t)
			lock, err := lockRoot()
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Unlock()
			dir := filepath.Join(root, "session")
			if err := store.EnsureDir(dir, store.DirPerm); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".requests")
			switch state {
			case "empty":
				err = store.EnsureDir(path, store.DirPerm)
			case "file":
				err = os.WriteFile(path, []byte("{}\n"), store.FilePerm)
			}
			if err != nil {
				t.Fatal(err)
			}
			active, err := activeRequests(dir)
			if active || (err != nil) != (state == "file") {
				t.Fatalf("active=%v, err=%v", active, err)
			}
		})
	}
}

func TestRemoveSessionRejectsInvalidLeaseDirectory(t *testing.T) {
	root := setupLogs(t)
	dir := filepath.Join(root, "broken")
	if err := store.EnsureDir(dir, store.DirPerm); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".requests")
	if err := os.WriteFile(path, []byte("{}\n"), store.FilePerm); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSession("broken"); err == nil {
		t.Fatal("expected error for invalid leases directory")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("invalid lease path was removed: %v", err)
	}
}

func TestRetentionThrottlesFailedScans(t *testing.T) {
	root := setupLogs(t)
	lock, err := lockRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	dir := filepath.Join(root, "broken")
	if err := store.EnsureDir(dir, store.DirPerm); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"events.jsonl", ".requests"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), store.FilePerm); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := cleanupOldLogsIfDue(root, now); err == nil {
		t.Fatal("expected scan error for invalid leases directory")
	}
	if err := cleanupOldLogsIfDue(root, now.Add(time.Second)); err != nil {
		t.Fatalf("failed scan retried before interval: %v", err)
	}
	if err := cleanupOldLogsIfDue(root, now.Add(cleanupInterval)); err == nil {
		t.Fatal("failed scan was not retried after interval")
	}
}
