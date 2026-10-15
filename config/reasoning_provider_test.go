package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/reasoning"
	"fuckyourcli/store"
)

func TestReasoningProviderIntentOr(t *testing.T) {
	global := ReasoningConfig{Mode: "effort", Effort: "high", ExtraBody: map[string]any{"a": 1}}

	// An empty override inherits everything.
	got := (ReasoningProviderConfig{}).IntentOr(global)
	if got.Mode != "effort" || got.Effort != "high" || got.ExtraBody["a"] != 1 {
		t.Fatalf("empty override must inherit the global preference, got %+v", got)
	}

	// A mode-only override replaces the mode but keeps the global extra body.
	got = (ReasoningProviderConfig{Intent: ReasoningConfig{Mode: "off"}}).IntentOr(global)
	if got.Mode != "off" || got.Effort != "high" || got.ExtraBody["a"] != 1 {
		t.Fatalf("mode-only override = %+v", got)
	}

	// Explicit fields win; an explicit extra body replaces the global one.
	got = (ReasoningProviderConfig{Intent: ReasoningConfig{
		Mode: "budget", Budget: 4096, ExtraBody: map[string]any{"b": 2},
	}}).IntentOr(global)
	if got.Mode != "budget" || got.Budget != 4096 || got.ExtraBody["b"] != 2 {
		t.Fatalf("full override = %+v", got)
	}
	if _, stale := got.ExtraBody["a"]; stale {
		t.Fatalf("an explicit extra body must replace, not merge: %+v", got.ExtraBody)
	}
}

func TestReasoningProfileToProfile(t *testing.T) {
	always := map[string]any{"generationConfig.thinkingConfig.includeThoughts": true}
	src := &ReasoningProfileConfig{
		TogglePath:          "chat_template_kwargs.enable_thinking",
		ToggleEnabledValue:  true,
		ToggleDisabledValue: false,
		EffortPath:          "reasoning_effort",
		EffortValues:        []string{"low", "high"},
		EffortMap:           map[string]string{"minimal": "low"},
		BudgetPath:          "thinking.budget_tokens",
		BudgetMin:           1024,
		BudgetMax:           8192,
		ForbidsTemperature:  true,
		CannotDisable:       true,
		Always:              always,
	}
	pc := ReasoningProviderConfig{Profile: src}
	prof := pc.Profile.ToProfile()
	if prof == nil {
		t.Fatal("ToProfile returned nil")
	}
	if prof.TogglePath != "chat_template_kwargs.enable_thinking" || prof.ToggleEnabledValue != true {
		t.Fatalf("toggle not converted: %+v", prof)
	}
	if prof.EffortPath != "reasoning_effort" || len(prof.EffortValues) != 2 || prof.EffortMap["minimal"] != "low" {
		t.Fatalf("effort not converted: %+v", prof)
	}
	if prof.BudgetPath != "thinking.budget_tokens" || prof.BudgetMin != 1024 || prof.BudgetMax != 8192 {
		t.Fatalf("budget not converted: %+v", prof)
	}
	if !prof.ForbidsTemperature || !prof.CannotDisable || len(prof.Always) != 1 {
		t.Fatalf("flags not converted: %+v", prof)
	}

	// The conversion clones the maps and slices: editing the config afterwards
	// must not change the profile the providers already hold.
	src.EffortValues[0] = "mutated"
	src.Always["generationConfig.thinkingConfig.includeThoughts"] = false
	if prof.EffortValues[0] != "low" || prof.Always["generationConfig.thinkingConfig.includeThoughts"] != true {
		t.Fatalf("ToProfile must clone its inputs: %+v", prof)
	}

	// A nil placement means "auto-detect": no override at all.
	if (*ReasoningProfileConfig)(nil).ToProfile() != nil {
		t.Fatal("a nil profile must convert to a nil override")
	}
}

func TestReasoningProviderSetClearAndKeys(t *testing.T) {
	withConfigDir(t)

	if _, ok := ReasoningForProvider("DeepSeek"); ok {
		t.Fatal("no override expected on a fresh config")
	}
	SetReasoningForProvider("DeepSeek", ReasoningProviderConfig{
		Profile: &ReasoningProfileConfig{TogglePath: "thinking.type"},
	})
	pc, ok := ReasoningForProvider("DeepSeek")
	if !ok || pc.Profile == nil || pc.Profile.TogglePath != "thinking.type" {
		t.Fatalf("override not stored: %+v ok=%v", pc, ok)
	}

	// An empty edit removes the entry, so config.json keeps no dead keys.
	SetReasoningForProvider("DeepSeek", ReasoningProviderConfig{Intent: ReasoningConfig{Mode: "off"}})
	if _, ok := ReasoningForProvider("DeepSeek"); !ok {
		t.Fatal("an intent-only override must be kept")
	}
	SetReasoningForProvider("DeepSeek", ReasoningProviderConfig{})
	if _, ok := ReasoningForProvider("DeepSeek"); ok {
		t.Fatal("a zero override must be dropped")
	}
	if config.ReasoningByProvider != nil {
		t.Fatal("an empty map must be pruned back to nil")
	}

	// The key list covers every configured provider, the provider types and any
	// stale entry that is still stored.
	config.OpenAICompatibleProviders = []OpenAICompatibleProviderConfig{{Name: "DeepSeek"}}
	SetReasoningForProvider("Ghost", ReasoningProviderConfig{Profile: &ReasoningProfileConfig{BudgetPath: "b"}})
	keys := ReasoningProviderKeys()
	want := map[string]bool{
		"DeepSeek":             true,
		"Ghost":                true,
		string(ProviderGemini): true,
		string(ProviderCodex):  true,
	}
	for _, k := range keys {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("missing keys %v (got %v)", want, keys)
	}
	ClearReasoningForProvider("Ghost")
	if _, ok := ReasoningForProvider("Ghost"); ok {
		t.Fatal("ClearReasoningForProvider left the entry behind")
	}
}

func TestActiveReasoningUsesProviderOverride(t *testing.T) {
	withConfigDir(t)
	config.Provider = ProviderOpenAICompatible
	config.ActiveProviderName = "DeepSeek"
	config.Reasoning = ReasoningConfig{Mode: "effort", Effort: "high"}
	SetReasoningForProvider("DeepSeek", ReasoningProviderConfig{
		Intent:  ReasoningConfig{Mode: "off"},
		Profile: &ReasoningProfileConfig{TogglePath: "thinking.type", ToggleEnabledValue: "enabled"},
	})

	intent, prof := ActiveReasoning()
	if intent.Mode != reasoning.ModeOff {
		t.Fatalf("intent = %+v, want the provider override", intent)
	}
	if prof == nil || prof.TogglePath != "thinking.type" || prof.ToggleEnabledValue != "enabled" {
		t.Fatalf("profile = %+v, want the provider override", prof)
	}

	// Another provider keeps the global preference and auto-detection.
	config.ActiveProviderName = "Other"
	intent, prof = ActiveReasoning()
	if intent.Mode != reasoning.ModeEffort || intent.Effort != "high" {
		t.Fatalf("global intent = %+v", intent)
	}
	if prof != nil {
		t.Fatalf("no override expected, got %+v", prof)
	}
}

func TestReasoningProviderPersists(t *testing.T) {
	dir := withConfigDir(t)
	config.Provider = ProviderGemini
	SetReasoningForProvider(string(ProviderGemini), ReasoningProviderConfig{
		Profile: &ReasoningProfileConfig{
			TogglePath:          "generationConfig.thinkingConfig.thinkingBudget",
			BudgetMax:           8192,
			ToggleEnabledValue:  true,
			ToggleDisabledValue: false,
		},
	})
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}

	// The document carries the map under its stable key.
	var raw map[string]any
	if err := store.ReadJSON(filepath.Join(dir, configFileName), &raw); err != nil {
		t.Fatal(err)
	}
	byProvider, ok := raw["reasoning_by_provider"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning_by_provider missing from config.json: %v", raw)
	}
	if _, ok := byProvider[string(ProviderGemini)]; !ok {
		t.Fatalf("provider key missing: %v", byProvider)
	}

	config = defaults()
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	pc, ok := ReasoningForProvider(string(ProviderGemini))
	if !ok || pc.Profile == nil || pc.Profile.BudgetMax != 8192 || pc.Profile.ToggleEnabledValue != true {
		t.Fatalf("override not reloaded: %+v ok=%v", pc, ok)
	}
}

func TestReasoningProviderMigrationFromV5(t *testing.T) {
	dir := withConfigDir(t)
	raw, err := json.Marshal(map[string]any{
		"schema":    5,
		"provider":  "openai_compatible",
		"model":     "m",
		"reasoning": map[string]any{"mode": "effort", "effort": "high"},
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
	if config.Reasoning.Mode != "effort" || config.Reasoning.Effort != "high" {
		t.Fatalf("the global preference must survive the migration: %+v", config.Reasoning)
	}
	if len(config.ReasoningByProvider) != 0 {
		t.Fatalf("no per-provider override expected, got %+v", config.ReasoningByProvider)
	}
}
