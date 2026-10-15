package config

import (
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/reasoning"
	"fuckyourcli/store"
)

func TestReasoningConfigIntentRoundTrip(t *testing.T) {
	cases := []ReasoningConfig{
		{Mode: "default"},
		{Mode: "off"},
		{Mode: "adaptive"},
		{Mode: "effort", Effort: "high"},
		{Mode: "budget", Budget: 8192},
	}
	for _, rc := range cases {
		in := rc.Intent()
		if back := FromIntent(in); back.Mode != rc.Mode || back.Effort != rc.Effort || back.Budget != rc.Budget {
			t.Errorf("round trip %+v -> %+v -> %+v", rc, in, back)
		}
	}
}

func TestReasoningConfigIntentModes(t *testing.T) {
	cases := map[string]reasoning.Mode{
		"":         reasoning.ModeDefault,
		"bogus":    reasoning.ModeDefault,
		"default":  reasoning.ModeDefault,
		"off":      reasoning.ModeOff,
		"adaptive": reasoning.ModeAdaptive,
		"effort":   reasoning.ModeEffort,
		"budget":   reasoning.ModeBudget,
		"OFF":      reasoning.ModeOff, // case-insensitive
	}
	for mode, want := range cases {
		if got := (ReasoningConfig{Mode: mode}).Intent().Mode; got != want {
			t.Errorf("mode %q -> %v, want %v", mode, got, want)
		}
	}
}

func TestReasoningDefaultIsDefensive(t *testing.T) {
	if got := defaults().Reasoning.Intent().Mode; got != reasoning.ModeDefault {
		t.Fatalf("fresh install reasoning mode = %v, want the defensive default", got)
	}
}

func TestReasoningMigrationFromLegacyScalars(t *testing.T) {
	cases := []struct {
		name string
		json string
		want ReasoningConfig
	}{
		{"enabled+effort", `{"schema":4,"thinking_enabled":true,"reasoning_effort":"high"}`,
			ReasoningConfig{Mode: "effort", Effort: "high"}},
		{"disabled", `{"schema":4,"thinking_enabled":false,"reasoning_effort":"high"}`,
			ReasoningConfig{Mode: "off"}},
		{"effort none", `{"schema":4,"thinking_enabled":true,"reasoning_effort":"none"}`,
			ReasoningConfig{Mode: "off"}},
		{"enabled no effort", `{"schema":4,"thinking_enabled":true,"reasoning_effort":""}`,
			ReasoningConfig{Mode: "adaptive"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := withConfigDir(t)
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(tc.json), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := LoadE(); err != nil {
				t.Fatalf("LoadE: %v", err)
			}
			got := config.Reasoning
			if got.Mode != tc.want.Mode || got.Effort != tc.want.Effort {
				t.Fatalf("reasoning = %+v, want %+v", got, tc.want)
			}

			// The legacy keys must be gone from the persisted document.
			var raw map[string]any
			if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
				t.Fatal(err)
			}
			if _, ok := raw["thinking_enabled"]; ok {
				t.Fatalf("thinking_enabled not removed: %v", raw)
			}
			if _, ok := raw["reasoning_effort"]; ok {
				t.Fatalf("reasoning_effort not removed: %v", raw)
			}
			if raw["schema"] != float64(CurrentSchemaVersion) {
				t.Fatalf("schema = %v, want %d", raw["schema"], CurrentSchemaVersion)
			}
		})
	}
}

func TestReasoningLabel(t *testing.T) {
	cases := []struct {
		rc   ReasoningConfig
		want string
	}{
		{ReasoningConfig{Mode: "default"}, "default (server decides)"},
		{ReasoningConfig{Mode: "off"}, "off"},
		{ReasoningConfig{Mode: "adaptive"}, "adaptive"},
		{ReasoningConfig{Mode: "effort", Effort: "x"}, "effort: x"},
		{ReasoningConfig{Mode: "budget", Budget: 100}, "budget: 100 tokens"},
	}
	for _, tc := range cases {
		if got := tc.rc.Label(); got != tc.want {
			t.Errorf("%+v.Label() = %q, want %q", tc.rc, got, tc.want)
		}
	}
}
