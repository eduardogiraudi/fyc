package config

import (
	"fmt"
	"fuckyourcli/providers"
	"fuckyourcli/ui"
)

// providerKind identifies the provider type without magic numbers.
type providerKind string

const (
	kindGemini    providerKind = "gemini"
	kindCodex     providerKind = "codex"
	kindOpenAI    providerKind = "openai"
	kindResponses providerKind = "responses"
	kindAnthropic providerKind = "anthropic"
)

// providerRef is a typed reference to a provider in the configuration.
// It replaces the old system of encoded indices (-1, -2, -100-i, -200-i).
type providerRef struct {
	kind  providerKind
	index int // index in the slice; -1 for singleton providers (Gemini, Codex)
	label string
}

// buildProviderList builds the list of available providers to show in the menu.
func buildProviderList() (labels []string, refs []providerRef) {
	if config.GeminiAPIKey != "" {
		labels = append(labels, fmt.Sprintf("Google Gemini (%s)", config.GeminiModel))
		refs = append(refs, providerRef{kind: kindGemini, index: -1})
	}
	for i, p := range config.OpenAICompatibleProviders {
		labels = append(labels, fmt.Sprintf("%s (%s)", p.Name, p.Model))
		refs = append(refs, providerRef{kind: kindOpenAI, index: i})
	}
	for i, p := range config.OpenAIResponsesCompatibleProviders {
		labels = append(labels, fmt.Sprintf("%s [Responses] (%s)", p.Name, p.Model))
		refs = append(refs, providerRef{kind: kindResponses, index: i})
	}
	if config.CodexAccessToken != "" {
		labels = append(labels, fmt.Sprintf("OpenAI Codex (%s)", config.CodexModel))
		refs = append(refs, providerRef{kind: kindCodex, index: -1})
	}
	for i, p := range config.AnthropicProviders {
		labels = append(labels, fmt.Sprintf("%s [Anthropic] (%s)", p.Name, p.Model))
		refs = append(refs, providerRef{kind: kindAnthropic, index: i})
	}
	return
}

// selectProvider shows all configured providers and allows selecting an active one.
// Returns true if the user wants to enter the chat.
func selectProvider() bool {
	labels, refs := buildProviderList()

	if len(labels) == 0 {
		fmt.Println(ui.WarnBox.Render("No providers configured yet. Add one first from 'Add New Provider'."))
		return false
	}

	labels = append(labels, "← Back")
	idx := ui.SelectOptionWithStyledTitle(
		ui.SelectorHeader.Render("▸  Select Active Provider")+"\n"+
			ui.InfoStyle.Render("  Current: "+GetCurrentProviderInfo()),
		labels,
	)
	if idx < 0 || idx >= len(refs) {
		return false
	}

	if !activateProvider(refs[idx]) {
		// The user dismissed the follow-up model prompt (q / Ctrl+C / Esc):
		// treat it as "back", not as "enter chat".
		return false
	}
	return true
}

// activateProvider sets the active provider, saves the configuration and
// offers the user the possibility to change the model. It returns false when
// the user dismisses the model prompt (q / Ctrl+C / Esc), so the caller can
// stay in the configuration menu instead of proceeding.
func activateProvider(ref providerRef) bool {
	switch ref.kind {
	case kindGemini:
		config.Provider = ProviderGemini
		config.Model = config.GeminiModel
		Save()
		fmt.Println(ui.OkStyle.Render("Provider set to Google Gemini"))
		fmt.Println(ui.OkStyle.Render("Model: " + config.GeminiModel))
		return maybeChangeModel(ref)

	case kindCodex:
		config.Provider = ProviderCodex
		config.Model = config.CodexModel
		Save()
		fmt.Println(ui.OkStyle.Render("Provider set to OpenAI Codex"))
		fmt.Println(ui.OkStyle.Render("Model: " + config.CodexModel))
		return maybeChangeModel(ref)

	case kindAnthropic:
		p := config.AnthropicProviders[ref.index]
		config.Provider = ProviderAnthropic
		config.Model = p.Model
		config.ActiveProviderName = p.Name
		Save()
		fmt.Println(ui.OkStyle.Render("Provider set to " + p.Name))
		fmt.Println(ui.OkStyle.Render("Model: " + config.Model))
		return maybeChangeModel(ref)

	case kindResponses:
		p := config.OpenAIResponsesCompatibleProviders[ref.index]
		config.Provider = ProviderOpenAIResponsesCompatible
		config.Model = p.Model
		config.ActiveProviderName = p.Name
		Save()
		fmt.Println(ui.OkStyle.Render("Provider set to " + p.Name))
		fmt.Println(ui.OkStyle.Render("Model: " + config.Model))
		return maybeChangeModel(ref)

	case kindOpenAI:
		p := config.OpenAICompatibleProviders[ref.index]
		config.Provider = ProviderOpenAICompatible
		config.Model = p.Model
		config.ActiveProviderName = p.Name
		Save()
		fmt.Println(ui.OkStyle.Render("Provider set to " + p.Name))
		fmt.Println(ui.OkStyle.Render("Model: " + config.Model))
		return maybeChangeModel(ref)
	}
	return true
}

// currentModelForRef returns the current model for a given provider.
func currentModelForRef(ref providerRef) string {
	switch ref.kind {
	case kindGemini:
		return config.GeminiModel
	case kindCodex:
		return config.CodexModel
	case kindAnthropic:
		return config.AnthropicProviders[ref.index].Model
	case kindResponses:
		return config.OpenAIResponsesCompatibleProviders[ref.index].Model
	case kindOpenAI:
		return config.OpenAICompatibleProviders[ref.index].Model
	}
	return ""
}

// fetchModelsForRef retrieves the available models for a given provider.
// In case of error or empty list, it returns nil (the caller handles the fallback).
func fetchModelsForRef(ref providerRef) ([]string, error) {
	switch ref.kind {
	case kindGemini:
		fmt.Println(ui.InfoStyle.Render("Fetching available Gemini models..."))
		return getGeminiChatModels(config.GeminiAPIKey)

	case kindCodex:
		fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
		return providers.GetCodexModels(config.CodexAccessToken)

	case kindAnthropic:
		p := config.AnthropicProviders[ref.index]
		fmt.Println(ui.InfoStyle.Render("Fetching available Anthropic models..."))
		return providers.GetAnthropicModels(p.BaseURL, p.APIKey)

	case kindResponses:
		p := config.OpenAIResponsesCompatibleProviders[ref.index]
		fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
		return providers.GetOpenAIResponsesCompatibleModels(p.BaseURL, p.APIKey)

	case kindOpenAI:
		p := config.OpenAICompatibleProviders[ref.index]
		fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
		return providers.GetOpenAICompatibleModels(p.BaseURL, p.APIKey)
	}
	return nil, nil
}

// defaultModelsForRef returns the default (fallback) models for a provider.
func defaultModelsForRef(ref providerRef) []string {
	switch ref.kind {
	case kindGemini:
		return []string{"gemini-2.0-flash", "gemini-1.5-flash", "gemini-1.5-pro"}
	case kindCodex:
		return providers.CodexDefaultModels()
	case kindAnthropic:
		return providers.AnthropicDefaultModels()
	default:
		// For OpenAI and Responses there is no sensible default: it is asked manually.
		return nil
	}
}

// applyModelForRef updates the chosen model in the provider and in config.Model.
func applyModelForRef(ref providerRef, model string) {
	config.Model = model
	switch ref.kind {
	case kindGemini:
		config.GeminiModel = model
	case kindCodex:
		config.CodexModel = model
	case kindAnthropic:
		config.AnthropicProviders[ref.index].Model = model
	case kindResponses:
		config.OpenAIResponsesCompatibleProviders[ref.index].Model = model
	case kindOpenAI:
		config.OpenAICompatibleProviders[ref.index].Model = model
	}
	Save()
	fmt.Println(ui.OkStyle.Render("Model updated to " + model))
}

// maybeChangeModel asks the user if they want to change the model and handles
// the fetch -> fallback -> selection flow uniformly for all providers.
// maybeChangeModel asks the user if they want to change the model and handles
// the fetch -> fallback -> selection flow uniformly for all providers. It
// returns false when the user dismisses a prompt (q / Ctrl+C / Esc) instead of
// answering, so the caller can abort the flow and stay in the menu.
func maybeChangeModel(ref providerRef) bool {
	current := currentModelForRef(ref)
	switch ui.SelectOption(
		fmt.Sprintf("Current model: %s. Change it?", current),
		[]string{"No, keep current", "Yes, select new model"},
	) {
	case 1:
		// "Yes, select new model": fall through to the selection flow below.
	case ui.SelectorCanceled:
		return false // dismissed: abort, stay in the menu
	default:
		return true // "No, keep current"
	}

	models, err := fetchModelsForRef(ref)
	if err != nil || len(models) == 0 {
		defaults := defaultModelsForRef(ref)
		if len(defaults) > 0 {
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. Using defaults."))
			models = defaults
		} else {
			// Provider without defaults (e.g. OpenAI Responses): manual input
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. You can enter manually."))
			manual := ui.InputText("Enter model name:")
			if manual == "" {
				return true
			}
			applyModelForRef(ref, manual)
			return true
		}
	}

	mIdx := ui.SelectOption("Select model", models)
	if mIdx < 0 {
		return false // dismissed the model list: abort
	}
	applyModelForRef(ref, models[mIdx])
	return true
}
