package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testDoc models a document that evolved from v0 (legacy, no schema field) to
// v1 (adds the schema field) to v2 (adds Extra).
type testDoc struct {
	Schema int    `json:"schema"`
	Name   string `json:"name"`
	Extra  string `json:"extra,omitempty"`
}

func (testDoc) SchemaVersion() int { return 2 }

func testMigrations() Migrations {
	return Migrations{
		0: func(b []byte) ([]byte, error) {
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return nil, err
			}
			m["schema"] = 1
			return json.Marshal(m)
		},
		1: func(b []byte) ([]byte, error) {
			var d testDoc
			if err := json.Unmarshal(b, &d); err != nil {
				return nil, err
			}
			d.Schema = 2
			d.Extra = "up:" + d.Name
			return json.Marshal(d)
		},
	}
}

func TestMigrationsTarget(t *testing.T) {
	if got := testMigrations().Target(); got != 2 {
		t.Fatalf("Target() = %d, want 2", got)
	}
	if got := (Migrations{}).Target(); got != 0 {
		t.Fatalf("empty Target() = %d, want 0", got)
	}
}

func TestSchemaField(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`{"name":"x"}`, 0},
		{`{"schema":1,"name":"x"}`, 1},
		{`{"schema":7}`, 7},
	}
	for _, c := range cases {
		got, err := Schema([]byte(c.in))
		if err != nil {
			t.Fatalf("Schema(%s): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Schema(%s) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := Schema([]byte("{bad")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Schema(bad) err = %v, want ErrCorrupt", err)
	}
}

func TestMigrateLegacyChain(t *testing.T) {
	out, err := Migrate([]byte(`{"name":"x"}`), 2, testMigrations())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var d testDoc
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatal(err)
	}
	if d.Schema != 2 || d.Name != "x" || d.Extra != "up:x" {
		t.Fatalf("migrated = %+v", d)
	}
}

func TestMigrateAlreadyCurrent(t *testing.T) {
	in := []byte(`{"schema":2,"name":"x"}`)
	out, err := Migrate(in, 2, testMigrations())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("current document was modified: %s", out)
	}
}

func TestMigrateTooNew(t *testing.T) {
	_, err := Migrate([]byte(`{"schema":5}`), 2, testMigrations())
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestMigrateMissingStep(t *testing.T) {
	_, err := Migrate([]byte(`{"name":"x"}`), 2, Migrations{})
	if !errors.Is(err, ErrNoMigration) {
		t.Fatalf("err = %v, want ErrNoMigration", err)
	}
}

func TestMigrateRejectsBadOutput(t *testing.T) {
	bad := Migrations{0: func([]byte) ([]byte, error) { return []byte(`{"schema":7}`), nil }}
	if _, err := Migrate([]byte(`{}`), 1, bad); err == nil {
		t.Fatal("Migrate should reject a migration that declares the wrong schema")
	}
	broken := Migrations{0: func([]byte) ([]byte, error) { return []byte(`{`), nil }}
	if _, err := Migrate([]byte(`{}`), 1, broken); err == nil {
		t.Fatal("Migrate should reject non-JSON migration output")
	}
}

func captureLogs(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	SetLogger(func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	t.Cleanup(func() { SetLogger(nil) })
	return &lines
}

func TestLoadVersionedMigratesLegacyFile(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte(`{"name":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var d testDoc
	if err := LoadVersioned(path, &d, testMigrations()); err != nil {
		t.Fatalf("LoadVersioned: %v", err)
	}
	if d.Schema != 2 || d.Extra != "up:legacy" {
		t.Fatalf("loaded = %+v", d)
	}
}

func TestLoadVersionedFallsBackToBackup(t *testing.T) {
	logs := captureLogs(t)
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+BackupSuffix, []byte(`{"schema":1,"name":"good"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var d testDoc
	if err := LoadVersioned(path, &d, testMigrations()); err != nil {
		t.Fatalf("LoadVersioned: %v", err)
	}
	if d.Name != "good" || d.Schema != 2 {
		t.Fatalf("recovered = %+v", d)
	}
	if len(*logs) == 0 {
		t.Fatal("recovery was not logged")
	}
}

func TestLoadVersionedCorruptWithoutBackup(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	var d testDoc
	err := LoadVersioned(path, &d, testMigrations())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestLoadVersionedMissing(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "absent.json")
	var d testDoc
	err := LoadVersioned(path, &d, testMigrations())
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want os.IsNotExist", err)
	}
}

func TestLoadVersionedNoPartialStateOnRetry(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.json")
	// The corrupt main file would populate Extra but fail on a later field;
	// the backup is valid and must win cleanly.
	if err := os.WriteFile(path, []byte(`{"schema":1,"name":"bad","extra":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+BackupSuffix, []byte(`{"schema":1,"name":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := testDoc{Extra: "stale"}
	if err := LoadVersioned(path, &d, testMigrations()); err != nil {
		t.Fatalf("LoadVersioned: %v", err)
	}
	if d.Name != "ok" || d.Extra != "up:ok" {
		t.Fatalf("recovered = %+v", d)
	}
}
