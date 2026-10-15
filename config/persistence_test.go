package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/store"
)

// withConfigDir points the configuration at a throw-away directory and resets
// the package-level state afterwards.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	t.Setenv("FYC_AUTH_FILE", "")
	store.SetLogger(func(string, ...any) {})
	t.Cleanup(func() {
		store.SetLogger(nil)
		config = defaults()
	})
	return dir
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := withConfigDir(t)

	config.Provider = ProviderAnthropic
	config.Model = "claude-test"
	config.CodexModel = "gpt-5"
	config.ToolStates = map[string]bool{"ls": true, "read": false}
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}

	config = defaults()
	config.Provider = ProviderGemini // must be overwritten by the load
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.Provider != ProviderAnthropic || config.Model != "claude-test" || config.CodexModel != "gpt-5" {
		t.Fatalf("loaded config = %+v", config)
	}
	if !config.ToolStates["ls"] || config.ToolStates["read"] {
		t.Fatalf("ToolStates = %v", config.ToolStates)
	}
	if config.Schema != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", config.Schema, CurrentSchemaVersion)
	}
	if _, err := os.Stat(filepath.Join(dir, configFileName)); err != nil {
		t.Fatalf("config file not written: %v", err)
	}
}

func TestSavedFileDeclaresSchema(t *testing.T) {
	dir := withConfigDir(t)
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}
	var raw map[string]any
	if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
		t.Fatal(err)
	}
	if got := raw["schema"]; got != float64(CurrentSchemaVersion) {
		t.Fatalf("schema = %v, want %d", got, CurrentSchemaVersion)
	}
}

func TestSecondSaveKeepsBackup(t *testing.T) {
	dir := withConfigDir(t)
	config.Provider = ProviderGemini
	if err := SaveE(); err != nil {
		t.Fatalf("first SaveE: %v", err)
	}
	config.Provider = ProviderAnthropic
	if err := SaveE(); err != nil {
		t.Fatalf("second SaveE: %v", err)
	}
	var bak Config
	if err := store.ReadJSON(filepath.Join(dir, configFileName+store.BackupSuffix), &bak); err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if bak.Provider != ProviderGemini {
		t.Fatalf("backup provider = %q, want %q", bak.Provider, ProviderGemini)
	}
}

func TestLoadMigratesLegacyFile(t *testing.T) {
	dir := withConfigDir(t)
	legacy := `{"provider":"anthropic","model":"claude-x","tool_states":{"read":true}}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.Provider != ProviderAnthropic || config.Model != "claude-x" {
		t.Fatalf("legacy values lost: %+v", config)
	}
	if !config.ToolStates["read"] {
		t.Fatalf("legacy ToolStates lost: %v", config.ToolStates)
	}
	if config.Schema != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want migrated to %d", config.Schema, CurrentSchemaVersion)
	}
}

func TestLoadRecoversFromBackup(t *testing.T) {
	dir := withConfigDir(t)
	main := filepath.Join(dir, configFileName)
	if err := os.WriteFile(main, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := `{"schema":1,"provider":"gemini","model":"gemini-2.0-flash"}`
	if err := os.WriteFile(main+store.BackupSuffix, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.Provider != ProviderGemini {
		t.Fatalf("provider = %q, want recovered %q", config.Provider, ProviderGemini)
	}
}

func TestLoadCorruptWithoutBackupFallsBackToDefaults(t *testing.T) {
	dir := withConfigDir(t)
	main := filepath.Join(dir, configFileName)
	if err := os.WriteFile(main, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := LoadE()
	if !errors.Is(err, store.ErrCorrupt) {
		t.Fatalf("err = %v, want store.ErrCorrupt", err)
	}
	if config.Provider != ProviderOpenAICompatible {
		t.Fatalf("expected defaults after corruption, got %+v", config)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	withConfigDir(t)
	config = defaults()
	config.Provider = ProviderGemini
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE on missing file: %v", err)
	}
	if config.Provider != ProviderOpenAICompatible {
		t.Fatalf("expected defaults, got %+v", config)
	}
}

func TestSaveLoadWrappersDoNotPanic(t *testing.T) {
	withConfigDir(t)
	config.Provider = ProviderCodex
	Save()
	config = defaults()
	Load()
	if config.Provider != ProviderCodex {
		t.Fatalf("Load() did not restore the saved provider: %+v", config)
	}
}

// TestConfigMigrationsTargetCurrent guarantees the migration chain can reach the
// schema the current build understands (a missing step would break every load).
func TestConfigMigrationsTargetCurrent(t *testing.T) {
	if got := configMigrations.Target(); got != CurrentSchemaVersion {
		t.Fatalf("migration target = %d, want CurrentSchemaVersion %d", got, CurrentSchemaVersion)
	}
}

// TestLoadBackfillsAndPersistsAgentsMDDefaults checks the backward-compat path:
// a v1 config.json without the agents_md_* fields is migrated in memory and
// rewritten on disk with the defaults, so the file carries the new keys.
func TestLoadBackfillsAndPersistsAgentsMDDefaults(t *testing.T) {
	dir := withConfigDir(t)
	legacy := `{"schema":1,"provider":"anthropic","model":"claude-x"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if !config.AgentsMDEnabled || config.AgentsMDMaxBytes != DefaultAgentsMDMaxBytes {
		t.Fatalf("in-memory defaults not applied: %+v", config)
	}
	if len(config.AgentsMDFallbackFilenames) != len(DefaultAgentsMDFallbackFilenames) {
		t.Fatalf("fallback filenames = %v", config.AgentsMDFallbackFilenames)
	}

	var raw map[string]any
	if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if got := raw["schema"]; got != float64(CurrentSchemaVersion) {
		t.Fatalf("persisted schema = %v, want %d", got, CurrentSchemaVersion)
	}
	if raw["agents_md_enabled"] != true {
		t.Fatalf("agents_md_enabled not persisted: %v", raw["agents_md_enabled"])
	}
	if got := raw["agents_md_max_bytes"]; got != float64(DefaultAgentsMDMaxBytes) {
		t.Fatalf("agents_md_max_bytes = %v, want %d", got, DefaultAgentsMDMaxBytes)
	}
}

// TestLoadKeepsCustomAgentsMDValues ensures a user-edited value is never
// overwritten by the migration while the document is still upgraded to the
// current schema.
func TestLoadKeepsCustomAgentsMDValues(t *testing.T) {
	dir := withConfigDir(t)
	legacy := `{"schema":1,"agents_md_enabled":false,"agents_md_max_bytes":1024,"agents_md_fallback_filenames":["FOO.md"]}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.AgentsMDEnabled {
		t.Fatal("custom agents_md_enabled=false was overwritten")
	}
	if config.AgentsMDMaxBytes != 1024 {
		t.Fatalf("agents_md_max_bytes = %d, want 1024", config.AgentsMDMaxBytes)
	}
	if len(config.AgentsMDFallbackFilenames) != 1 || config.AgentsMDFallbackFilenames[0] != "FOO.md" {
		t.Fatalf("fallback filenames = %v, want [FOO.md]", config.AgentsMDFallbackFilenames)
	}

	var raw map[string]any
	if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if got := raw["schema"]; got != float64(CurrentSchemaVersion) {
		t.Fatalf("persisted schema = %v, want %d", got, CurrentSchemaVersion)
	}
}

// TestLoadMissingFileDoesNotCreateConfig makes sure the backfill-and-save logic
// only fires for an existing older document: a plain first run must not drop a
// config.json behind the user.
func TestLoadMissingFileDoesNotCreateConfig(t *testing.T) {
	dir := withConfigDir(t)
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, configFileName)); !os.IsNotExist(err) {
		t.Fatalf("config.json should not be created on a missing-file load (err=%v)", err)
	}
}

// TestDefaultsHaveNoProviderModel pins the removal of the built-in model: a
// fresh install must not ship a hard-coded (qwen) default that looks configured.
func TestDefaultsHaveNoProviderModel(t *testing.T) {
	if got := defaults().Model; got != "" {
		t.Fatalf("defaults().Model = %q, want empty", got)
	}
}

// TestNeedsSetupOnFreshInstall: with no config.json the wizard must run.
func TestNeedsSetupOnFreshInstall(t *testing.T) {
	withConfigDir(t)
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if !NeedsSetup() {
		t.Fatal("NeedsSetup() = false on a fresh install, want true")
	}
}

// TestNeedsSetupWithProvider: a config carrying a provider must not re-run the
// wizard.
func TestNeedsSetupWithProvider(t *testing.T) {
	dir := withConfigDir(t)
	cfg := `{"schema":3,"provider":"openai_compatible","model":"llama3","active_provider_name":"Ollama",
		"openai_compatible_providers":[{"name":"Ollama","base_url":"http://localhost:11434/v1","model":"llama3"}]}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if NeedsSetup() {
		t.Fatal("NeedsSetup() = true with a configured provider, want false")
	}
}

// TestNeedsSetupWithoutProvider: a config.json that exists but carries no
// provider (e.g. recovered from corruption) still needs the wizard.
func TestNeedsSetupWithoutProvider(t *testing.T) {
	dir := withConfigDir(t)
	cfg := `{"schema":3,"provider":"openai_compatible","model":""}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if !NeedsSetup() {
		t.Fatal("NeedsSetup() = false with no provider configured, want true")
	}
}

// TestNeedsSetupClearsAfterAddingProvider pins the first-run loop bug: once a
// provider is present in the in-memory config the wizard must not re-arm.
func TestNeedsSetupClearsAfterAddingProvider(t *testing.T) {
	withConfigDir(t)
	if err := LoadE(); err != nil { // fresh install → defaults
		t.Fatalf("LoadE: %v", err)
	}
	if !NeedsSetup() {
		t.Fatal("NeedsSetup() = false before any provider was added")
	}
	AddOpenAICompatibleProvider("Ollama", "http://localhost:11434/v1", "", "llama3")
	if NeedsSetup() {
		t.Fatal("NeedsSetup() = true after adding a provider, want false")
	}
}

func TestMidTurnCompactionDefaultsAndMigration(t *testing.T) {
	if defaults().CompactionMidTurn {
		t.Fatal("mid-turn compaction must be opt-in")
	}
	for _, value := range []string{"", `,"compaction_mid_turn":false`, `,"compaction_mid_turn":true`} {
		t.Run(value, func(t *testing.T) {
			dir := withConfigDir(t)
			data := `{"schema":9,"compaction_enabled":true,"compaction_threshold":12345` + value + `}`
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := LoadE(); err != nil {
				t.Fatal(err)
			}
			want := value == `,"compaction_mid_turn":true`
			if config.CompactionMidTurn != want || !config.CompactionEnabled || config.CompactionThreshold != 12345 {
				t.Fatalf("migration changed settings: %+v", config)
			}
			var raw map[string]any
			if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
				t.Fatal(err)
			}
			if raw["compaction_mid_turn"] != want || raw["schema"] != float64(CurrentSchemaVersion) {
				t.Fatalf("migration not persisted: %v", raw)
			}
			SetCompactionMidTurn(!want)
			if err := SaveE(); err != nil {
				t.Fatal(err)
			}
			if err := LoadE(); err != nil {
				t.Fatal(err)
			}
			if config.CompactionMidTurn != !want {
				t.Fatal("setting did not survive reload")
			}
		})
	}
}

func TestCurrentSchemaMissingMidTurnCompactionIsDisabled(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"schema":10,"compaction_enabled":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.CompactionMidTurn {
		t.Fatal("missing setting enabled mid-turn compaction")
	}
}
