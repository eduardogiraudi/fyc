package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/store"
)

func TestMaxMediaDefaults(t *testing.T) {
	if got := defaults().MaxMedia; got != 10 {
		t.Fatalf("default max media = %d, want 10", got)
	}
	withConfigDir(t)
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	if Get().MaxMedia != 10 || EffectiveMaxMedia() != 10 {
		t.Fatal("missing config must use the media default")
	}
}

func TestLoadMaxMedia(t *testing.T) {
	for _, schema := range []int{0, 11, CurrentSchemaVersion} {
		for _, tc := range []struct {
			setting   string
			stored    int
			effective int
		}{
			{"", 10, 10},
			{`,"max_media":1`, 1, 1},
			{`,"max_media":5`, 5, 5},
			{`,"max_media":20`, 20, 20},
			{`,"max_media":0`, 0, 10},
			{`,"max_media":-3`, -3, 10},
		} {
			t.Run(fmt.Sprintf("schema_%d/%s", schema, tc.setting), func(t *testing.T) {
				dir := withConfigDir(t)
				path := filepath.Join(dir, configFileName)
				data := fmt.Sprintf(`{"schema":%d,"model":"test"%s}`, schema, tc.setting)
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := LoadE(); err != nil {
					t.Fatal(err)
				}
				if Get().MaxMedia != tc.stored || EffectiveMaxMedia() != tc.effective {
					t.Fatalf("max media = %d (effective %d), want %d (%d)", Get().MaxMedia, EffectiveMaxMedia(), tc.stored, tc.effective)
				}
				if Get().Model != "test" || Get().Schema != CurrentSchemaVersion {
					t.Fatal("migration lost the model or schema")
				}
				if schema < CurrentSchemaVersion {
					var raw map[string]any
					if err := store.ReadJSON(path, &raw); err != nil {
						t.Fatal(err)
					}
					if raw["max_media"] != float64(tc.stored) || raw["schema"] != float64(CurrentSchemaVersion) {
						t.Fatalf("migration not persisted: max_media=%v schema=%v", raw["max_media"], raw["schema"])
					}
				} else {
					raw, err := os.ReadFile(path)
					if err != nil || string(raw) != data {
						t.Fatal("current-schema config was unexpectedly rewritten")
					}
				}
			})
		}
	}
}

func TestMaxMediaSaveLoadRoundTrip(t *testing.T) {
	dir := withConfigDir(t)
	config = defaults()
	for _, limit := range []int{1, 5, 10, 25} {
		SetMaxMedia(limit)
		if Get().MaxMedia != limit || EffectiveMaxMedia() != limit {
			t.Fatal("setter did not update the active limit")
		}
		if err := SaveE(); err != nil {
			t.Fatal(err)
		}
		SetMaxMedia(99)
		if err := LoadE(); err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
			t.Fatal(err)
		}
		if raw["max_media"] != float64(limit) || Get().MaxMedia != limit || EffectiveMaxMedia() != limit {
			t.Fatalf("media limit %d did not survive save/load", limit)
		}
	}
}

func TestConfigUnmarshalMaxMedia(t *testing.T) {
	var cfg Config
	for _, tc := range []struct {
		data string
		want int
	}{
		{`{"model":"test"}`, 10},
		{`{"model":"test","max_media":3}`, 3},
		{`{"model":"test","max_media":0}`, 0},
	} {
		if err := json.Unmarshal([]byte(tc.data), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.MaxMedia != tc.want || cfg.Model != "test" || !cfg.CompactBanner {
			t.Fatal("unmarshal lost media defaults, explicit settings or unrelated fields")
		}
	}
}
