package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fuckyourcli/store"
)

func TestSubagentLimitDefaults(t *testing.T) {
	var cfg Config
	if cfg.SubagentDefaultIterationCount() != 250 || cfg.SubagentTimeout() != 20*time.Minute {
		t.Fatalf("defaults = %d/%s", cfg.SubagentDefaultIterationCount(), cfg.SubagentTimeout())
	}
}

func TestSubagentLimitBounds(t *testing.T) {
	cfg := Config{SubagentDefaultIterations: 80, SubagentTimeoutSeconds: 5}
	if cfg.SubagentDefaultIterationCount() != 80 {
		t.Fatalf("configured iterations must be honoured, got %d", cfg.SubagentDefaultIterationCount())
	}
	if cfg.SubagentTimeout() != 30*time.Second {
		t.Fatalf("timeout below the minimum must be raised, got %s", cfg.SubagentTimeout())
	}
	// No ceiling: a hard task may legitimately need far more than any default.
	cfg = Config{SubagentDefaultIterations: 100000, SubagentTimeoutSeconds: 1000000}
	if cfg.SubagentDefaultIterationCount() != 100000 || cfg.SubagentTimeout() != 1000000*time.Second {
		t.Fatalf("budgets must not be capped: %d %s", cfg.SubagentDefaultIterationCount(), cfg.SubagentTimeout())
	}
	if got := ClampSubagentIterations(0); got != 1 {
		t.Fatalf("iteration floor = %d, want 1", got)
	}
}

// A v14 file carried a configurable iteration cap. Upgrading must retire the key
// (no silent leftover in config.json) and keep the budget the user had set.
func TestSubagentIterationCapIsDroppedOnUpgrade(t *testing.T) {
	dir := withConfigDir(t)
	legacy := `{"schema":14,"provider":"anthropic","model":"claude-x","subagent_default_iterations":40,"subagent_max_iterations":80,"subagent_timeout_seconds":600}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if got := Get().SubagentDefaultIterationCount(); got != 40 {
		t.Fatalf("iteration budget = %d, want the configured 40", got)
	}
	if got := Get().SubagentTimeout(); got != 10*time.Minute {
		t.Fatalf("timeout = %s, want 10m", got)
	}
	var raw map[string]any
	if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if _, ok := raw["subagent_max_iterations"]; ok {
		t.Fatalf("subagent_max_iterations must be dropped, got %v", raw["subagent_max_iterations"])
	}
	if got := raw["schema"]; got != float64(CurrentSchemaVersion) {
		t.Fatalf("persisted schema = %v, want %d", got, CurrentSchemaVersion)
	}
}
