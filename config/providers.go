package config

import (
	"encoding/json"
	"fmt"
	"fuckyourcli/providers"
	"io"
	"net/http"
	"strings"
)

func getGeminiChatModels(apiKey string) ([]string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Models []geminiModelInfo `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var chatModels []string
	for _, m := range result.Models {
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				// Strip the "models/" prefix if present
				name := strings.TrimPrefix(m.Name, "models/")
				chatModels = append(chatModels, name)
				break
			}
		}
	}
	return chatModels, nil
}

// AddAnthropicProvider adds a new Anthropic Messages API provider.
func AddAnthropicProvider(name, baseURL, apiKey, model string) {
	provider := OpenAICompatibleProviderConfig{
		Name:    name,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	}
	config.AnthropicProviders = append(config.AnthropicProviders, provider)
}

// RemoveAnthropicProvider removes an Anthropic provider by name.
func RemoveAnthropicProvider(name string) {
	var newProviders []OpenAICompatibleProviderConfig
	for _, p := range config.AnthropicProviders {
		if p.Name != name {
			newProviders = append(newProviders, p)
		}
	}
	config.AnthropicProviders = newProviders
}

// AddOpenAIResponsesCompatibleProvider adds a new OpenAI Responses API-compatible provider.
func AddOpenAIResponsesCompatibleProvider(name, baseURL, apiKey, model string) {
	provider := OpenAICompatibleProviderConfig{
		Name:    name,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	}
	config.OpenAIResponsesCompatibleProviders = append(config.OpenAIResponsesCompatibleProviders, provider)
}

// RemoveOpenAIResponsesCompatibleProvider removes a Responses provider by name.
func RemoveOpenAIResponsesCompatibleProvider(name string) {
	var newProviders []OpenAICompatibleProviderConfig
	for _, p := range config.OpenAIResponsesCompatibleProviders {
		if p.Name != name {
			newProviders = append(newProviders, p)
		}
	}
	config.OpenAIResponsesCompatibleProviders = newProviders
}

// RemoveOpenAICompatibleProvider removes a provider by name
func RemoveOpenAICompatibleProvider(name string) {
	var newProviders []OpenAICompatibleProviderConfig
	for _, p := range config.OpenAICompatibleProviders {
		if p.Name != name {
			newProviders = append(newProviders, p)
		}
	}
	config.OpenAICompatibleProviders = newProviders
}

// SwitchToOpenAIProvider switches to a specific OpenAI provider by name and sets its model
func SwitchToOpenAIProvider(name, model string, saveToFile bool) error {
	found := false
	for i, p := range config.OpenAICompatibleProviders {
		if p.Name == name {
			config.Provider = ProviderOpenAICompatible
			config.OpenAICompatibleProviders[i].Model = model
			config.Model = model
			config.ActiveProviderName = name
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("Provider '%s' not found", name)
	}

	if saveToFile {
		Save()
	}
	return nil
}

// SwitchToOpenAIResponsesProvider switches to a specific Responses provider by name and sets its model.
func SwitchToOpenAIResponsesProvider(name, model string, saveToFile bool) error {
	found := false
	for i, p := range config.OpenAIResponsesCompatibleProviders {
		if p.Name == name {
			config.Provider = ProviderOpenAIResponsesCompatible
			config.OpenAIResponsesCompatibleProviders[i].Model = model
			config.Model = model
			config.ActiveProviderName = name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("Responses provider '%s' not found", name)
	}
	if saveToFile {
		Save()
	}
	return nil
}

// SwitchToAnthropicProvider switches to a specific Anthropic Messages API provider by name and sets its model.
func SwitchToAnthropicProvider(name, model string, saveToFile bool) error {
	found := false
	for i, p := range config.AnthropicProviders {
		if p.Name == name {
			config.Provider = ProviderAnthropic
			config.AnthropicProviders[i].Model = model
			config.Model = model
			config.ActiveProviderName = name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("Anthropic provider '%s' not found", name)
	}
	if saveToFile {
		Save()
	}
	return nil
}

// GetCurrentProviderInfo returns a human-readable string describing current provider and model
func GetCurrentProviderInfo() string {
	cfg := Get()

	if cfg.Provider == ProviderGemini {
		return fmt.Sprintf("Google Gemini (%s)", cfg.Model)
	} else if cfg.Provider == ProviderOpenAICompatible {
		// Find provider name
		if cfg.ActiveProviderName != "" {
			for _, p := range cfg.OpenAICompatibleProviders {
				if p.Name == cfg.ActiveProviderName {
					return fmt.Sprintf("%s (%s)", p.Name, cfg.Model)
				}
			}
		}
		// Fallback: match by model
		for _, p := range cfg.OpenAICompatibleProviders {
			if p.Model == cfg.Model {
				return fmt.Sprintf("%s (%s)", p.Name, cfg.Model)
			}
		}
		return fmt.Sprintf("OpenAI Compatible (%s)", cfg.Model)
	} else if cfg.Provider == ProviderOpenAIResponsesCompatible {
		if cfg.ActiveProviderName != "" {
			for _, p := range cfg.OpenAIResponsesCompatibleProviders {
				if p.Name == cfg.ActiveProviderName {
					return fmt.Sprintf("%s [Responses] (%s)", p.Name, cfg.Model)
				}
			}
		}
		// Fallback: match by model
		for _, p := range cfg.OpenAIResponsesCompatibleProviders {
			if p.Model == cfg.Model {
				return fmt.Sprintf("%s [Responses] (%s)", p.Name, cfg.Model)
			}
		}
		return fmt.Sprintf("OpenAI Responses Compatible (%s)", cfg.Model)
	} else if cfg.Provider == ProviderCodex {
		return fmt.Sprintf("OpenAI Codex (%s)", cfg.Model)
	} else if cfg.Provider == ProviderAnthropic {
		if cfg.ActiveProviderName != "" {
			for _, p := range cfg.AnthropicProviders {
				if p.Name == cfg.ActiveProviderName {
					return fmt.Sprintf("%s [Anthropic] (%s)", p.Name, cfg.Model)
				}
			}
		}
		// Fallback: match by model
		for _, p := range cfg.AnthropicProviders {
			if p.Model == cfg.Model {
				return fmt.Sprintf("%s [Anthropic] (%s)", p.Name, cfg.Model)
			}
		}
		return fmt.Sprintf("Anthropic (%s)", cfg.Model)
	}

	return fmt.Sprintf("Unknown Provider (%s)", cfg.Model)
}

// ActiveProviderEndpoint returns the base URL and API key/token of the active
// provider. It reuses the same selection logic as GetCurrentProviderInfo so the
// status-bar hook can reach a provider's own endpoint without duplicating it.
func ActiveProviderEndpoint() (baseURL, apiKey string) {
	cfg := config
	find := func(list []OpenAICompatibleProviderConfig) *OpenAICompatibleProviderConfig {
		if cfg.ActiveProviderName != "" {
			for i := range list {
				if list[i].Name == cfg.ActiveProviderName {
					return &list[i]
				}
			}
		}
		for i := range list {
			if list[i].Model == cfg.Model {
				return &list[i]
			}
		}
		return nil
	}
	switch cfg.Provider {
	case ProviderOpenAICompatible:
		if p := find(cfg.OpenAICompatibleProviders); p != nil {
			return p.BaseURL, p.APIKey
		}
	case ProviderOpenAIResponsesCompatible:
		if p := find(cfg.OpenAIResponsesCompatibleProviders); p != nil {
			return p.BaseURL, p.APIKey
		}
	case ProviderAnthropic:
		if p := find(cfg.AnthropicProviders); p != nil {
			return p.BaseURL, p.APIKey
		}
	case ProviderGemini:
		return "", cfg.GeminiAPIKey
	case ProviderCodex:
		return "", cfg.CodexAccessToken
	}
	return "", ""
}

// GetAvailableProviders returns list of available providers for switching
func GetAvailableProviders() []string {
	cfg := Get()
	providers := []string{}

	// Add Gemini if configured
	if cfg.GeminiAPIKey != "" {
		providers = append(providers, fmt.Sprintf("Google Gemini (%s)", cfg.GeminiModel))
	}

	// Add all OpenAI compatible providers
	for _, p := range cfg.OpenAICompatibleProviders {
		providers = append(providers, fmt.Sprintf("%s (%s)", p.Name, p.Model))
	}
	for _, p := range cfg.OpenAIResponsesCompatibleProviders {
		providers = append(providers, fmt.Sprintf("%s [Responses] (%s)", p.Name, p.Model))
	}
	// Add Codex if configured
	if cfg.CodexAccessToken != "" {
		providers = append(providers, fmt.Sprintf("OpenAI Codex (%s)", cfg.CodexModel))
	}
	for _, p := range cfg.AnthropicProviders {
		providers = append(providers, fmt.Sprintf("%s [Anthropic] (%s)", p.Name, p.Model))
	}

	return providers
}

// ParseProviderSelection parses a provider selection string and returns provider type and model
func ParseProviderSelection(selection string) (ProviderType, string, error) {
	cfg := Get()

	if strings.HasPrefix(selection, "Google Gemini") {
		// Extract model from parentheses
		start := strings.Index(selection, "(")
		end := strings.Index(selection, ")")
		if start != -1 && end != -1 && end > start {
			model := selection[start+1 : end]
			return ProviderGemini, model, nil
		}
		return ProviderGemini, cfg.GeminiModel, nil
	}
	if strings.HasPrefix(selection, "OpenAI Codex") {
		start := strings.Index(selection, "(")
		end := strings.Index(selection, ")")
		if start != -1 && end != -1 && end > start {
			model := selection[start+1 : end]
			return ProviderCodex, model, nil
		}
		return ProviderCodex, cfg.CodexModel, nil
	}
	if strings.Contains(selection, " [Anthropic] ") {
		for _, p := range cfg.AnthropicProviders {
			providerStr := fmt.Sprintf("%s [Anthropic] (%s)", p.Name, p.Model)
			if selection == providerStr {
				return ProviderAnthropic, p.Model, nil
			}
		}
		// Fallback: try to match just "Anthropic" prefix
		start := strings.Index(selection, "(")
		end := strings.Index(selection, ")")
		if start != -1 && end != -1 && end > start {
			model := selection[start+1 : end]
			return ProviderAnthropic, model, nil
		}
		if len(cfg.AnthropicProviders) > 0 {
			return ProviderAnthropic, cfg.AnthropicProviders[0].Model, nil
		}
		return ProviderAnthropic, cfg.Model, nil
	}
	if strings.Contains(selection, " [Responses] ") {
		for _, p := range cfg.OpenAIResponsesCompatibleProviders {
			providerStr := fmt.Sprintf("%s [Responses] (%s)", p.Name, p.Model)
			if selection == providerStr {
				return ProviderOpenAIResponsesCompatible, p.Model, nil
			}
		}
	}

	// Check OpenAI compatible providers
	for _, p := range cfg.OpenAICompatibleProviders {
		providerStr := fmt.Sprintf("%s (%s)", p.Name, p.Model)
		if selection == providerStr {
			return ProviderOpenAICompatible, p.Model, nil
		}
	}

	return "", "", fmt.Errorf("Invalid provider selection: %s", selection)
}

// GetAvailableModelsForCurrentProvider returns available models for the current provider
func GetAvailableModelsForCurrentProvider() ([]string, error) {
	cfg := Get()

	if cfg.Provider == ProviderGemini {
		if cfg.GeminiAPIKey == "" {
			return []string{cfg.GeminiModel}, nil
		}
		chatModels, err := getGeminiChatModels(cfg.GeminiAPIKey)
		if err != nil || len(chatModels) == 0 {
			// Return current model as fallback
			return []string{cfg.GeminiModel}, nil
		}
		return chatModels, nil

	} else if cfg.Provider == ProviderOpenAICompatible {
		// Find the active OpenAI compatible provider by name
		var activeProvider *OpenAICompatibleProviderConfig
		if cfg.ActiveProviderName != "" {
			for _, p := range cfg.OpenAICompatibleProviders {
				if p.Name == cfg.ActiveProviderName {
					activeProvider = &p
					break
				}
			}
		}
		if activeProvider == nil {
			// Fallback: match by model (backward compat)
			for _, p := range cfg.OpenAICompatibleProviders {
				if p.Model == cfg.Model {
					activeProvider = &p
					break
				}
			}
		}

		if activeProvider == nil {
			return []string{cfg.Model}, nil
		}

		var models []string
		var err error
		if activeProvider.APIKey != "" {
			models, err = providers.GetOpenAICompatibleModels(activeProvider.BaseURL, activeProvider.APIKey)
		} else {
			models, err = providers.GetOpenAICompatibleModels(activeProvider.BaseURL, "")
		}

		if err != nil || len(models) == 0 {
			return []string{cfg.Model}, nil
		}
		return models, nil
	} else if cfg.Provider == ProviderOpenAIResponsesCompatible {
		var activeProvider *OpenAICompatibleProviderConfig
		if cfg.ActiveProviderName != "" {
			for _, p := range cfg.OpenAIResponsesCompatibleProviders {
				if p.Name == cfg.ActiveProviderName {
					activeProvider = &p
					break
				}
			}
		}
		if activeProvider == nil {
			// Fallback: match by model (backward compat)
			for _, p := range cfg.OpenAIResponsesCompatibleProviders {
				if p.Model == cfg.Model {
					activeProvider = &p
					break
				}
			}
		}
		if activeProvider == nil {
			return []string{cfg.Model}, nil
		}
		models, err := providers.GetOpenAIResponsesCompatibleModels(activeProvider.BaseURL, activeProvider.APIKey)
		if err != nil || len(models) == 0 {
			return []string{cfg.Model}, nil
		}
		return models, nil
	} else if cfg.Provider == ProviderCodex {
		if cfg.CodexAccessToken == "" {
			return providers.CodexDefaultModels(), nil
		}
		models, err := providers.GetCodexModels(cfg.CodexAccessToken)
		if err != nil || len(models) == 0 {
			return providers.CodexDefaultModels(), nil
		}
		return models, nil
	} else if cfg.Provider == ProviderAnthropic {
		var activeProvider *OpenAICompatibleProviderConfig
		if cfg.ActiveProviderName != "" {
			for _, p := range cfg.AnthropicProviders {
				if p.Name == cfg.ActiveProviderName {
					activeProvider = &p
					break
				}
			}
		}
		if activeProvider == nil {
			// Fallback: match by model (backward compat)
			for _, p := range cfg.AnthropicProviders {
				if p.Model == cfg.Model {
					activeProvider = &p
					break
				}
			}
		}
		if activeProvider == nil {
			return providers.AnthropicDefaultModels(), nil
		}
		models, err := providers.GetAnthropicModels(activeProvider.BaseURL, activeProvider.APIKey)
		if err != nil || len(models) == 0 {
			return providers.AnthropicDefaultModels(), nil
		}
		return models, nil
	}

	return []string{cfg.Model}, nil
}

// setActiveProvider updates the current provider, model and active provider name.
func setActiveProvider(provider ProviderType, name string, model string) {
	config.Provider = provider
	config.Model = model
	config.ActiveProviderName = name
}

// SwitchProviderAndModel changes provider and model at runtime
func SwitchProviderAndModel(provider ProviderType, model string, saveToFile bool) error {
	cfg := Get()

	// Validate provider switch
	if provider == ProviderGemini {
		if cfg.GeminiAPIKey == "" {
			return fmt.Errorf("Gemini API key not configured. Run 'fuckyourcli config' first")
		}
		config.Provider = ProviderGemini
		config.Model = model
		// Update Gemini model if different
		if model != cfg.GeminiModel {
			config.GeminiModel = model
		}
	} else if provider == ProviderCodex {
		if cfg.CodexAccessToken == "" {
			return fmt.Errorf("Codex not authenticated. Run 'fuckyourcli config' first")
		}
		config.Provider = ProviderCodex
		config.Model = model
		if model != cfg.CodexModel {
			config.CodexModel = model
		}
	} else if provider == ProviderAnthropic {
		if len(cfg.AnthropicProviders) == 0 {
			return fmt.Errorf("No Anthropic providers configured. Run 'fuckyourcli config' first")
		}
		found := false
		// 1. Try to find by current active name
		if cfg.ActiveProviderName != "" {
			for i, p := range config.AnthropicProviders {
				if p.Name == cfg.ActiveProviderName {
					config.AnthropicProviders[i].Model = model
					setActiveProvider(ProviderAnthropic, p.Name, model)
					found = true
					break
				}
			}
		}
		// 2. Try to find by model match
		if !found {
			for i, p := range config.AnthropicProviders {
				if p.Model == cfg.Model || p.Model == model {
					config.AnthropicProviders[i].Model = model
					setActiveProvider(ProviderAnthropic, p.Name, model)
					found = true
					break
				}
			}
		}
		// 3. Fallback to first provider
		if !found {
			config.AnthropicProviders[0].Model = model
			setActiveProvider(ProviderAnthropic, config.AnthropicProviders[0].Name, model)
		}
	} else if provider == ProviderOpenAICompatible {
		if len(cfg.OpenAICompatibleProviders) == 0 {
			return fmt.Errorf("No OpenAI compatible providers configured")
		}
		found := false
		// 1. Check if a provider already has this model set as default
		for _, p := range config.OpenAICompatibleProviders {
			if p.Model == model {
				setActiveProvider(ProviderOpenAICompatible, p.Name, model)
				found = true
				break
			}
		}
		if !found {
			// 2. Try to apply it to the CURRENTLY active OpenAI provider (search by Name)
			if cfg.ActiveProviderName != "" {
				for i, p := range config.OpenAICompatibleProviders {
					if p.Name == cfg.ActiveProviderName {
						config.OpenAICompatibleProviders[i].Model = model
						setActiveProvider(ProviderOpenAICompatible, p.Name, model)
						found = true
						break
					}
				}
			}
		}
		if !found {
			// 3. Fallback: if not found but there's only one provider, update and use it
			if len(config.OpenAICompatibleProviders) == 1 {
				config.OpenAICompatibleProviders[0].Model = model
				setActiveProvider(ProviderOpenAICompatible, config.OpenAICompatibleProviders[0].Name, model)
			} else {
				return fmt.Errorf("Model '%s' not found and no active OpenAI provider identified to update", model)
			}
		}
	} else if provider == ProviderOpenAIResponsesCompatible {
		if len(cfg.OpenAIResponsesCompatibleProviders) == 0 {
			return fmt.Errorf("No OpenAI Responses providers configured")
		}
		found := false
		// 1. Search by active name
		if cfg.ActiveProviderName != "" {
			for i, p := range config.OpenAIResponsesCompatibleProviders {
				if p.Name == cfg.ActiveProviderName {
					config.OpenAIResponsesCompatibleProviders[i].Model = model
					setActiveProvider(ProviderOpenAIResponsesCompatible, p.Name, model)
					found = true
					break
				}
			}
		}
		// 2. Fallback for old model
		if !found {
			for i, p := range config.OpenAIResponsesCompatibleProviders {
				if p.Model == cfg.Model || p.Model == model {
					config.OpenAIResponsesCompatibleProviders[i].Model = model
					setActiveProvider(ProviderOpenAIResponsesCompatible, p.Name, model)
					found = true
					break
				}
			}
		}
		// 3. Extreme fallback to the first available
		if !found {
			if len(config.OpenAIResponsesCompatibleProviders) == 1 {
				config.OpenAIResponsesCompatibleProviders[0].Model = model
				setActiveProvider(ProviderOpenAIResponsesCompatible, config.OpenAIResponsesCompatibleProviders[0].Name, model)
			} else {
				return fmt.Errorf("Model '%s' not found and no active Responses provider identified to update", model)
			}
		}
	} else {
		return fmt.Errorf("Unknown provider type: %s", provider)
	}

	if saveToFile {
		Save()
	}
	return nil
}
