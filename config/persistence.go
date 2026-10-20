package config

import (
	"encoding/json"
	"fuckyourcli/store"
	"os"
	"path/filepath"
	"strconv"
)

// configFileName is the name of the configuration file inside ConfigDir.
const configFileName = "config.json"

// getConfigPath returns the absolute path of the configuration file, creating
// its directory (0700: it may hold API keys and OAuth tokens) on first use.
func getConfigPath() string {
	dir := store.ConfigDir()
	if err := store.EnsureDir(dir, store.DirPerm); err != nil {
		store.Notice("config: cannot create %s: %v", dir, err)
	}
	ensureConfigDefenses(dir)
	return filepath.Join(dir, configFileName)
}

// configMigrations upgrades older config.json documents to the current schema.
// Version 0 is the historical file, which had no "schema" field at all.
var configMigrations = store.Migrations{
	0: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// The historical file had no "schema" field; this migration only
		// stamps v1. Hard-code the version instead of CurrentSchemaVersion so
		// the step stays correct when the current schema is bumped again.
		doc["schema"] = json.RawMessage(strconv.Itoa(1))
		return json.Marshal(doc)
	},
	1: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// Add the AGENTS.md instruction settings with their defaults, but only
		// when absent, so a user-customised value is never overwritten.
		if _, ok := doc["agents_md_enabled"]; !ok {
			doc["agents_md_enabled"] = json.RawMessage("true")
		}
		if _, ok := doc["agents_md_max_bytes"]; !ok {
			doc["agents_md_max_bytes"] = json.RawMessage(strconv.Itoa(DefaultAgentsMDMaxBytes))
		}
		if _, ok := doc["agents_md_fallback_filenames"]; !ok {
			fb, err := json.Marshal(DefaultAgentsMDFallbackFilenames)
			if err != nil {
				return nil, err
			}
			doc["agents_md_fallback_filenames"] = fb
		}
		doc["schema"] = json.RawMessage(strconv.Itoa(2))
		return json.Marshal(doc)
	},
	2: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v3 adds the status-bar hook settings (hooks.status_bar and
		// hooks.status_by_provider). They are optional and default to a no-op
		// (disabled), so only the schema version is stamped here.
		doc["schema"] = json.RawMessage(strconv.Itoa(3))
		return json.Marshal(doc)
	},
	3: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v4 adds the LSP discovery overrides (lsp.search_paths,
		// lsp.binaries, lsp.disabled). They are optional and default to empty,
		// so only the schema version is stamped here.
		doc["schema"] = json.RawMessage(strconv.Itoa(4))
		return json.Marshal(doc)
	},
	4: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v5 replaces the legacy thinking_enabled / reasoning_effort scalars with
		// a structured reasoning block. Fold the old values in so the user's
		// current behaviour is preserved (the old built-in default was
		// enabled=true, effort="high").
		enabled := true
		if raw, ok := doc["thinking_enabled"]; ok {
			_ = json.Unmarshal(raw, &enabled)
		}
		effort := "high"
		if raw, ok := doc["reasoning_effort"]; ok {
			_ = json.Unmarshal(raw, &effort)
		}
		var rc ReasoningConfig
		switch {
		case !enabled, effort == "none":
			rc.Mode = "off"
		case effort == "":
			// Thinking was on with no explicit effort: let the model decide.
			rc.Mode = "adaptive"
		default:
			rc.Mode = "effort"
			rc.Effort = effort
		}
		block, err := json.Marshal(rc)
		if err != nil {
			return nil, err
		}
		doc["reasoning"] = block
		delete(doc, "thinking_enabled")
		delete(doc, "reasoning_effort")
		doc["schema"] = json.RawMessage(strconv.Itoa(5))
		return json.Marshal(doc)
	},
	5: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v6 adds the per-provider reasoning overrides
		// (reasoning_by_provider.*). They are optional and default to empty
		// (auto-detection), so only the schema version is stamped here.
		doc["schema"] = json.RawMessage(strconv.Itoa(6))
		return json.Marshal(doc)
	},
	6: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v7 introduces the secret placeholders + auth.json split. The move
		// itself is performed by SaveE (collapse), which LoadE triggers for any
		// older document or when an inline secret is still present; here we only
		// stamp the version (see config/secrets.go).
		doc["schema"] = json.RawMessage(strconv.Itoa(7))
		return json.Marshal(doc)
	},
	7: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v8 adds browser_paths: the explicit browser executables probed by the
		// crawler before the built-in locations. It is optional and defaults to
		// empty (built-in discovery only), so only the schema version is stamped.
		doc["schema"] = json.RawMessage(strconv.Itoa(8))
		return json.Marshal(doc)
	},
	8: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		if _, ok := doc["compact_banner"]; !ok {
			doc["compact_banner"] = json.RawMessage("true")
		}
		doc["schema"] = json.RawMessage(strconv.Itoa(9))
		return json.Marshal(doc)
	},
	9: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		if _, ok := doc["compaction_mid_turn"]; !ok {
			doc["compaction_mid_turn"] = json.RawMessage("false")
		}
		doc["schema"] = json.RawMessage(strconv.Itoa(10))
		return json.Marshal(doc)
	},
	10: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		// v11 lets the compaction summary be written by a dedicated model.
		// Absent compaction_model means "current" (the model active at
		// compaction time) and absent compaction_on_failure means the
		// non-destructive fallback, so the defaults need no explicit value.
		doc["schema"] = json.RawMessage(strconv.Itoa(11))
		return json.Marshal(doc)
	},
}

// Load reads config.json into the in-memory configuration, ignoring errors.
// It is the backward-compatible wrapper around LoadE.
func Load() {
	_ = LoadE()
}

// LoadE reads config.json into the in-memory configuration.
//
// A missing file is not an error: the built-in defaults are kept. A corrupt
// file is recovered from config.json.bak by the store package; when even that
// fails the defaults are restored and the reason is logged and returned, so a
// single bad write can never leave the application without a configuration.
func LoadE() error {
	path := getConfigPath()
	onDiskSchema, existed := onDiskSchemaOf(path)
	var loaded Config
	err := store.LoadVersioned(path, &loaded, configMigrations)
	if err != nil {
		config = defaults()
		if os.IsNotExist(err) {
			return nil
		}
		store.Notice("config: %v; starting from defaults", err)
		return err
	}
	config = loaded
	if config.ToolStates == nil {
		config.ToolStates = map[string]bool{}
	}
	// Capture the on-disk expressions before resolving, so SaveE can keep an
	// untouched ${VAR} placeholder instead of overwriting it with a generated
	// name, and can tell a runtime change (e.g. a rotated Codex token) apart.
	config.rawSecrets = captureRawSecrets(loaded)
	hadInline := hasInlineSecrets(loaded)
	authStore, _ := loadAuthStore()
	noticeUnresolvedSecrets(config.rawSecrets, authStore)
	expandConfigSecrets(&config, authStore)
	// A document written by an older build is upgraded in memory by
	// store.LoadVersioned but never rewritten on disk, and an inline secret is
	// still sitting in plain text: persist once so config.json carries the new
	// default fields and the secrets move into auth.json. A file that already
	// declares the current schema with no inline secret is left untouched.
	if existed && (onDiskSchema < CurrentSchemaVersion || hadInline) {
		if err := SaveE(); err != nil {
			store.Notice("config: could not persist upgraded config: %v", err)
		}
	}
	return nil
}

// NeedsSetup reports whether the user still has to pick a provider: the
// in-memory config carries none. A fresh install starts from defaults (which
// ship no provider), so a first run triggers the wizard in both entrypoints.
// It is the gate for the first-run setup.
func NeedsSetup() bool {
	c := config
	return c.GeminiAPIKey == "" &&
		c.CodexAccessToken == "" &&
		len(c.OpenAICompatibleProviders) == 0 &&
		len(c.OpenAIResponsesCompatibleProviders) == 0 &&
		len(c.AnthropicProviders) == 0
}

// onDiskSchemaOf reports the schema version declared by the raw config.json and
// whether the file exists. A missing file yields (0, false); a file that is
// unreadable or has no parseable "schema" field yields (0, true), i.e. the
// historical no-schema format.
func onDiskSchemaOf(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	version, err := store.Schema(raw)
	if err != nil {
		return 0, true
	}
	return version, true
}

// Save persists the in-memory configuration, ignoring errors.
// It is the backward-compatible wrapper around SaveE.
func Save() {
	_ = SaveE()
}

// SaveE persists the in-memory configuration atomically, keeping the previous
// version as config.json.bak for recovery.
func SaveE() error {
	if config.ToolStates == nil {
		config.ToolStates = map[string]bool{}
	}
	if len(config.ReasoningByProvider) == 0 {
		config.ReasoningByProvider = nil // never write an empty map
	}
	config.Schema = CurrentSchemaVersion

	authStore, authCorrupt := loadAuthStore()
	out, updatedAuth, authChanged := collapseConfigSecrets(config, authStore, authCorrupt)
	out.Schema = CurrentSchemaVersion
	if authChanged {
		// auth.json is written first: if it fails, fall back to a frozen rewrite
		// that keeps every on-disk expression, so config.json never points at a
		// secret that was not actually stored.
		if err := saveAuthStore(updatedAuth); err != nil {
			store.Notice("auth: cannot persist %s: %v; keeping secrets inline", AuthFilePath(), err)
			out, _, _ = collapseConfigSecrets(config, authStore, true)
			out.Schema = CurrentSchemaVersion
		}
	}
	if err := store.WriteJSON(getConfigPath(), out, store.FilePerm, true); err != nil {
		return err
	}
	config.rawSecrets = captureRawSecrets(out)
	return nil
}
