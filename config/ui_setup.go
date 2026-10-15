package config

import (
	"fmt"
	"fuckyourcli/providers"
	"fuckyourcli/ui"
)

// addNewProvider shows the submenu for adding a new provider.
// Returns true if the user wants to enter chat immediately after.
func addNewProvider() bool {
	idx := ui.SelectOptionWithStyledTitle(
		ui.SelectorHeader.Render("▸  Add New Provider"),
		[]string{
			"Google Gemini (cloud)",
			"OpenAI Compatible (Chat Completions)",
			"OpenAI Responses API Compatible",
			"Anthropic Messages API",
			"OpenAI Codex (OAuth)",
			"← Back",
		},
	)
	switch idx {
	case 0:
		return setupGeminiConfig()
	case 1:
		return setupNewOpenAICompatibleProvider()
	case 2:
		return setupNewOpenAIResponsesCompatibleProvider()
	case 3:
		return setupAnthropicConfig()
	case 4:
		return setupCodexConfig()
	}
	return false
}

// setupGeminiConfig handles the configuration flow for Google Gemini.
func setupGeminiConfig() bool {
	if config.GeminiAPIKey != "" {
		if ui.SelectOption("Gemini API key is already set. Change it?", []string{"No, keep current", "Yes, enter new key"}) == 1 {
			config.GeminiAPIKey = ui.InputPassword("Enter your Gemini API key (from https://aistudio.google.com/apikey):")
		}
	} else {
		config.GeminiAPIKey = ui.InputPassword("Enter your Gemini API key (from https://aistudio.google.com/apikey):")
	}

	if config.GeminiAPIKey == "" {
		fmt.Println(ui.InfoStyle.Render("API key cannot be empty. Keeping previous configuration."))
		return false
	}

	if ui.SelectOption(
		fmt.Sprintf("Current Gemini model: %s. Change it?", config.GeminiModel),
		[]string{"No, keep current", "Yes, select new model"},
	) == 1 {
		fmt.Println(ui.InfoStyle.Render("Fetching available Gemini models..."))
		models, err := getGeminiChatModels(config.GeminiAPIKey)
		if err != nil || len(models) == 0 {
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. Using defaults."))
			models = []string{"gemini-2.0-flash", "gemini-1.5-flash", "gemini-1.5-pro"}
		}
		chosen, ok := selectModelOrCancel("Select Gemini model", models)
		if !ok {
			return false
		}
		config.GeminiModel = chosen
	}

	config.Provider = ProviderGemini
	config.Model = config.GeminiModel
	Save()
	fmt.Println(ui.OkStyle.Render("Provider set to Google Gemini"))
	fmt.Println(ui.OkStyle.Render("Model set to " + config.GeminiModel))
	return true
}

// setupAnthropicConfig handles the configuration flow for Anthropic.
func setupAnthropicConfig() bool {
	fmt.Println(ui.TitleStyle.Render("Setup Anthropic Messages API Provider"))
	fmt.Println()

	name := ui.InputText("Enter a name for this provider (e.g., 'Anthropic Direct', 'Anthropic Proxy'):")
	if name == "" {
		fmt.Println(ui.InfoStyle.Render("Provider name cannot be empty."))
		return false
	}
	if providerNameExists(name, kindAnthropic) {
		fmt.Println(ui.InfoStyle.Render("A provider with this name already exists."))
		return false
	}

	const defaultURL = "https://api.anthropic.com/v1"
	baseURL := inputWithDefault("Enter base URL", defaultURL)
	apiKey := ui.InputPassword("Enter API key (leave empty if not required):")

	models := fetchOrInputModels(
		func() ([]string, error) {
			fmt.Println(ui.InfoStyle.Render("Fetching available Anthropic models..."))
			return providers.GetAnthropicModels(baseURL, apiKey)
		},
		providers.AnthropicDefaultModels,
	)
	if models == nil {
		return false
	}

	selectedModel, ok := selectModelOrCancel("Select Anthropic model", models)
	if !ok {
		return false
	}
	AddAnthropicProvider(name, baseURL, apiKey, selectedModel)
	config.Provider = ProviderAnthropic
	config.Model = selectedModel
	config.ActiveProviderName = name
	Save()

	fmt.Println()
	fmt.Println(ui.OkStyle.Render("Anthropic provider '" + name + "' added successfully!"))
	fmt.Println(ui.OkStyle.Render("Base URL: " + baseURL))
	fmt.Println(ui.OkStyle.Render("Model: " + selectedModel))
	if apiKey == "" {
		fmt.Println(ui.InfoStyle.Render("API key: (not set)"))
	}
	return true
}

// setupNewOpenAICompatibleProvider handles the configuration flow for OpenAI-compatible providers.
func setupNewOpenAICompatibleProvider() bool {
	fmt.Println(ui.TitleStyle.Render("Setup New OpenAI Compatible Provider"))
	fmt.Println()

	name := ui.InputText("Enter a name for this provider (e.g., 'Ollama', 'Deepseek', 'OpenAI'):")
	if name == "" {
		fmt.Println(ui.InfoStyle.Render("Provider name cannot be empty."))
		return false
	}
	if providerNameExists(name, kindOpenAI) {
		fmt.Println(ui.InfoStyle.Render("A provider with this name already exists."))
		return false
	}

	const defaultURL = "http://localhost:11434/v1"
	baseURL := inputWithDefault("Enter base URL", defaultURL)
	apiKey := ui.InputPassword("Enter API key (leave empty for local providers like Ollama):")

	models := fetchOrInputModels(
		func() ([]string, error) {
			fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
			return providers.GetOpenAICompatibleModels(baseURL, apiKey)
		},
		nil,
	)
	if models == nil {
		return false
	}

	selectedModel, ok := selectModelOrCancel("Select model", models)
	if !ok {
		return false
	}
	AddOpenAICompatibleProvider(name, baseURL, apiKey, selectedModel)
	config.Provider = ProviderOpenAICompatible
	config.Model = selectedModel
	config.ActiveProviderName = name
	Save()

	fmt.Println()
	fmt.Println(ui.OkStyle.Render("Provider '" + name + "' added successfully!"))
	fmt.Println(ui.OkStyle.Render("Base URL: " + baseURL))
	fmt.Println(ui.OkStyle.Render("Model: " + selectedModel))
	if apiKey == "" {
		fmt.Println(ui.InfoStyle.Render("API key: (not set - local provider)"))
	}
	return true
}

// setupNewOpenAIResponsesCompatibleProvider handles the configuration flow
// for providers compatible with the Responses API.
func setupNewOpenAIResponsesCompatibleProvider() bool {
	fmt.Println(ui.TitleStyle.Render("Setup New OpenAI Responses API Compatible Provider"))
	fmt.Println()

	name := ui.InputText("Enter a name for this provider (e.g., 'OpenAI Responses', 'Local Responses'):")
	if name == "" {
		fmt.Println(ui.InfoStyle.Render("Provider name cannot be empty."))
		return false
	}
	if providerNameExists(name, kindResponses) {
		fmt.Println(ui.InfoStyle.Render("A Responses provider with this name already exists."))
		return false
	}

	const defaultURL = "https://api.openai.com/v1"
	baseURL := inputWithDefault("Enter base URL", defaultURL)
	apiKey := ui.InputPassword("Enter API key (leave empty if not required):")

	models := fetchOrInputModels(
		func() ([]string, error) {
			fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
			return providers.GetOpenAIResponsesCompatibleModels(baseURL, apiKey)
		},
		nil,
	)
	if models == nil {
		return false
	}

	selectedModel, ok := selectModelOrCancel("Select model", models)
	if !ok {
		return false
	}
	AddOpenAIResponsesCompatibleProvider(name, baseURL, apiKey, selectedModel)
	config.Provider = ProviderOpenAIResponsesCompatible
	config.Model = selectedModel
	config.ActiveProviderName = name
	Save()

	fmt.Println()
	fmt.Println(ui.OkStyle.Render("Responses provider '" + name + "' added successfully!"))
	fmt.Println(ui.OkStyle.Render("Base URL: " + baseURL))
	fmt.Println(ui.OkStyle.Render("Model: " + selectedModel))
	return true
}

// setupCodexConfig handles the OAuth flow + model selection for Codex.
func setupCodexConfig() bool {
	if err := ensureCodexAuth(); err != nil {
		fmt.Println(ui.InfoStyle.Render("Authentication failed: " + err.Error()))
		return false
	}

	fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
	models, err := providers.GetCodexModels(config.CodexAccessToken)
	if err != nil || len(models) == 0 {
		fmt.Println(ui.InfoStyle.Render("Could not fetch models. Using defaults."))
		models = providers.CodexDefaultModels()
	}

	selectedModel, ok := selectModelOrCancel("Select Codex model", models)
	if !ok {
		return false
	}
	config.CodexModel = selectedModel
	config.Provider = ProviderCodex
	config.Model = selectedModel
	Save()

	fmt.Println(ui.OkStyle.Render("Provider set to OpenAI Codex"))
	fmt.Println(ui.OkStyle.Render("Model: " + selectedModel))
	return true
}

// --- helpers ---

// inputWithDefault shows a prompt with a default value and uses it if the input is empty.
func inputWithDefault(prompt, defaultVal string) string {
	val := ui.InputText(fmt.Sprintf("%s (default: %s):", prompt, defaultVal))
	if val == "" {
		return defaultVal
	}
	return val
}

// selectModelOrCancel shows a model selector and returns the chosen model. ok
// is false when the user dismissed the prompt (q / Ctrl+C / Esc) without
// choosing, so the caller can abort instead of indexing past the list.
func selectModelOrCancel(title string, models []string) (string, bool) {
	idx := ui.SelectOption(title, models)
	if idx < 0 {
		return "", false
	}
	return models[idx], true
}

// fetchOrInputModels attempts to fetch via fetchFn; in case of error it uses fallbackFn
// (if not nil) or asks the user to enter the model manually.
// Returns nil if no model could be obtained.
func fetchOrInputModels(fetchFn func() ([]string, error), fallbackFn func() []string) []string {
	models, err := fetchFn()
	if err == nil && len(models) > 0 {
		return models
	}

	if fallbackFn != nil {
		fmt.Println(ui.InfoStyle.Render("Could not fetch models. Using defaults."))
		return fallbackFn()
	}

	fmt.Println(ui.InfoStyle.Render("Could not fetch models. You can enter manually."))
	manual := ui.InputText("Enter model name:")
	if manual == "" {
		fmt.Println(ui.InfoStyle.Render("Model name cannot be empty."))
		return nil
	}
	return []string{manual}
}

// providerNameExists checks if a provider with that name already exists for the given type.
func providerNameExists(name string, kind providerKind) bool {
	switch kind {
	case kindOpenAI:
		for _, p := range config.OpenAICompatibleProviders {
			if p.Name == name {
				return true
			}
		}
	case kindResponses:
		for _, p := range config.OpenAIResponsesCompatibleProviders {
			if p.Name == name {
				return true
			}
		}
	case kindAnthropic:
		for _, p := range config.AnthropicProviders {
			if p.Name == name {
				return true
			}
		}
	}
	return false
}
