package main

import (
	"fmt"
	"slices"

	"fuckyourcli/config"
	"fuckyourcli/reasoning"
	"fuckyourcli/ui"
)

// Use the request's resolved plan so catalog facts, thinking and overrides agree
// with the UI. Codex never sends the configured temperature.
func currentTemperatureOmitted() bool {
	cfg := config.Get()
	if cfg.Provider == config.ProviderCodex {
		return true
	}
	model, baseURL := cfg.Model, ""
	var list []config.OpenAICompatibleProviderConfig
	switch cfg.Provider {
	case config.ProviderOpenAICompatible:
		list = cfg.OpenAICompatibleProviders
	case config.ProviderOpenAIResponsesCompatible:
		list = cfg.OpenAIResponsesCompatibleProviders
	case config.ProviderAnthropic:
		list = cfg.AnthropicProviders
	}
	if p := findActiveProvider(list, cfg.ActiveProviderName, cfg.Model); p != nil {
		model = p.Model
		if cfg.Provider != config.ProviderAnthropic {
			baseURL = p.BaseURL
		}
	}
	intent, override := config.ActiveReasoning()
	transport := transportForProvider(cfg.Provider)
	profile, _ := reasoning.Lookup(transport, baseURL, model, override)
	plan, _ := reasoning.Translate(intent, profile)
	path := "temperature"
	if transport == reasoning.GeminiGenerateContent {
		path = "generationConfig.temperature"
	}
	return slices.Contains(plan.Omit, path)
}

func currentTemperatureLabel() string {
	if currentTemperatureOmitted() {
		return "Omitted"
	}
	return fmt.Sprintf("%.2f", config.Get().Temperature)
}

func currentProviderLine() string {
	mode := "manual"
	if config.Get().NoAsk {
		mode = "auto"
	}
	return ui.RenderProviderInfo(config.GetCurrentProviderInfo(), currentTemperatureLabel(), mode)
}
