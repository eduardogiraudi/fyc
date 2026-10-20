package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fuckyourcli/store"
)

// compactorTestConfig builds a configuration with one entry per provider list.
func compactorTestConfig() Config {
	return Config{
		Provider:           ProviderOpenAICompatible,
		Model:              "chat-model",
		ActiveProviderName: "Work",
		OpenAICompatibleProviders: []OpenAICompatibleProviderConfig{
			{Name: "Work", BaseURL: "https://work.test/v1", Model: "chat-model"},
			{Name: "Cheap", BaseURL: "https://cheap.test/v1", Model: "cheap-model"},
		},
		OpenAIResponsesCompatibleProviders: []OpenAICompatibleProviderConfig{
			{Name: "Cheap", BaseURL: "https://responses.test/v1", Model: "responses-model"},
		},
		AnthropicProviders: []OpenAICompatibleProviderConfig{
			{Name: "Claude", BaseURL: "https://claude.test", Model: "claude-sonnet"},
		},
		CompactionThreshold: DefaultCompactionThreshold,
	}
}

func TestCompactorDefaultsToCurrent(t *testing.T) {
	cfg := compactorTestConfig()
	choice, err := cfg.Compactor()
	if err != nil {
		t.Fatal(err)
	}
	if !choice.Current || choice.Kind != CompactorOpenAI || choice.Provider != "Work" || choice.Model != "chat-model" {
		t.Fatalf("default compactor = %+v, want the active provider", choice)
	}
	if label := cfg.CompactorLabel(); !strings.Contains(label, "chat-model") {
		t.Fatalf("label = %q", label)
	}
}

func TestCompactorResolvesPinnedModel(t *testing.T) {
	cfg := compactorTestConfig()
	cfg.CompactionModel = CompactionModelConfig{Kind: "openai", Provider: "Cheap", Model: "cheap-model"}
	choice, err := cfg.Compactor()
	if err != nil {
		t.Fatal(err)
	}
	if choice.Current || choice.Provider != "Cheap" || choice.Model != "cheap-model" {
		t.Fatalf("pinned compactor = %+v", choice)
	}
	// The same name in another list is a different provider: the kind decides.
	cfg.CompactionModel = CompactionModelConfig{Kind: "responses", Provider: "Cheap"}
	choice, err = cfg.Compactor()
	if err != nil || choice.Kind != CompactorResponses || choice.Model != "responses-model" {
		t.Fatalf("entry model not inherited: %+v %v", choice, err)
	}
	cfg.CompactionModel = CompactionModelConfig{Kind: "anthropic", Provider: "Claude", Model: "claude-haiku"}
	if choice, err = cfg.Compactor(); err != nil || choice.Label() != "Claude → claude-haiku" {
		t.Fatalf("anthropic compactor = %+v %v", choice, err)
	}
	cfg.CompactionModel = CompactionModelConfig{Kind: "gemini", Model: "gemini-2.5-flash"}
	if choice, err = cfg.Compactor(); err != nil || choice.Provider != "" || choice.Kind != CompactorGemini {
		t.Fatalf("singleton compactor = %+v %v", choice, err)
	}
}

func TestCompactorRejectsUnusableSelection(t *testing.T) {
	for _, sel := range []CompactionModelConfig{
		{Kind: "openai", Provider: "Gone", Model: "m"},
		{Kind: "openai"},
		{Kind: "nonsense", Provider: "Cheap", Model: "m"},
		{Kind: "anthropic", Model: "m"},
	} {
		cfg := compactorTestConfig()
		cfg.CompactionModel = sel
		if _, err := cfg.Compactor(); err == nil {
			t.Fatalf("compactor %+v accepted", sel)
		}
		if label := cfg.CompactorLabel(); !strings.Contains(label, "invalid compactor") {
			t.Fatalf("label = %q, want the unresolved selection to be visible", label)
		}
	}
}

func TestCompactionFailurePolicyDefaults(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want CompactionFailurePolicy
	}{
		{"", CompactionFallbackCurrent},
		{"fallback_current", CompactionFallbackCurrent},
		{" FALLBACK_CURRENT ", CompactionFallbackCurrent},
		{"abort", CompactionAbort},
		{"Abort", CompactionAbort},
		{"nonsense", CompactionFallbackCurrent},
	} {
		cfg := Config{CompactionOnFailure: tc.raw}
		if got := cfg.CompactionFailurePolicy(); got != tc.want {
			t.Fatalf("policy(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestCompactorSameTarget(t *testing.T) {
	help := CompactorChoice{Kind: CompactorOpenAI, Provider: "Cheap", Model: "cheap-model"}
	same := CompactorChoice{Current: true, Kind: CompactorOpenAI, Provider: "Cheap", Model: "cheap-model"}
	other := CompactorChoice{Current: true, Kind: CompactorOpenAI, Provider: "Work", Model: "chat-model"}
	if !help.SameTarget(same) {
		t.Fatal("the same entry and model must not trigger a duplicate request")
	}
	if help.SameTarget(other) {
		t.Fatal("a different model must be a retry target")
	}
	if (CompactorChoice{Kind: CompactorGemini, Model: "a"}).SameTarget(CompactorChoice{Kind: CompactorGemini, Model: "a"}) != true {
		t.Fatal("gemini singletons compare by model")
	}
}

func TestCompactionModelMigration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	store.SetLogger(func(string, ...any) {})
	t.Cleanup(func() { store.SetLogger(nil) })
	doc := `{"schema":10,"provider":"openai_compatible","model":"m","compaction_enabled":true,"compaction_threshold":4096}`
	if err := store.WriteFile(dir+"/config.json", []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	cfg := Get()
	if cfg.Schema != CurrentSchemaVersion || cfg.CompactionModel.Kind != "" || cfg.CompactionFailurePolicy() != CompactionFallbackCurrent {
		t.Fatalf("migrated config = %+v", cfg.CompactionModel)
	}
	if !cfg.CompactionEnabled || cfg.CompactionThreshold != 4096 {
		t.Fatal("migration lost the existing compaction settings")
	}
	// The upgraded document is persisted once, with the new schema.
	raw, err := os.ReadFile(dir + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if string(persisted["schema"]) != "11" {
		t.Fatalf("schema on disk = %s", persisted["schema"])
	}
}

func TestCompactionSelectionPinsProviderEntry(t *testing.T) {
	cfg := compactorTestConfig()
	// compactionSelection reads the package-level configuration: restore the
	// previous value rather than the defaults, in case other tests set one.
	prev := config
	defer func() { config = prev }()
	config = cfg
	for _, tc := range []struct {
		ref  providerRef
		want CompactionModelConfig
	}{
		{providerRef{kind: kindOpenAI, index: 1}, CompactionModelConfig{Kind: "openai", Provider: "Cheap", Model: "m"}},
		{providerRef{kind: kindResponses, index: 0}, CompactionModelConfig{Kind: "responses", Provider: "Cheap", Model: "m"}},
		{providerRef{kind: kindAnthropic, index: 0}, CompactionModelConfig{Kind: "anthropic", Provider: "Claude", Model: "m"}},
		{providerRef{kind: kindGemini, index: -1}, CompactionModelConfig{Kind: "gemini", Model: "m"}},
		{providerRef{kind: kindCodex, index: -1}, CompactionModelConfig{Kind: "codex", Model: "m"}},
	} {
		if got := compactionSelection(tc.ref, "m"); got != tc.want {
			t.Fatalf("selection for %v = %+v, want %+v", tc.ref, got, tc.want)
		}
	}
	if failurePolicyLabel(CompactionAbort) == failurePolicyLabel(CompactionFallbackCurrent) {
		t.Fatal("the two policies need distinct labels")
	}
}
