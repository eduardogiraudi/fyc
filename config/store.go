package config

import (
	"strings"

	"fuckyourcli/reasoning"
	"fuckyourcli/types"
)

// DefaultCompactionThreshold is the estimated context size (in tokens) above
// which auto-compaction triggers when no explicit threshold is configured.
const DefaultCompactionThreshold = 32000

var config = defaults()

// defaults returns a freshly initialised configuration with every field at its
// built-in value. It is used at start-up and to recover from an unrecoverable
// config.json (see LoadE).
func defaults() Config {
	return Config{
		Schema:                             CurrentSchemaVersion,
		CompactBanner:                      true,
		Provider:                           ProviderOpenAICompatible,
		Model:                              "",
		AllowFileWrite:                     false,
		AskFileWriteEach:                   true,
		Delay:                              0,
		Temperature:                        0.1,
		GeminiModel:                        "gemini-2.0-flash",
		AnthropicProviders:                 []OpenAICompatibleProviderConfig{},
		OpenAICompatibleProviders:          []OpenAICompatibleProviderConfig{},
		OpenAIResponsesCompatibleProviders: []OpenAICompatibleProviderConfig{},
		MCPServers:                         []types.MCPServerConfig{},
		ToolStates:                         map[string]bool{},
		Reasoning:                          ReasoningConfig{Mode: "default"},
		CodexModel:                         "gpt-5-codex",
		CompactionEnabled:                  false,
		CompactionThreshold:                DefaultCompactionThreshold,
		CompactionMidTurn:                  false,
		CompactionModel:                    CompactionModelConfig{},
		CompactionOnFailure:                string(CompactionFallbackCurrent),
		AgentsMDEnabled:                    true,
		AgentsMDMaxBytes:                   DefaultAgentsMDMaxBytes,
		AgentsMDFallbackFilenames:          append([]string(nil), DefaultAgentsMDFallbackFilenames...),
		Hooks:                              HooksConfig{},
	}
}

// Get returns the current configuration
func Get() Config {
	return config
}

// GetProvider returns the current provider
func GetProvider() ProviderType {
	return config.Provider
}

// SetConfigAllowFileWrite sets the file permission flags
func SetConfigAllowFileWrite(allow, askEach bool) {
	config.AllowFileWrite = allow
	config.AskFileWriteEach = askEach
}

// SetConfigAllowComputerUse sets the persistent permission for computer use
func SetConfigAllowComputerUse(allow bool) {
	config.AllowComputerUse = allow
}

// SetWaylandRestoreToken persists the XDG-portal restore token for computer use.
func SetWaylandRestoreToken(token string) {
	config.WaylandRestoreToken = token
}

// SetNoAsk sets the auto mode (non-persistent)
func SetNoAsk(noAsk bool) {
	config.NoAsk = noAsk
}

// SetDelay sets the delay between requests (in seconds)
func SetDelay(delay float64) {
	config.Delay = delay
}

// SetTemperature sets the temperature for AI responses
func SetTemperature(temp float64) {
	config.Temperature = temp
}

// SetReasoning persists the thinking/reasoning preference.
func SetReasoning(r ReasoningConfig) {
	config.Reasoning = r
}

// SetCompactionEnabled enables/disables context auto-compaction.
func SetCompactionEnabled(enabled bool) {
	config.CompactionEnabled = enabled
}

// SetCompactionThreshold sets the threshold (in estimated tokens) for auto-compaction.
func SetCompactionThreshold(threshold int) {
	config.CompactionThreshold = threshold
}

// SetCompactionMidTurn enables the additional in-turn compaction checks.
func SetCompactionMidTurn(enabled bool) {
	config.CompactionMidTurn = enabled
}

// SetCompactionModel stores the model that writes the compaction summary. A
// zero value means "current" (the model active at compaction time).
func SetCompactionModel(m CompactionModelConfig) {
	config.CompactionModel = m
}

// SetCompactionOnFailure stores the failure policy of the compaction request.
func SetCompactionOnFailure(p CompactionFailurePolicy) {
	config.CompactionOnFailure = string(p)
}

// SetPreToolHook sets the pre-tool execution hook.
func SetPreToolHook(enabled bool, command string) {
	config.Hooks.PreTool = ToolHook{Enabled: enabled, Command: command}
}

// SetPostToolHook sets the post-tool execution hook.
func SetPostToolHook(enabled bool, command string) {
	config.Hooks.PostTool = ToolHook{Enabled: enabled, Command: command}
}

// SetStatusBarHook sets the default status-bar hook.
func SetStatusBarHook(h StatusBarHook) {
	config.Hooks.Status = h
}

// SetStatusBarHookForProvider sets the status-bar hook override for a provider.
// An empty provider is ignored; a zero hook value clears nothing (it just
// stores a disabled override).
func SetStatusBarHookForProvider(provider string, h StatusBarHook) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return
	}
	if config.Hooks.StatusByProvider == nil {
		config.Hooks.StatusByProvider = map[string]StatusBarHook{}
	}
	config.Hooks.StatusByProvider[provider] = h
}

// CurrentProviderKey returns the identifier used to select a per-provider
// status-bar hook: the active provider name when one is set, else the provider
// type.
func CurrentProviderKey() string {
	name := strings.TrimSpace(config.ActiveProviderName)
	switch config.Provider {
	case ProviderOpenAICompatible, ProviderOpenAIResponsesCompatible, ProviderAnthropic:
		if name != "" {
			return name
		}
	}
	if config.Provider == "" {
		return "unknown"
	}
	return string(config.Provider)
}

// ActiveStatusHook resolves the status-bar hook to run: a per-provider override
// (keyed by the active provider name, then by its type) wins over the default.
func ActiveStatusHook() StatusBarHook {
	if config.Hooks.StatusByProvider != nil {
		if h, ok := config.Hooks.StatusByProvider[CurrentProviderKey()]; ok {
			return h
		}
		if h, ok := config.Hooks.StatusByProvider[string(config.Provider)]; ok {
			return h
		}
	}
	return config.Hooks.Status
}

// StatusHookFor returns the hook configured for a provider key ("" = default).
func StatusHookFor(provider string) StatusBarHook {
	if provider == "" {
		return config.Hooks.Status
	}
	if config.Hooks.StatusByProvider != nil {
		return config.Hooks.StatusByProvider[provider]
	}
	return StatusBarHook{}
}

// StatusProviderKeys returns the provider identifiers that can carry a
// status-bar hook: the default marker ("") first, then every configured
// provider name and the provider types. Keys that already hold an override are
// always kept, so a hook for a since-removed provider stays editable.
func StatusProviderKeys() []string {
	keys := []string{""}
	seen := map[string]bool{"": true}
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	cfg := config
	for _, p := range cfg.OpenAICompatibleProviders {
		add(p.Name)
	}
	for _, p := range cfg.OpenAIResponsesCompatibleProviders {
		add(p.Name)
	}
	for _, p := range cfg.AnthropicProviders {
		add(p.Name)
	}
	add(string(ProviderGemini))
	add(string(ProviderCodex))
	for k := range cfg.Hooks.StatusByProvider {
		add(k)
	}
	return keys
}

// EffectiveCompactionThreshold returns the effective threshold, applying the
// default when the configured value is invalid (0 or negative).
func EffectiveCompactionThreshold() int {
	if config.CompactionThreshold <= 0 {
		return DefaultCompactionThreshold
	}
	return config.CompactionThreshold
}

// AddOpenAICompatibleProvider adds a new OpenAI-compatible provider
func AddOpenAICompatibleProvider(name, baseURL, apiKey, model string) {
	provider := OpenAICompatibleProviderConfig{
		Name:    name,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	}
	config.OpenAICompatibleProviders = append(config.OpenAICompatibleProviders, provider)
}

// SetReasoningForProvider stores the reasoning override for a provider key (the
// active provider name, or its type). An empty key is ignored; an override that
// carries nothing is removed, so a cleared entry leaves no trace in config.json.
func SetReasoningForProvider(provider string, rc ReasoningProviderConfig) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return
	}
	if rc.IsZero() {
		ClearReasoningForProvider(provider)
		return
	}
	if config.ReasoningByProvider == nil {
		config.ReasoningByProvider = map[string]ReasoningProviderConfig{}
	}
	config.ReasoningByProvider[provider] = rc
}

// ReasoningForProvider returns the override stored for a provider key.
func ReasoningForProvider(provider string) (ReasoningProviderConfig, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" || config.ReasoningByProvider == nil {
		return ReasoningProviderConfig{}, false
	}
	rc, ok := config.ReasoningByProvider[provider]
	return rc, ok
}

// ClearReasoningForProvider removes the override for a provider key.
func ClearReasoningForProvider(provider string) {
	if config.ReasoningByProvider == nil {
		return
	}
	delete(config.ReasoningByProvider, strings.TrimSpace(provider))
	if len(config.ReasoningByProvider) == 0 {
		config.ReasoningByProvider = nil
	}
}

// ActiveReasoning resolves the reasoning intent and the placement override that
// apply to the active provider. A per-provider entry wins field by field over
// the global preference; without one, the intent is global and the placement is
// auto-detected (built-in table + models.dev catalog).
func ActiveReasoning() (reasoning.Intent, *reasoning.Profile) {
	return ReasoningForKey(CurrentProviderKey())
}

// ReasoningProviderKeys lists the provider identifiers that can carry a
// reasoning override: every configured provider name, the provider types, and
// any key that already holds an override (so a stale entry stays editable).
func ReasoningProviderKeys() []string {
	keys := []string{}
	seen := map[string]bool{}
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	cfg := config
	for _, p := range cfg.OpenAICompatibleProviders {
		add(p.Name)
	}
	for _, p := range cfg.OpenAIResponsesCompatibleProviders {
		add(p.Name)
	}
	for _, p := range cfg.AnthropicProviders {
		add(p.Name)
	}
	add(string(ProviderGemini))
	add(string(ProviderCodex))
	for k := range cfg.ReasoningByProvider {
		add(k)
	}
	return keys
}
