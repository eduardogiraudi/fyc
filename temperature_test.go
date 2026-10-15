package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/models"
	"fuckyourcli/store"
	"fuckyourcli/ui"
)

func temperatureConfigLoader(t *testing.T) func(config.Config) {
	t.Helper()
	original := config.Get()
	restoreDir := t.TempDir()
	if err := store.WriteJSON(filepath.Join(restoreDir, "config.json"), original, 0o600, false); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	t.Cleanup(func() {
		os.Setenv("FYC_CONFIG_DIR", restoreDir)
		if err := config.LoadE(); err != nil {
			t.Errorf("restore config: %v", err)
		}
		config.SetNoAsk(original.NoAsk)
		models.Invalidate()
	})
	const catalog = `{"test":{"id":"test","api":"https://example.test/v1","models":{
		"no-temp":{"id":"no-temp","temperature":false},
		"with-temp":{"id":"with-temp","temperature":true},
		"undocumented-temp":{"id":"undocumented-temp"}
	}}}`
	if err := store.WriteFile(models.Path(), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	models.Invalidate()
	return func(cfg config.Config) {
		t.Helper()
		cfg.Schema = config.CurrentSchemaVersion
		if err := store.WriteJSON(filepath.Join(dir, "config.json"), cfg, 0o600, false); err != nil {
			t.Fatal(err)
		}
		if err := config.LoadE(); err != nil {
			t.Fatal(err)
		}
	}
}

func temperatureConfig(pt config.ProviderType, model, mode string) config.Config {
	cfg := config.Config{
		Provider: pt, Model: model, Temperature: 0.7, ActiveProviderName: "test",
		Reasoning: config.ReasoningConfig{Mode: mode, Effort: "high", Budget: 8192},
	}
	p := config.OpenAICompatibleProviderConfig{Name: "test", BaseURL: "https://example.test/v1", Model: model}
	switch pt {
	case config.ProviderOpenAICompatible:
		cfg.OpenAICompatibleProviders = []config.OpenAICompatibleProviderConfig{p}
	case config.ProviderOpenAIResponsesCompatible:
		cfg.OpenAIResponsesCompatibleProviders = []config.OpenAICompatibleProviderConfig{p}
	case config.ProviderAnthropic:
		cfg.AnthropicProviders = []config.OpenAICompatibleProviderConfig{p}
	}
	return cfg
}

func TestCurrentTemperatureLabel(t *testing.T) {
	load := temperatureConfigLoader(t)
	for _, pt := range []config.ProviderType{
		config.ProviderOpenAICompatible, config.ProviderOpenAIResponsesCompatible,
		config.ProviderAnthropic, config.ProviderGemini,
	} {
		for _, mode := range []string{"default", "off", "effort", "budget", "adaptive"} {
			for _, model := range []string{"no-temp", "with-temp", "undocumented-temp", "unknown"} {
				t.Run(string(pt)+"/"+mode+"/"+model, func(t *testing.T) {
					load(temperatureConfig(pt, model, mode))
					want := "0.70"
					if model == "no-temp" || (pt == config.ProviderAnthropic && mode != "default" && mode != "off") {
						want = "Omitted"
					}
					if got := currentTemperatureLabel(); got != want {
						t.Fatalf("temperature label = %q, want %q", got, want)
					}
					if config.Get().Temperature != 0.7 {
						t.Fatal("rendering changed the saved preference")
					}
				})
			}
		}
	}
	for _, mode := range []string{"default", "off", "effort"} {
		load(temperatureConfig(config.ProviderCodex, "with-temp", mode))
		if got := currentTemperatureLabel(); got != "Omitted" {
			t.Fatalf("Codex label = %q", got)
		}
	}
	for _, mode := range []string{"default", "off"} {
		load(temperatureConfig(config.ProviderAnthropic, "claude-opus-4-7", mode))
		if got := currentTemperatureLabel(); got != "Omitted" {
			t.Fatalf("adaptive-only Anthropic label = %q", got)
		}
	}
}

func TestTemperatureUsesProviderOverride(t *testing.T) {
	load := temperatureConfigLoader(t)
	cfg := temperatureConfig(config.ProviderAnthropic, "claude-opus-4-7", "effort")
	cfg.ReasoningByProvider = map[string]config.ReasoningProviderConfig{
		"test": {Profile: &config.ReasoningProfileConfig{}},
	}
	load(cfg)
	if got := currentTemperatureLabel(); got != "0.70" {
		t.Fatalf("forced profile should replace detection, got %q", got)
	}
	config.SetReasoningForProvider("test", config.ReasoningProviderConfig{
		Profile: &config.ReasoningProfileConfig{ForbidsTemperature: true},
	})
	if got := currentTemperatureLabel(); got != "Omitted" {
		t.Fatalf("forced restriction was ignored: %q", got)
	}
	load(temperatureConfig(config.ProviderAnthropic, "claude-sonnet-4-6", "effort"))
	config.SetReasoningForProvider("test", config.ReasoningProviderConfig{Intent: config.ReasoningConfig{Mode: "off"}})
	if got := currentTemperatureLabel(); got != "0.70" {
		t.Fatalf("provider's thinking-off intent was ignored: %q", got)
	}
}

type temperatureSink struct {
	*recordSink
	providerInfo string
}

func (s *temperatureSink) WriteStats(provider, _ string) { s.providerInfo = provider }

func TestSlashTemperatureSavesPreferenceAndWarns(t *testing.T) {
	load := temperatureConfigLoader(t)
	const warning = "Warning: Temperature is not supported by the current model; preference saved. It will be used when a model that supports it is selected."
	for _, model := range []string{"no-temp", "with-temp"} {
		t.Run(model, func(t *testing.T) {
			load(temperatureConfig(config.ProviderOpenAICompatible, model, "default"))
			sink := &temperatureSink{recordSink: &recordSink{}}
			s := &ChatSession{Sink: sink}
			if handled, modified := handleSlashCommand("/temp 0.8", s); !handled || modified != "" {
				t.Fatalf("command not handled: %v %q", handled, modified)
			}
			if !strings.Contains(sink.text(), "Temperature set to 0.80") {
				t.Fatalf("missing confirmation: %s", sink.text())
			}
			if warned := strings.Contains(sink.text(), warning); warned != (model == "no-temp") {
				t.Fatalf("unexpected warning: %s", sink.text())
			}
			want := "0.80"
			if model == "no-temp" {
				want = "Omitted"
			}
			if !strings.Contains(sink.providerInfo, want) {
				t.Fatalf("status bar not refreshed: %q", sink.providerInfo)
			}
			var saved config.Config
			if err := store.ReadJSON(filepath.Join(store.ConfigDir(), "config.json"), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Temperature != 0.8 || config.Get().Temperature != 0.8 {
				t.Fatal("temperature preference was not saved")
			}
			if err := config.LoadE(); err != nil {
				t.Fatal(err)
			}
			if config.Get().Temperature != 0.8 {
				t.Fatal("temperature preference did not survive reload")
			}
		})
	}
}

func TestSlashTemperatureQueryAndInvalidValues(t *testing.T) {
	load := temperatureConfigLoader(t)
	load(temperatureConfig(config.ProviderOpenAICompatible, "no-temp", "default"))
	for _, input := range []string{"/temp", "/temp ", "/temp invalid", "/temp -1", "/temp 3"} {
		sink := &recordSink{}
		handleSlashCommand(input, &ChatSession{Sink: sink})
		if config.Get().Temperature != 0.7 || strings.Contains(sink.text(), "preference saved") {
			t.Fatalf("query/invalid input changed preference: %q: %s", input, sink.text())
		}
		if strings.TrimSpace(input) == "/temp" && !strings.Contains(sink.text(), "Current temperature: 0.70") {
			t.Fatalf("query failed: %s", sink.text())
		}
	}
}

func TestTemperatureStatusRefreshAfterModelAndReasoningChanges(t *testing.T) {
	load := temperatureConfigLoader(t)
	load(temperatureConfig(config.ProviderOpenAICompatible, "no-temp", "default"))
	m := newChatModel(&ChatSession{}, nil, nil, nil)
	if !strings.Contains(m.statsProviderInfo, "Omitted") {
		t.Fatalf("initial status = %q", m.statsProviderInfo)
	}
	load(temperatureConfig(config.ProviderOpenAICompatible, "with-temp", "default"))
	updated, _ := m.Update(generationDoneMsg{})
	m = updated.(chatModel)
	if strings.Contains(m.statsProviderInfo, "Omitted") || !strings.Contains(m.statsProviderInfo, "0.70") {
		t.Fatalf("model switch left stale status: %q", m.statsProviderInfo)
	}
	load(temperatureConfig(config.ProviderAnthropic, "claude-sonnet-4-6", "effort"))
	updated, _ = m.Update(generationDoneMsg{})
	m = updated.(chatModel)
	if !strings.Contains(m.statsProviderInfo, "Omitted") {
		t.Fatalf("thinking change left stale status: %q", m.statsProviderInfo)
	}
	config.SetReasoning(config.ReasoningConfig{Mode: "off"})
	updated, _ = m.Update(generationDoneMsg{})
	m = updated.(chatModel)
	if strings.Contains(m.statsProviderInfo, "Omitted") {
		t.Fatalf("thinking off left stale status: %q", m.statsProviderInfo)
	}
	sink := &temperatureSink{recordSink: &recordSink{}}
	toggleAutoMode(sink)
	if !strings.Contains(sink.providerInfo, "0.70") {
		t.Fatalf("auto toggle lost temperature: %q", sink.providerInfo)
	}
	config.SetReasoning(config.ReasoningConfig{Mode: "effort", Effort: "high"})
	toggleAutoMode(sink)
	if !strings.Contains(sink.providerInfo, "Omitted") {
		t.Fatalf("auto toggle lost omission: %q", sink.providerInfo)
	}
}

// The renderer accepts the same resolved label used by the startup banner.
func TestTemperatureProviderRenderer(t *testing.T) {
	load := temperatureConfigLoader(t)
	load(temperatureConfig(config.ProviderOpenAICompatible, "no-temp", "default"))
	line := ui.RenderProviderInfo("test", currentTemperatureLabel(), "manual")
	if !strings.Contains(line, "Omitted") || strings.Contains(line, "0.70") {
		t.Fatalf("renderer displayed an inactive preference: %q", line)
	}
}

func TestTemperatureUsesActualRequestModel(t *testing.T) {
	load := temperatureConfigLoader(t)
	cfg := temperatureConfig(config.ProviderOpenAICompatible, "with-temp", "default")
	cfg.OpenAICompatibleProviders[0].Model = "no-temp"
	load(cfg)
	if currentTemperatureLabel() != "Omitted" {
		t.Fatal("active provider's request model must win over the global model")
	}
	// A catalog omission still wins over an extra-body temperature.
	config.SetReasoning(config.ReasoningConfig{ExtraBody: map[string]any{"temperature": 1.2}})
	if currentTemperatureLabel() != "Omitted" {
		t.Fatal("extra body bypassed the request's omission policy")
	}
}
