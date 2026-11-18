package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/config"
)

type providerMenuSink struct {
	recordSink
	titles []string
}

func (s *providerMenuSink) SelectOption(title string, options []string) int {
	s.titles = append(s.titles, title)
	return s.recordSink.SelectOption(title, options)
}

func TestProviderSwitchOffersConfiguredModelWithoutFetching(t *testing.T) {
	load := temperatureConfigLoader(t)
	for _, pt := range []config.ProviderType{config.ProviderOpenAICompatible, config.ProviderOpenAIResponsesCompatible, config.ProviderAnthropic, config.ProviderGemini, config.ProviderCodex} {
		t.Run(string(pt), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			cfg := temperatureConfig(config.ProviderOpenAICompatible, "old", "default")
			p := config.OpenAICompatibleProviderConfig{Name: "target", BaseURL: server.URL, Model: "remembered"}
			switch pt {
			case config.ProviderOpenAICompatible:
				cfg.OpenAICompatibleProviders = append(cfg.OpenAICompatibleProviders, p)
			case config.ProviderOpenAIResponsesCompatible:
				cfg.OpenAIResponsesCompatibleProviders = []config.OpenAICompatibleProviderConfig{p}
			case config.ProviderAnthropic:
				cfg.AnthropicProviders = []config.OpenAICompatibleProviderConfig{p}
			case config.ProviderGemini:
				cfg.GeminiAPIKey, cfg.GeminiModel = "test-key", "remembered"
			case config.ProviderCodex:
				cfg.CodexAccessToken, cfg.CodexModel = "test-token", "remembered"
			}
			load(cfg)
			idx := 1
			if pt == config.ProviderGemini {
				idx = 0
			}
			sink := &providerMenuSink{recordSink: recordSink{selectQueue: []int{idx, 0, 0}}}
			handleSlashCommand("/provider", &ChatSession{Sink: sink})
			if config.GetProvider() != pt || config.Get().Model != "remembered" || requests != 0 {
				t.Fatalf("provider = %s, model = %s, requests = %d", config.GetProvider(), config.Get().Model, requests)
			}
			if len(sink.titles) != 3 || !strings.HasPrefix(sink.titles[1], "Use current model") {
				t.Fatalf("unexpected prompts: %q", sink.titles)
			}
		})
	}
}

func TestProviderSwitchChoosingAndCancellationPreserveConfig(t *testing.T) {
	load := temperatureConfigLoader(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "another"}, {"id": "remembered"}}})
	}))
	defer server.Close()
	cfg := temperatureConfig(config.ProviderOpenAICompatible, "old", "default")
	cfg.OpenAICompatibleProviders = append(cfg.OpenAICompatibleProviders, config.OpenAICompatibleProviderConfig{Name: "target", BaseURL: server.URL, Model: "remembered"})
	for _, queue := range [][]int{{-1}, {1, -1}, {1, 1, -1}} {
		load(cfg)
		before := config.Get()
		sink := &providerMenuSink{recordSink: recordSink{selectQueue: queue}}
		handleSlashCommand("/provider", &ChatSession{Sink: sink})
		if !reflect.DeepEqual(before, config.Get()) {
			t.Fatalf("cancel %v changed the configuration", queue)
		}
	}
	load(cfg)
	sink := &providerMenuSink{recordSink: recordSink{selectQueue: []int{1, 1, 0, 0}}}
	handleSlashCommand("/provider", &ChatSession{Sink: sink})
	if config.Get().ActiveProviderName != "target" || config.Get().Model != "another" {
		t.Fatalf("wrong selection: %s / %s", config.Get().ActiveProviderName, config.Get().Model)
	}
	if config.Get().OpenAICompatibleProviders[0].Model != "old" {
		t.Fatal("switch changed the original provider's model")
	}
}
