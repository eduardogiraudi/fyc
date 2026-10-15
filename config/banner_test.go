package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/store"
)

func TestCompactBannerDefaults(t *testing.T) {
	if !defaults().CompactBanner {
		t.Fatal("compact banner must be enabled by default")
	}
	withConfigDir(t)
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	if !Get().CompactBanner {
		t.Fatal("missing config must use compact banner")
	}
}

func TestLoadCompactBanner(t *testing.T) {
	for _, schema := range []int{0, 8, CurrentSchemaVersion} {
		for _, setting := range []string{"", `,"compact_banner":true`, `,"compact_banner":false`} {
			t.Run(fmt.Sprintf("schema_%d/%s", schema, setting), func(t *testing.T) {
				dir := withConfigDir(t)
				path := filepath.Join(dir, configFileName)
				data := fmt.Sprintf(`{"schema":%d,"model":"test"%s}`, schema, setting)
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := LoadE(); err != nil {
					t.Fatal(err)
				}
				want := setting != `,"compact_banner":false`
				if Get().CompactBanner != want {
					t.Fatalf("compact banner = %v, want %v", Get().CompactBanner, want)
				}
				if Get().Model != "test" || Get().Schema != CurrentSchemaVersion {
					t.Fatalf("model/schema lost: %+v", Get())
				}
				if schema < CurrentSchemaVersion {
					var raw map[string]any
					if err := store.ReadJSON(path, &raw); err != nil {
						t.Fatal(err)
					}
					if raw["compact_banner"] != want || raw["schema"] != float64(CurrentSchemaVersion) {
						t.Fatalf("migration not persisted: %v", raw)
					}
				}
			})
		}
	}
}

func TestCompactBannerSaveLoadRoundTrip(t *testing.T) {
	for _, compact := range []bool{true, false} {
		t.Run(fmt.Sprint(compact), func(t *testing.T) {
			withConfigDir(t)
			config = defaults()
			config.CompactBanner = compact
			if err := SaveE(); err != nil {
				t.Fatal(err)
			}
			config.CompactBanner = !compact
			if err := LoadE(); err != nil {
				t.Fatal(err)
			}
			if Get().CompactBanner != compact {
				t.Fatalf("compact banner = %v, want %v", Get().CompactBanner, compact)
			}
		})
	}
}

func TestConfigUnmarshalCompactBanner(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"model":"test"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.CompactBanner || cfg.Model != "test" {
		t.Fatalf("missing banner setting lost defaults or other fields: %+v", cfg)
	}
	if err := json.Unmarshal([]byte(`{"compact_banner":false}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CompactBanner {
		t.Fatal("explicit false must disable compact banner")
	}
}
