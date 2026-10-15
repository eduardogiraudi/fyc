package reasoning

import "strings"

// Lookup resolves the Profile for a request. When override is non-nil it is
// returned verbatim (a user-forced profile always wins over detection). host may
// be empty; model is required for the per-family table.
func Lookup(transport Transport, host, model string, override *Profile) (Profile, Detection) {
	if override != nil {
		p := *override
		p.Transport = transport
		if p.Model == "" {
			p.Model = model
		}
		return p, Detection{Source: "override", Confident: true}
	}
	det := Detect(host, model)
	family := det.Family
	// The endpoint, not the model brand, decides which wire fields it accepts.
	if det.Provider == FamilyLocal || det.Provider == FamilyOllama {
		family = det.Provider
	}
	p := builtin(transport, family, model)
	// The catalog only refines the model-intrinsic facts; the overlay is a
	// no-op when the catalog is missing or the model is unknown.
	applyCatalog(&p, host, model)
	return p, det
}

// builtin returns the known Profile for a (transport, family, model) triple.
// An unknown family yields an empty Profile: Translate then only emits Always
// and the warnings derived from the defensive default.
func builtin(transport Transport, family Family, model string) Profile {
	p := Profile{Transport: transport, Model: model}
	switch transport {
	case ChatCompletions:
		return chatCompletionsProfile(family, model)
	case OpenAIResponses:
		return responsesProfile(family, model)
	case AnthropicMessages:
		return anthropicProfile(model)
	case GeminiGenerateContent:
		return geminiProfile(model)
	}
	return p
}

// chatCompletionsProfile is the per-family table for the OpenAI Chat
// Completions wire. This is the widest surface: each vendor places the toggle
// and the effort differently, which is exactly why they are data here.
func chatCompletionsProfile(family Family, model string) Profile {
	p := Profile{Transport: ChatCompletions, Model: model}
	switch family {
	case FamilyDeepSeek:
		// Toggle at top-level thinking.type (the SDK "extra_body" is merged into
		// the body); depth via top-level reasoning_effort low/high/max
		// (Appendix A.4).
		p.TogglePath = "thinking.type"
		p.ToggleEnabledValue = "enabled"
		p.ToggleDisabledValue = "disabled"
		p.EffortPath = "reasoning_effort"
		p.EffortValues = []string{"low", "high", "max"}
		p.EffortMap = map[string]string{
			"minimal": "low", "low": "low", "medium": "high",
			"high": "high", "xhigh": "high", "max": "max", "ultra": "max",
		}

	case FamilyGLM:
		if isGLM53(model) {
			// GLM-5.3: thinking always on, effort low/high/max only.
			p.CannotDisable = true
			p.EffortPath = "reasoning_effort"
			p.EffortValues = []string{"low", "high", "max"}
			p.EffortMap = map[string]string{"minimal": "low", "medium": "high", "xhigh": "max"}
			break
		}
		// GLM-5.2: full label set with an internal remap (Appendix A.5).
		p.EffortPath = "reasoning_effort"
		p.EffortValues = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
		p.EffortMap = map[string]string{
			"none": "stop", "minimal": "stop", "low": "high",
			"medium": "high", "xhigh": "max",
		}

	case FamilyQwen:
		// Toggle nested under chat_template_kwargs; self-hosted servers ignore
		// a top-level enable_thinking (Appendix A.6).
		p.TogglePath = "chat_template_kwargs.enable_thinking"
		p.ToggleEnabledValue = true
		p.ToggleDisabledValue = false

	case FamilyMistral:
		p.EffortPath = "reasoning_effort"
		p.EffortValues = []string{"high", "none"}
		p.EffortMap = map[string]string{
			"minimal": "none", "low": "high", "medium": "high",
			"high": "high", "xhigh": "high", "max": "high", "ultra": "high",
		}

	case FamilyOpenAI:
		// Bare Chat Completions: reasoning_effort is the only exposed control.
		p.EffortPath = "reasoning_effort"
		p.EffortValues = []string{"none", "minimal", "low", "medium", "high"}

	case FamilyKimi, FamilyMiniMax:
		// Documented as a plain reasoning_effort knob (Appendix A.8).
		p.EffortPath = "reasoning_effort"

	case FamilyOllama:
		// Ollama's OpenAI API disables thinking through effort, not Qwen's toggle.
		p.TogglePath = "reasoning_effort"
		p.ToggleEnabledValue = "high"
		p.ToggleDisabledValue = "none"
		p.EffortPath = "reasoning_effort"
		p.EffortValues = []string{"none", "low", "medium", "high"}
		p.EffortMap = map[string]string{
			"minimal": "low", "xhigh": "high", "max": "high", "ultra": "high",
		}

	case FamilyLocal:
		// Local servers (Ollama, vLLM, SGLang, llama.cpp) are tolerated guesses:
		// the model name is the only signal and the host tells us nothing else.
		// Ollama documents reasoning_effort as the control its OpenAI-compatible
		// endpoint understands (a native "think" field is NOT part of that API);
		// the Qwen-style nested toggle stays as the on/off control, which the
		// other local servers accept.
		p.TogglePath = "chat_template_kwargs.enable_thinking"
		p.ToggleEnabledValue = true
		p.ToggleDisabledValue = false
		p.EffortPath = "reasoning_effort"
		p.EffortValues = []string{"none", "low", "medium", "high"}
		p.EffortMap = map[string]string{
			"minimal": "low", "xhigh": "high", "max": "high", "ultra": "high",
		}
	}
	return p
}

func responsesProfile(family Family, model string) Profile {
	p := Profile{Transport: OpenAIResponses, Model: model}
	switch family {
	case FamilyOpenAI:
		p.EffortPath = "reasoning.effort"
		p.EffortValues = []string{"none", "minimal", "low", "medium", "high"}
		p.AlongsideControl = map[string]any{"reasoning.summary": "auto"}
	case FamilyDeepSeek:
		// DeepSeek Responses: effort none/low/high/max, reasoning in plaintext
		// (Appendix A.14). There is no summary field.
		p.EffortPath = "reasoning.effort"
		p.EffortValues = []string{"none", "low", "high", "max"}
		p.EffortMap = map[string]string{
			"minimal": "low", "medium": "high", "xhigh": "high", "ultra": "max",
		}
	case FamilyLocal, FamilyOllama:
		// Ollama's /v1/responses (v0.13.3+) accepts reasoning.effort plus its own
		// "think" extension (boolean or a model-defined level); other local
		// servers ignore a field they do not know.
		p.TogglePath = "think"
		p.ToggleEnabledValue = true
		p.ToggleDisabledValue = false
		p.EffortPath = "reasoning.effort"
		p.EffortValues = []string{"none", "low", "medium", "high"}
		p.EffortMap = map[string]string{
			"minimal": "low", "xhigh": "high", "max": "high", "ultra": "high",
		}
	}
	return p
}

// anthropicProfile maps the three documented phases of the Messages API
// (Appendix A.1) onto one Profile. Phase selection is by model name.
func anthropicProfile(model string) Profile {
	p := Profile{
		Transport:  AnthropicMessages,
		Model:      model,
		TogglePath: "thinking.type",
	}
	switch {
	case isAnthropicAdaptiveOnly(model):
		// Phase 3: budget field removed; disabled is rejected; temperature,
		// top_p and top_k are rejected as well.
		p.ToggleAdaptiveValue = "adaptive"
		p.CannotDisable = true
		p.EffortPath = "output_config.effort"
		p.EffortValues = []string{"low", "medium", "high", "xhigh", "max"}
		p.ForbidsTemperature = true
	case isAnthropicDeprecation(model):
		// Phase 2: budget deprecated but still accepted; adaptive preferred.
		p.ToggleAdaptiveValue = "adaptive"
		p.ToggleEnabledValue = "enabled"
		p.ToggleDisabledValue = "disabled"
		p.BudgetPath = "thinking.budget_tokens"
		p.BudgetMin = 1024
		p.BudgetFromEffort = anthropicBudgetByEffort
		p.EffortPath = "output_config.effort"
		p.EffortValues = []string{"low", "medium", "high", "xhigh", "max"}
		p.ToggleFromControl = true
		p.ForbidsTemperatureWithThinking = true
	default:
		// Phase 1: extended thinking, budget_tokens only.
		p.ToggleEnabledValue = "enabled"
		p.ToggleDisabledValue = "disabled"
		p.BudgetPath = "thinking.budget_tokens"
		p.BudgetMin = 1024
		p.BudgetFromEffort = anthropicBudgetByEffort
		p.ToggleFromControl = true
		p.ForbidsTemperatureWithThinking = true
	}
	return p
}

// anthropicBudgetByEffort is the conventional client-side effort -> budget map
// (the API itself has no effort field in phase 1).
var anthropicBudgetByEffort = map[string]int{
	"minimal": 2048, "low": 4096, "medium": 10000,
	"high": 16000, "xhigh": 32000, "max": 32000,
}

func geminiProfile(model string) Profile {
	base := Profile{
		Transport: GeminiGenerateContent,
		Model:     model,
		Always:    map[string]any{"generationConfig.thinkingConfig.includeThoughts": true},
	}
	if isGemini3(model) {
		// Gemini 3: thinkingLevel only; thinkingBudget is rejected.
		base.EffortPath = "generationConfig.thinkingConfig.thinkingLevel"
		base.EffortValues = []string{"MINIMAL", "LOW", "MEDIUM", "HIGH"}
		base.EffortMap = map[string]string{
			"minimal": "MINIMAL", "low": "LOW", "medium": "MEDIUM",
			"high": "HIGH", "xhigh": "HIGH", "max": "HIGH", "none": "MINIMAL",
		}
		return base
	}
	// Gemini 2.5: thinkingBudget only; thinkingLevel is rejected. -1 enables
	// dynamic thinking, 0 disables it (Flash only).
	base.BudgetPath = "generationConfig.thinkingConfig.thinkingBudget"
	base.BudgetMin = 0
	base.BudgetMax = 32768
	base.AdaptiveBudget = -1
	base.BudgetFromEffort = map[string]int{
		"none": 0, "minimal": 0, "low": 2048, "medium": 8192,
		"high": 16384, "xhigh": 24576, "max": 32768,
	}
	return base
}

func isGLM53(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "glm-5.3") || strings.Contains(m, "glm-5-3")
}

func isGemini3(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gemini-3")
}

// isAnthropicAdaptiveOnly matches phase-3 models: Opus 4.7+, Sonnet 5+, Fable
// and Mythos, where budget_tokens is removed and thinking may be mandatory.
func isAnthropicAdaptiveOnly(model string) bool {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "fable"), strings.Contains(m, "mythos"),
		strings.Contains(m, "opus-4-7"), strings.Contains(m, "sonnet-5"),
		strings.Contains(m, "opus-5"):
		return true
	}
	return false
}

// isAnthropicDeprecation matches phase-2 models: 4.6 generations where the
// budget field is deprecated but still accepted.
func isAnthropicDeprecation(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "-4-6")
}
