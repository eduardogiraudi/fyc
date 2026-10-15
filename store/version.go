package store

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
)

// SchemaVersioner is implemented by documents that declare the schema version
// they understand. LoadVersioned uses it to pick the migration target.
type SchemaVersioner interface {
	SchemaVersion() int
}

// Migrations maps a source schema version to a function upgrading the raw JSON
// document from that version to the next one (vN -> vN+1). Documents without a
// "schema" field are treated as version 0 (legacy).
type Migrations map[int]func([]byte) ([]byte, error)

// Target returns the highest schema version reachable with the receiver: the
// largest migration key plus one. An empty map targets version 0.
func (m Migrations) Target() int {
	highest := -1
	for from := range m {
		if from > highest {
			highest = from
		}
	}
	return highest + 1
}

// Schema extracts the "schema" field from a raw JSON document. A document
// without the field is reported as version 0.
func Schema(data []byte) (int, error) {
	var head struct {
		Schema *int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if head.Schema == nil {
		return 0, nil
	}
	return *head.Schema, nil
}

// Migrate upgrades a raw JSON document to target by chaining the migrations.
// It is a no-op when the document is already at target, and returns
// ErrSchemaTooNew when it was produced by a newer version.
func Migrate(data []byte, target int, m Migrations) ([]byte, error) {
	cur, err := Schema(data)
	if err != nil {
		return nil, err
	}
	if cur > target {
		return nil, fmt.Errorf("%w: document is v%d, supported up to v%d", ErrSchemaTooNew, cur, target)
	}
	for cur < target {
		fn, ok := m[cur]
		if !ok {
			return nil, fmt.Errorf("%w: v%d -> v%d", ErrNoMigration, cur, cur+1)
		}
		next, err := fn(data)
		if err != nil {
			return nil, fmt.Errorf("store: migration v%d -> v%d: %w", cur, cur+1, err)
		}
		got, err := Schema(next)
		if err != nil {
			return nil, fmt.Errorf("store: migration v%d -> v%d produced invalid JSON: %w", cur, cur+1, err)
		}
		if got != cur+1 {
			return nil, fmt.Errorf("store: migration v%d -> v%d declared schema %d", cur, cur+1, got)
		}
		data = next
		cur++
	}
	return data, nil
}

// LoadVersioned reads a versioned JSON document from path into v, applying the
// migration chain up to v.SchemaVersion() when v implements SchemaVersioner,
// or up to the highest reachable schema otherwise.
//
// If the main file is missing, corrupt, or from an unsupported schema, the
// ".bak" copy is tried before returning the original error, so a single bad
// write never loses the data. A successful recovery is reported via SetLogger.
func LoadVersioned(path string, v any, m Migrations) error {
	target := m.Target()
	if sv, ok := v.(SchemaVersioner); ok {
		target = sv.SchemaVersion()
	}
	err := loadVersionedFile(path, target, m, v)
	if err == nil {
		return nil
	}
	bak := path + BackupSuffix
	if bakErr := loadVersionedFile(bak, target, m, v); bakErr == nil {
		if os.IsNotExist(err) {
			logf("store: %s missing, loaded %s instead", path, bak)
		} else {
			logf("store: recovered %s from %s (%v)", path, bak, err)
		}
		return nil
	}
	// The failure on the main file is the meaningful one: report it, whether
	// the backup was absent, unreadable, or equally broken.
	return err
}

func loadVersionedFile(path string, target int, m Migrations, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data, err = Migrate(data, target, m)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("store: LoadVersioned target must be a non-nil pointer, got %T", v)
	}
	// Decode into a fresh value and assign only on success: a failed attempt
	// never leaves a half-populated struct behind for the retry to mix with.
	fresh := reflect.New(rv.Type().Elem())
	if err := json.Unmarshal(data, fresh.Interface()); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	rv.Elem().Set(fresh.Elem())
	return nil
}
