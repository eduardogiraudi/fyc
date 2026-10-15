package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/store"
)

func TestActiveStatusHookDefaults(t *testing.T) {
	withConfigDir(t)
	config.Hooks.Status = StatusBarHook{Enabled: true, Command: "echo hi"}
	if h := ActiveStatusHook(); !h.Enabled || h.Command != "echo hi" {
		t.Fatalf("expected the default hook, got %+v", h)
	}
	config.Hooks.Status = StatusBarHook{}
	if h := ActiveStatusHook(); h.Enabled {
		t.Fatalf("a disabled default hook must stay disabled, got %+v", h)
	}
}

func TestStatusHookProviderOverride(t *testing.T) {
	withConfigDir(t)
	config.Provider = ProviderOpenAICompatible
	config.ActiveProviderName = "DeepSeek"
	SetStatusBarHook(StatusBarHook{Enabled: true, Command: "default"})
	SetStatusBarHookForProvider("DeepSeek", StatusBarHook{Enabled: true, Command: "deepseek"})

	if key := CurrentProviderKey(); key != "DeepSeek" {
		t.Fatalf("CurrentProviderKey = %q, want DeepSeek", key)
	}
	if got := ActiveStatusHook(); got.Command != "deepseek" {
		t.Fatalf("provider override not applied: %+v", got)
	}

	// A provider-type key is the fallback when the name has no override.
	config.ActiveProviderName = "Other"
	SetStatusBarHookForProvider(string(ProviderOpenAICompatible), StatusBarHook{Enabled: true, Command: "bytype"})
	if got := ActiveStatusHook(); got.Command != "bytype" {
		t.Fatalf("type fallback not applied: %+v", got)
	}
}

func TestStatusProviderKeysIncludeDefaultAndConfigured(t *testing.T) {
	withConfigDir(t)
	config.OpenAICompatibleProviders = []OpenAICompatibleProviderConfig{{Name: "DeepSeek"}}
	SetStatusBarHookForProvider("Ghost", StatusBarHook{Enabled: true, Command: "x"})

	keys := StatusProviderKeys()
	if len(keys) == 0 || keys[0] != "" {
		t.Fatalf("the first key must be the default marker, got %v", keys)
	}
	want := map[string]bool{
		"":                     true,
		"DeepSeek":             true,
		"Ghost":                true, // kept even when no longer configured
		string(ProviderGemini): true,
		string(ProviderCodex):  true,
	}
	for _, k := range keys {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("missing keys %v (got %v)", want, keys)
	}
}

func TestStatusHookPersists(t *testing.T) {
	withConfigDir(t)
	SetStatusBarHook(StatusBarHook{
		Enabled:         true,
		Command:         "curl -s ...",
		IntervalSeconds: 30,
		RefreshOnTurn:   true,
		IncludeSecrets:  true,
	})
	SetStatusBarHookForProvider("DeepSeek", StatusBarHook{Enabled: true, Command: "ds"})
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}

	config = defaults()
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if !config.Hooks.Status.Enabled || config.Hooks.Status.Command != "curl -s ..." ||
		!config.Hooks.Status.IncludeSecrets || config.Hooks.Status.IntervalSeconds != 30 {
		t.Fatalf("status hook not persisted: %+v", config.Hooks.Status)
	}
	if got := config.Hooks.StatusByProvider["DeepSeek"].Command; got != "ds" {
		t.Fatalf("per-provider hook not persisted: %q", got)
	}
}

func TestStatusBarHookMigrationFromV2(t *testing.T) {
	dir := withConfigDir(t)
	raw, err := json.Marshal(map[string]any{
		"schema":   2,
		"provider": "openai_compatible",
		"model":    "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), raw, store.FilePerm); err != nil {
		t.Fatal(err)
	}

	config = defaults()
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.Schema != CurrentSchemaVersion {
		t.Fatalf("schema = %d, want %d", config.Schema, CurrentSchemaVersion)
	}
	if config.Hooks.Status.Enabled {
		t.Fatalf("the status-bar hook must default to disabled")
	}
}
