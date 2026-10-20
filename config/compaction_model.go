package config

import (
	"fmt"
	"strings"

	"fuckyourcli/reasoning"
)

// CompactorKind addresses one of the provider lists for the compaction model.
// It mirrors providerKind in ui_provider.go, but is persisted (a name alone is
// ambiguous: the same name can appear in more than one list).
type CompactorKind string

const (
	CompactorCurrent   CompactorKind = ""
	CompactorGemini    CompactorKind = "gemini"
	CompactorCodex     CompactorKind = "codex"
	CompactorOpenAI    CompactorKind = "openai"
	CompactorResponses CompactorKind = "responses"
	CompactorAnthropic CompactorKind = "anthropic"
)

// CompactionModelConfig is the persisted selection of the model that writes the
// compaction summary. A zero value means "current": the model active at
// compaction time, resolved again on every attempt (a runtime switch through
// /models is followed, the model of the first turn is not).
//
// Model is pinned on purpose: switching provider overwrites the Model field of
// the provider entry, so reading it back would silently follow the active
// model. An empty Model means "the entry's configured model" for a non-active
// entry, and is kept only for hand-edited files.
type CompactionModelConfig struct {
	Kind     string `json:"kind,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// CompactionFailurePolicy decides what a failed compactor request does.
type CompactionFailurePolicy string

const (
	// CompactionFallbackCurrent retries once with the model active at that
	// moment. It never writes the configuration.
	CompactionFallbackCurrent CompactionFailurePolicy = "fallback_current"
	// CompactionAbort interrupts compaction and keeps the history untouched.
	CompactionAbort CompactionFailurePolicy = "abort"
)

// CompactorChoice is a validated selection of who compacts: either the live
// active provider (Current) or a pinned provider entry plus a model.
type CompactorChoice struct {
	Current  bool
	Kind     CompactorKind
	Provider string
	Model    string
}

// SameTarget reports whether two choices address the same connection and model.
// The fallback must not repeat a request that just failed for the same reason.
func (c CompactorChoice) SameTarget(o CompactorChoice) bool {
	if c.Kind != o.Kind || c.Model != o.Model {
		return false
	}
	if c.Kind == CompactorGemini || c.Kind == CompactorCodex {
		return true
	}
	return c.Provider == o.Provider
}

// Label renders the choice for logs and messages.
func (c CompactorChoice) Label() string {
	if c.Current {
		return "current (" + c.Model + ")"
	}
	switch c.Kind {
	case CompactorGemini:
		return "Gemini → " + c.Model
	case CompactorCodex:
		return "Codex → " + c.Model
	}
	name := c.Provider
	if name == "" {
		name = string(c.Kind)
	}
	return name + " → " + c.Model
}

// Compactor resolves the configured compactor and validates it against the
// current provider list. Resolution happens on every attempt (never cached): a
// provider deleted or renamed in the meantime is an error the caller handles
// through the failure policy, not a panic and not a stale index.
func (c Config) Compactor() (CompactorChoice, error) {
	sel := c.CompactionModel
	kind := CompactorKind(strings.ToLower(strings.TrimSpace(sel.Kind)))
	if kind == CompactorCurrent {
		return c.ActiveChoice(), nil
	}
	entry, err := c.ProviderEntry(kind, strings.TrimSpace(sel.Provider))
	if err != nil {
		return CompactorChoice{}, err
	}
	model := strings.TrimSpace(sel.Model)
	if model == "" {
		if entry == nil || entry.Model == "" {
			return CompactorChoice{}, fmt.Errorf("compaction model is empty")
		}
		model = entry.Model
	}
	name := ""
	if entry != nil {
		name = entry.Name
	}
	return CompactorChoice{Kind: kind, Provider: name, Model: model}, nil
}

// ActiveChoice describes the provider and model that are active right now. For
// the multi-provider kinds the entry is resolved again here, so Model is the
// model that will actually be sent (the entry's field, not a stale copy).
func (c Config) ActiveChoice() CompactorChoice {
	choice := CompactorChoice{Current: true, Provider: c.ActiveProviderName, Model: c.Model}
	switch c.Provider {
	case ProviderGemini:
		choice.Kind = CompactorGemini
	case ProviderCodex:
		choice.Kind = CompactorCodex
	case ProviderOpenAICompatible:
		choice.Kind = CompactorOpenAI
	case ProviderOpenAIResponsesCompatible:
		choice.Kind = CompactorResponses
	case ProviderAnthropic:
		choice.Kind = CompactorAnthropic
	}
	if choice.Kind == CompactorCurrent {
		return choice
	}
	if choice.Kind == CompactorGemini || choice.Kind == CompactorCodex {
		choice.Provider = ""
		return choice
	}
	if entry := c.activeEntry(choice.Kind); entry != nil {
		choice.Provider, choice.Model = entry.Name, entry.Model
	}
	return choice
}

// activeEntry finds the entry the active selection points at: by name first,
// then by model (the historical fallback used by findActiveProvider).
func (c Config) activeEntry(kind CompactorKind) *OpenAICompatibleProviderConfig {
	var list []OpenAICompatibleProviderConfig
	switch kind {
	case CompactorOpenAI:
		list = c.OpenAICompatibleProviders
	case CompactorResponses:
		list = c.OpenAIResponsesCompatibleProviders
	case CompactorAnthropic:
		list = c.AnthropicProviders
	default:
		return nil
	}
	if c.ActiveProviderName != "" {
		for i := range list {
			if list[i].Name == c.ActiveProviderName {
				return &list[i]
			}
		}
	}
	for i := range list {
		if list[i].Model == c.Model {
			return &list[i]
		}
	}
	return nil
}

// ProviderType maps the kind onto the configuration's provider type, so a
// reasoning override keyed by provider type still applies.
func (k CompactorKind) ProviderType() ProviderType {
	switch k {
	case CompactorGemini:
		return ProviderGemini
	case CompactorCodex:
		return ProviderCodex
	case CompactorOpenAI:
		return ProviderOpenAICompatible
	case CompactorResponses:
		return ProviderOpenAIResponsesCompatible
	case CompactorAnthropic:
		return ProviderAnthropic
	}
	return ""
}

// ReasoningKey is the key used to look up a per-provider reasoning override.
func (c CompactorChoice) ReasoningKey() string {
	if c.Provider != "" {
		return c.Provider
	}
	return string(c.Kind.ProviderType())
}

// ProviderEntry returns the configured provider entry addressed by kind and
// name. gemini and codex are singletons and yield (nil, nil).
func (c Config) ProviderEntry(kind CompactorKind, name string) (*OpenAICompatibleProviderConfig, error) {
	var list []OpenAICompatibleProviderConfig
	switch kind {
	case CompactorGemini, CompactorCodex:
		return nil, nil
	case CompactorOpenAI:
		list = c.OpenAICompatibleProviders
	case CompactorResponses:
		list = c.OpenAIResponsesCompatibleProviders
	case CompactorAnthropic:
		list = c.AnthropicProviders
	default:
		return nil, fmt.Errorf("unknown compaction provider kind %q", string(kind))
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	if name == "" {
		return nil, fmt.Errorf("compaction provider is empty")
	}
	return nil, fmt.Errorf("compaction provider %q is not configured", name)
}

// CompactionFailurePolicy returns the effective failure policy. An absent or
// unrecognised value keeps the non-destructive default.
func (c Config) CompactionFailurePolicy() CompactionFailurePolicy {
	switch CompactionFailurePolicy(strings.ToLower(strings.TrimSpace(c.CompactionOnFailure))) {
	case CompactionAbort:
		return CompactionAbort
	default:
		return CompactionFallbackCurrent
	}
}

// CompactorLabel renders the configured compactor for the UI and the banner.
func (c Config) CompactorLabel() string {
	choice, err := c.Compactor()
	if err != nil {
		return "current (invalid compactor: " + err.Error() + ")"
	}
	if choice.Current {
		if choice.Model == "" {
			return "current"
		}
		return "current (" + choice.Model + ")"
	}
	return choice.Label()
}

// ReasoningForKey resolves the reasoning intent and placement override for a
// provider key (an entry name or a provider type). It keeps a compactor's
// thinking settings independent from the active provider's override.
func ReasoningForKey(key string) (reasoning.Intent, *reasoning.Profile) {
	pc, _ := ReasoningForProvider(key)
	return pc.IntentOr(config.Reasoning).Intent(), pc.Profile.ToProfile()
}
