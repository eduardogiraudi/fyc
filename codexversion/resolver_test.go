package codexversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fuckyourcli/store"
)

func testResolver(t *testing.T) (*Resolver, *time.Time, *int) {
	t.Helper()
	r := NewResolver(filepath.Join(t.TempDir(), "codex-version.json"))
	r.fallback = "0.150.0"
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	r.now = func() time.Time { return now }
	r.fetch = func(context.Context) (string, error) { calls++; return "0.161.0", nil }
	return r, &now, &calls
}

func TestResolverCacheAndRefresh(t *testing.T) {
	r, now, calls := testResolver(t)
	for i := 0; i < 3; i++ {
		if got := r.Resolve(context.Background()); got != "0.161.0" {
			t.Fatal(got)
		}
	}
	if *calls != 1 {
		t.Fatalf("calls = %d", *calls)
	}
	var entry cacheEntry
	if err := store.ReadJSON(r.cacheFile, &entry); err != nil || entry.Version != "0.161.0" || !entry.CheckedAt.Equal(*now) {
		t.Fatalf("cache = %+v, %v", entry, err)
	}
	other := NewResolver(r.cacheFile)
	other.fallback, other.fetch, other.now = r.fallback, r.fetch, r.now
	if got := other.Resolve(context.Background()); got != "0.161.0" || *calls != 1 {
		t.Fatalf("restart: %q, calls %d", got, *calls)
	}
	*now = now.Add(cacheTTL)
	if got := r.Resolve(context.Background()); got != "0.161.0" || *calls != 2 {
		t.Fatalf("refresh: %q, calls %d", got, *calls)
	}
}

func TestResolverFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, cached, want       string
		age                      time.Duration
		corrupt, blocked, backup bool
	}{
		{"missing offline", "", "0.150.0", 0, false, false, false},
		{"stale offline", "0.161.0", "0.161.0", 48 * time.Hour, false, false, false},
		{"older cache", "0.144.0", "0.150.0", time.Hour, false, false, false},
		{"corrupt cache", "", "0.150.0", 0, true, false, false},
		{"invalid cached version", "dev", "0.150.0", 0, false, false, false},
		{"unwritable directory", "", "0.150.0", 0, false, true, false},
		{"backup recovery", "0.161.0", "0.161.0", time.Hour, true, false, true},
		{"future timestamp", "0.161.0", "0.161.0", -time.Hour, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, now, calls := testResolver(t)
			if tc.blocked {
				if err := os.WriteFile(r.cacheFile, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				r.cacheFile = filepath.Join(r.cacheFile, "entry.json")
			}
			if tc.cached != "" {
				file := r.cacheFile
				if tc.backup {
					file += store.BackupSuffix
				}
				if err := store.WriteJSON(file, cacheEntry{1, tc.cached, now.Add(-tc.age)}, 0, false); err != nil {
					t.Fatal(err)
				}
			}
			if tc.corrupt {
				if err := os.WriteFile(r.cacheFile, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.fetch = func(context.Context) (string, error) { (*calls)++; return "", errors.New("offline") }
			if got := r.Resolve(context.Background()); got != tc.want {
				t.Fatalf("got %q", got)
			}
			if tc.backup {
				if *calls != 0 {
					t.Fatal("fresh recovered cache triggered HTTP")
				}
				return
			}
			if *calls != 1 {
				t.Fatalf("calls = %d", *calls)
			}
			r.Resolve(context.Background())
			if *calls != 1 {
				t.Fatal("no negative cache")
			}
			*now = now.Add(failureTTL)
			r.Resolve(context.Background())
			if *calls != 2 {
				t.Fatal("failed lookup never retried")
			}
		})
	}
}

func TestResolverReadOnlyCacheStillUsesOnlineVersion(t *testing.T) {
	r, _, calls := testResolver(t)
	if err := os.WriteFile(r.cacheFile, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	r.cacheFile = filepath.Join(r.cacheFile, "entry.json")
	if got := r.Resolve(context.Background()); got != "0.161.0" {
		t.Fatal(got)
	}
	r.Resolve(context.Background())
	if *calls != 1 {
		t.Fatal("failure to persist disabled memory cache")
	}
}

func TestResolverNoDowngrade(t *testing.T) {
	r, _, _ := testResolver(t)
	r.fetch = func(context.Context) (string, error) { return "0.144.0", nil }
	if got := r.Resolve(context.Background()); got != "0.150.0" {
		t.Fatal(got)
	}
}

func TestResolverConcurrentAndCancelledWaiter(t *testing.T) {
	r, _, _ := testResolver(t)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	r.fetch = func(context.Context) (string, error) { calls.Add(1); close(started); <-release; return "0.161.0", nil }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if got := r.Resolve(context.Background()); got != "0.161.0" {
			t.Error(got)
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if got := r.Resolve(ctx); got != "0.150.0" {
		t.Fatalf("cancelled waiter = %q", got)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := r.Resolve(context.Background()); got != "0.161.0" {
				t.Error(got)
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent lookups = %d", calls.Load())
	}
}

func TestResolverCancellationDoesNotPoisonCache(t *testing.T) {
	r, _, calls := testResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.fetch = func(context.Context) (string, error) { (*calls)++; cancel(); return "", ctx.Err() }
	if got := r.Resolve(ctx); got != "0.150.0" {
		t.Fatal(got)
	}
	r.fetch = func(context.Context) (string, error) { (*calls)++; return "0.161.0", nil }
	if got := r.Resolve(context.Background()); got != "0.161.0" || *calls != 2 {
		t.Fatalf("retry = %q, calls %d", got, *calls)
	}
}

func TestResolverConcurrentDiskWriterCannotDowngrade(t *testing.T) {
	r, now, _ := testResolver(t)
	r.fetch = func(context.Context) (string, error) {
		// Another process finishes a newer lookup while this request is in flight.
		if err := store.WriteJSON(r.cacheFile, cacheEntry{1, "0.170.0", *now}, 0, false); err != nil {
			t.Fatal(err)
		}
		return "0.161.0", nil
	}
	if got := r.Resolve(context.Background()); got != "0.170.0" {
		t.Fatal(got)
	}
	var entry cacheEntry
	if err := store.ReadJSON(r.cacheFile, &entry); err != nil || entry.Version != "0.170.0" {
		t.Fatalf("newer disk version lost: %+v, %v", entry, err)
	}
}

func TestResolverBusyDiskLockIsNonFatal(t *testing.T) {
	r, _, _ := testResolver(t)
	lock, err := store.TryLock(r.cacheFile + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if got := r.Resolve(context.Background()); got != "0.161.0" {
		t.Fatal(got)
	}
	if _, err := os.Stat(r.cacheFile); !os.IsNotExist(err) {
		t.Fatalf("wrote cache without writer lock: %v", err)
	}
}

func TestResolverInvalidLookupRetainsFallback(t *testing.T) {
	r, _, calls := testResolver(t)
	r.fetch = func(context.Context) (string, error) { (*calls)++; return "0.162.0-alpha.1", nil }
	if got := r.Resolve(context.Background()); got != "0.150.0" {
		t.Fatal(got)
	}
	r.Resolve(context.Background())
	if *calls != 1 {
		t.Fatal("invalid lookup was not negatively cached")
	}
}
