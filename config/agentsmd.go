package config

// DefaultAgentsMDMaxBytes is the byte budget for the combined AGENTS.md
// instruction chain injected into the system prompt, mirroring the 32 KiB cap
// used by other coding agents. Content beyond the budget is truncated.
const DefaultAgentsMDMaxBytes = 32768

// DefaultAgentsMDFallbackFilenames are the per-directory instruction files
// consulted when no AGENTS.md (or AGENTS.override.md) is present at that level.
// AGENTS.md always wins; these are only a compatibility fallback.
var DefaultAgentsMDFallbackFilenames = []string{"CLAUDE.md", "GEMINI.md"}

// AgentsMDEnabled reports whether the AGENTS.md instruction chain is injected
// into the system prompt at all.
func AgentsMDEnabled() bool {
	return config.AgentsMDEnabled
}

// EffectiveAgentsMDMaxBytes returns the configured byte budget, falling back to
// the built-in default when the value is missing or invalid (0 or negative).
func EffectiveAgentsMDMaxBytes() int {
	if config.AgentsMDMaxBytes <= 0 {
		return DefaultAgentsMDMaxBytes
	}
	return config.AgentsMDMaxBytes
}

// EffectiveAgentsMDFallbackNames returns the configured fallback filenames,
// falling back to the built-in default when none are configured.
func EffectiveAgentsMDFallbackNames() []string {
	if len(config.AgentsMDFallbackFilenames) == 0 {
		return DefaultAgentsMDFallbackFilenames
	}
	return config.AgentsMDFallbackFilenames
}
