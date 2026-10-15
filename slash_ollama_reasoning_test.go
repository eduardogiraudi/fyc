package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/models"
)

func TestReasoningCommandOllamaCloudAndLocal(t *testing.T) {
	original, err := json.Marshal(config.Get())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	restoreDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(restoreDir, "config.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYC_CONFIG_DIR", dir)
	t.Cleanup(func() {
		// Restore the process-wide config without touching the user's config.
		os.Setenv("FYC_CONFIG_DIR", restoreDir)
		if err := config.LoadE(); err != nil {
			t.Errorf("restore config: %v", err)
		}
		models.Invalidate()
	})
	const catalog = `{"ollama-cloud":{"id":"ollama-cloud","api":"https://ollama.com/v1","models":{
		"gemma4:31b":{"id":"gemma4:31b","reasoning":true,"reasoning_options":[{"type":"toggle"}]}
	}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	models.Invalidate()
	for _, baseURL := range []string{"http://localhost:11434/v1", "https://ollama.com/v1"} {
		for _, responses := range []bool{false, true} {
			t.Run(baseURL+"/responses="+stringBool(responses), func(t *testing.T) {
				cfg := config.Config{
					Schema: config.CurrentSchemaVersion,
					Model:  "gemma4:31b-cloud", ActiveProviderName: "Ollama",
					Reasoning: config.ReasoningConfig{Mode: "effort", Effort: "high"},
				}
				provider := config.OpenAICompatibleProviderConfig{Name: "Ollama", BaseURL: baseURL, Model: cfg.Model}
				wantPreview := "sends now: reasoning_effort=high"
				if responses {
					cfg.Provider = config.ProviderOpenAIResponsesCompatible
					cfg.OpenAIResponsesCompatibleProviders = []config.OpenAICompatibleProviderConfig{provider}
					wantPreview = "sends now: reasoning.effort=high"
				} else {
					cfg.Provider = config.ProviderOpenAICompatible
					cfg.OpenAICompatibleProviders = []config.OpenAICompatibleProviderConfig{provider}
				}
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := config.LoadE(); err != nil {
					t.Fatal(err)
				}
				sink := &recordSink{selectIndex: -1}
				handleReasoningCommand(&ChatSession{Sink: sink})
				text := sink.text()
				for _, want := range []string{"Model: gemma4:31b-cloud", "detected: gemma (model)", "catalog ✓", wantPreview} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q in /reasoning:\n%s", want, text)
					}
				}
				if strings.Contains(text, "model not recognised") {
					t.Fatalf("Gemma still shows the unknown-model warning:\n%s", text)
				}
			})
		}
	}
}

func stringBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
