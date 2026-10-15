package logger

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"fuckyourcli/store"
)

const (
	MaxLogAge       = 30 * 24 * time.Hour
	MaxLogBytes     = 100 * 1024 * 1024
	cleanupInterval = 5 * time.Minute
)

// Called under the root lock; the timestamp throttles scans across processes.
func cleanupOldLogsIfDue(root string, now time.Time) error {
	path := filepath.Join(root, ".retention")
	var last time.Time
	if err := store.ReadJSON(path, &last); err != nil && !os.IsNotExist(err) && !errors.Is(err, store.ErrCorrupt) {
		return err
	}
	if elapsed := now.Sub(last); !last.IsZero() && elapsed >= 0 && elapsed < cleanupInterval {
		return nil
	}
	// Throttle failed scans too, rather than retrying expensive work every call.
	if err := store.WriteJSON(path, now, store.FilePerm, false); err != nil {
		return err
	}
	return cleanupOldLogs(root, now, MaxLogAge, MaxLogBytes)
}

// RemoveSession deletes the entire log set, never a currently running request.
func RemoveSession(id string) error {
	if !validID(id) {
		return errors.New("logs: invalid session id")
	}
	if _, err := os.Stat(getLogDir()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	lock, err := lockRoot()
	if err != nil {
		return err
	}
	defer lock.Unlock()
	dir := filepath.Join(getLogDir(), id)
	active, err := activeRequests(dir)
	if err != nil {
		return err
	}
	if active {
		return store.ErrLocked
	}
	return os.RemoveAll(dir)
}

// Called under the root lock. Per-request advisory leases protect live sessions
// across processes; crashed writers leave unlocked leases that are reclaimable.
func activeRequests(dir string) (bool, error) {
	path := filepath.Join(dir, ".requests")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// On Windows, ReadDir can treat a regular file as an empty directory.
	if !info.IsDir() {
		return false, errors.New("logs: requests path is not a directory")
	}
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !validID(e.Name()) {
			return true, nil
		}
		lock, err := store.TryLock(filepath.Join(path, e.Name()))
		if errors.Is(err, store.ErrLocked) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if err := lock.Unlock(); err != nil {
			return false, err
		}
	}
	return false, nil
}

func cleanupOldLogs(root string, now time.Time, maxAge time.Duration, maxBytes int64) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type candidate struct {
		dir    string
		last   time.Time
		size   int64
		active bool
	}
	var candidates []candidate
	var total int64
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, err := os.Lstat(filepath.Join(dir, "events.jsonl"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		c := candidate{dir: dir, last: info.ModTime()}
		if err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			c.size += info.Size()
			if info.ModTime().After(c.last) {
				c.last = info.ModTime()
			}
			return nil
		}); err != nil {
			return err
		}
		c.active, err = activeRequests(dir)
		if err != nil {
			return err
		}
		total += c.size
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].last.Equal(candidates[j].last) {
			return candidates[i].dir < candidates[j].dir
		}
		return candidates[i].last.Before(candidates[j].last)
	})
	for _, c := range candidates {
		if c.active {
			continue
		}
		if (maxAge > 0 && now.Sub(c.last) > maxAge) || (maxBytes > 0 && total > maxBytes) {
			if err := os.RemoveAll(c.dir); err != nil {
				return err
			}
			total -= c.size
		}
	}
	return nil
}
