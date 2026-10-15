package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want %q", data, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits: os.Stat reports 0666/0777 for
	// everything, so the mode assertion is meaningful only off Windows.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("perm = %o, want 0600", perm)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestWriteFileDefaultPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := WriteFile(path, []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != FilePerm {
			t.Fatalf("perm = %o, want default %o", perm, FilePerm)
		}
	}
}

// TestWriteFileKeepsOriginalOnFailure forces the rename to fail after the temp
// file has been fully written and verifies the previous content survives and
// no temp file is leaked.
func TestWriteFileKeepsOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := renameFile
	renameFile = func(oldpath, newpath string) error { return errors.New("boom") }
	t.Cleanup(func() { renameFile = original })

	if err := WriteFile(path, []byte("new"), 0o600); err == nil {
		t.Fatal("WriteFile should have failed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("content = %q, want the original untouched", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestWriteJSONBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")

	if err := WriteJSON(path, map[string]int{"a": 1}, 0o600, true); err != nil {
		t.Fatalf("first WriteJSON: %v", err)
	}
	if _, err := os.Stat(path + BackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("backup created on first write: %v", err)
	}

	if err := WriteJSON(path, map[string]int{"a": 2}, 0o600, true); err != nil {
		t.Fatalf("second WriteJSON: %v", err)
	}
	var cur map[string]int
	if err := ReadJSON(path, &cur); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if cur["a"] != 2 {
		t.Fatalf("current a = %d, want 2", cur["a"])
	}
	var bak map[string]int
	if err := ReadJSON(path+BackupSuffix, &bak); err != nil {
		t.Fatalf("ReadJSON backup: %v", err)
	}
	if bak["a"] != 1 {
		t.Fatalf("backup a = %d, want the previous value 1", bak["a"])
	}
}

func TestReadJSONMissingIsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	var v map[string]any
	err := ReadJSON(path, &v)
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want os.IsNotExist", err)
	}
}

func TestReadJSONCorruptIsErrCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	err := ReadJSON(path, &v)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestBackupMissingSourceIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if err := Backup(path); err != nil {
		t.Fatalf("Backup of a missing file should be a no-op, got %v", err)
	}
	if _, err := os.Stat(path + BackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup file: %v", err)
	}
}

func TestWriteJSONIsIndentedAndNewlineTerminated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := WriteJSON(path, map[string]int{"a": 1}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("file does not end in a newline: %q", data)
	}
	var round map[string]int
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
}
