package codexversion

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"fuckyourcli/store"
)

const (
	cacheTTL   = 24 * time.Hour
	failureTTL = 5 * time.Minute
)

type cacheEntry struct {
	Schema    int       `json:"schema"`
	Version   string    `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
}

func (cacheEntry) SchemaVersion() int { return 1 }

// Resolver deduplicates concurrent lookups and retains the last good version offline.
// Construct it with NewResolver; Resolve always returns a version, not a network error.
type Resolver struct {
	mu        sync.Mutex
	cacheFile string
	fetch     func(context.Context) (string, error)
	now       func() time.Time
	fallback  string
	loaded    bool
	version   string
	nextCheck time.Time
	running   chan struct{}
}

func NewResolver(cacheFile string) *Resolver {
	return &Resolver{
		cacheFile: cacheFile,
		fetch:     (Fetcher{}).Latest,
		now:       time.Now,
		fallback:  Bundled(),
	}
}

// Resolve checks HTTP at most daily after success, or every five minutes after failure.
// A fresh cache avoids HTTP entirely. Caller cancellation never poisons the retry window.
func (r *Resolver) Resolve(ctx context.Context) string {
	r.mu.Lock()
	now := r.now()
	if !r.loaded {
		r.loaded = true
		r.version = r.fallback
		var entry cacheEntry
		if r.cacheFile != "" && store.LoadVersioned(r.cacheFile, &entry, nil) == nil && Valid(entry.Version) {
			r.version = Newer(r.version, entry.Version)
			if entry.Version == r.version && !entry.CheckedAt.IsZero() && !entry.CheckedAt.After(now) {
				r.nextCheck = entry.CheckedAt.Add(cacheTTL)
			}
		}
	}
	if ctx.Err() != nil || now.Before(r.nextCheck) {
		version := r.version
		r.mu.Unlock()
		return version
	}
	if done := r.running; done != nil {
		r.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		r.mu.Lock()
		version := r.version
		r.mu.Unlock()
		return version
	}
	done := make(chan struct{})
	r.running = done
	r.mu.Unlock()

	version, err := r.fetch(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	now = r.now()
	if err == nil && Valid(version) {
		r.version = Newer(r.version, version)
		r.nextCheck = now.Add(cacheTTL)
		if r.cacheFile != "" && store.EnsureDir(filepath.Dir(r.cacheFile), 0) == nil {
			// Serialize cross-process writers and never overwrite a newer cached version.
			if lock, lockErr := store.TryLock(r.cacheFile + ".lock"); lockErr == nil {
				var entry cacheEntry
				if store.LoadVersioned(r.cacheFile, &entry, nil) == nil {
					r.version = Newer(r.version, entry.Version)
				}
				// Persistence is best-effort: a read-only cache must not disable Codex.
				_ = store.WriteJSON(r.cacheFile, cacheEntry{1, r.version, now}, 0, true)
				_ = lock.Unlock()
			}
		}
	} else if ctx.Err() == nil {
		r.nextCheck = now.Add(failureTTL)
	}
	r.running = nil
	close(done)
	return r.version
}
