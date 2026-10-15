package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BackupSuffix is appended to a file name for its recovery copy.
const BackupSuffix = ".bak"

// renameFile is os.Rename, indirected so tests can force a failure in the
// window between the fully written temp file and the rename.
var renameFile = os.Rename

// WriteFile atomically replaces path with data.
//
// It writes to a temporary file in the same directory, fsyncs it, renames it
// over path, then flushes the directory entry. A concurrent reader therefore
// sees either the previous complete content or the new complete content, never
// a half-written file. On failure the temp file is removed and any existing
// path is left untouched. perm defaults to FilePerm when zero.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = FilePerm
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup: on any error below the temp file disappears and the
	// caller's original file is untouched.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write temp for %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: sync temp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp for %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("store: chmod temp for %s: %w", path, err)
	}
	if err := renameFile(tmpName, path); err != nil {
		return fmt.Errorf("store: rename over %s: %w", path, err)
	}
	tmpName = "" // renamed away: never remove the destination
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("store: flush dir %s: %w", dir, err)
	}
	return nil
}

// WriteJSON marshals v as indented JSON and writes it atomically to path.
// When backup is true the previous content of path is copied to path+".bak"
// first, so a later corruption can be recovered.
func WriteJSON(path string, v any, perm os.FileMode, backup bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	if backup {
		if err := Backup(path); err != nil {
			return err
		}
	}
	return WriteFile(path, data, perm)
}

// ReadJSON reads path and unmarshals it into v. A missing file returns the
// os.IsNotExist error unchanged (there is simply nothing saved yet); malformed
// JSON is reported as ErrCorrupt.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	return nil
}

// Backup copies the current content of path to path+".bak" atomically. A
// missing source file is not an error (there is nothing to back up).
func Backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("store: backup %s: %w", path, err)
	}
	perm := FilePerm
	if info, err := os.Stat(path); err == nil {
		if p := info.Mode().Perm(); p != 0 {
			perm = p
		}
	}
	return WriteFile(path+BackupSuffix, data, perm)
}
