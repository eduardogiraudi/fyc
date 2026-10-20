package config

import (
	"encoding/json"
	"fuckyourcli/types"
)

// ProviderType represents the AI provider type
type ProviderType string

const (
	ProviderGemini                    ProviderType = "gemini"
	ProviderOpenAICompatible          ProviderType = "openai_compatible"
	ProviderOpenAIResponsesCompatible ProviderType = "openai_responses_compatible"
	ProviderAnthropic                 ProviderType = "anthropic"
	ProviderCodex                     ProviderType = "codex"
)

// OpenAICompatibleProviderConfig represents a custom OpenAI-compatible provider
type OpenAICompatibleProviderConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model"`
}

// ToolHook is a single user-configured command executed around a tool call.
// The command runs through the platform shell with FYC_* environment variables
// and a JSON payload on stdin (see tools/hooks.go).
type ToolHook struct {
	Enabled bool   `json:"enabled"`
	Command string `json:"command"`
}

// StatusBarHook renders a custom line on the status bar (e.g. remaining
// credits). The command runs through the platform shell; the first non-empty
// line of stdout is shown. A failing command is silent: the last useful value
// is kept (see tools/status_hook.go).
type StatusBarHook struct {
	Enabled bool   `json:"enabled"`
	Command string `json:"command"`
	// IntervalSeconds refreshes the line in the background; 0 disables the
	// periodic refresh (RefreshOnTurn alone still updates it once per turn).
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	// RefreshOnTurn re-runs the command after every assistant turn.
	RefreshOnTurn bool `json:"refresh_on_turn"`
	// IncludeSecrets exports the active API key/token to the command through
	// FYC_API_KEY. Off by default: the secret must never leak unless asked.
	IncludeSecrets bool `json:"include_secrets,omitempty"`
}

// HooksConfig groups the pre- and post-tool execution hooks plus the status-bar
// hook, which can be overridden per provider via StatusByProvider.
type HooksConfig struct {
	PreTool  ToolHook `json:"pre_tool"`
	PostTool ToolHook `json:"post_tool"`
	// Status is the status-bar hook applied to every provider without an
	// override. Its Enabled field is the global on/off switch.
	Status StatusBarHook `json:"status_bar,omitempty"`
	// StatusByProvider overrides Status for a given provider. The key is the
	// active provider name (or its type when no distinct name applies).
	StatusByProvider map[string]StatusBarHook `json:"status_by_provider,omitempty"`
}

// LSPConfig customises language-server discovery (see lsp/discovery.go). Every
// resolved source wins over the auto-detected ones, so an explicit user choice
// (a pinned binary, or a directory) always takes precedence over PATH and the
// editor caches.
type LSPConfig struct {
	// SearchPaths are extra directories probed for a server's binary. They are
	// checked before the system PATH.
	SearchPaths []string `json:"search_paths,omitempty"`
	// Binaries pins a server (canonical name, e.g. "gopls") to an explicit
	// executable, overriding every other source.
	Binaries map[string]string `json:"binaries,omitempty"`
	// Disabled lists canonical server names that must never be started.
	Disabled []string `json:"disabled,omitempty"`
}

type ReasoningConfig struct {
	// Mode is one of "default", "off", "adaptive", "effort", "budget".
	Mode string `json:"mode,omitempty"`
	// Effort is the named level used by Mode "effort" (e.g. "low", "high").
	Effort string `json:"effort,omitempty"`
	// Budget is the token budget used by Mode "budget".
	Budget int `json:"budget,omitempty"`
	// ExtraBody is a free-form escape hatch merged into the request verbatim; it
	// covers providers the built-in tables do not know yet.
	ExtraBody map[string]any `json:"extra_body,omitempty"`
}

// ReasoningProfileConfig is the typed placement of the thinking fields on the
// wire, declared per provider. It complements the models.dev catalog: the
// catalog says *what* a model can do, this says *where* the fields go for this
// endpoint. Only the fields set here are sent — an explicit override wins over
// the built-in table and disables the catalog overlay for that provider.
type ReasoningProfileConfig struct {
	TogglePath          string `json:"toggle_path,omitempty"`
	ToggleEnabledValue  any    `json:"toggle_enabled_value,omitempty"`
	ToggleDisabledValue any    `json:"toggle_disabled_value,omitempty"`
	ToggleAdaptiveValue any    `json:"toggle_adaptive_value,omitempty"`
	CannotDisable       bool   `json:"cannot_disable,omitempty"`
	ToggleFromControl   bool   `json:"toggle_from_control,omitempty"`

	EffortPath   string            `json:"effort_path,omitempty"`
	EffortValues []string          `json:"effort_values,omitempty"`
	EffortMap    map[string]string `json:"effort_map,omitempty"`

	BudgetPath       string         `json:"budget_path,omitempty"`
	BudgetMin        int            `json:"budget_min,omitempty"`
	BudgetMax        int            `json:"budget_max,omitempty"`
	BudgetFromEffort map[string]int `json:"budget_from_effort,omitempty"`
	AdaptiveBudget   int            `json:"adaptive_budget,omitempty"`

	ForbidsTemperature bool           `json:"forbids_temperature,omitempty"`
	Always             map[string]any `json:"always,omitempty"`
	AlongsideControl   map[string]any `json:"alongside_control,omitempty"`
}

// ReasoningProviderConfig is the per-provider reasoning override. Every field is
// optional and inherits from the global preference when unset, so a provider
// entry can override just the placement, just the intent, or both.
type ReasoningProviderConfig struct {
	// Intent overrides the global reasoning preference for this provider.
	Intent ReasoningConfig `json:"intent,omitempty"`
	// Profile overrides where the thinking fields go; nil means auto-detection
	// (built-in transport table + models.dev catalog).
	Profile *ReasoningProfileConfig `json:"profile,omitempty"`
}

// Config represents the application configuration
type Config struct {
	// Schema is the on-disk format version of the configuration file. It is
	// managed by Load/Save (see persistence.go) and migrated automatically.
	Schema           int          `json:"schema"`
	Provider         ProviderType `json:"provider"`
	Model            string       `json:"model"`
	AllowFileWrite   bool         `json:"allow_file_write"`
	AskFileWriteEach bool         `json:"ask_file_write_each"`
	CompactBanner    bool         `json:"compact_banner"`
	// AllowComputerUse persists "Allow always" for computer-use tools.
	AllowComputerUse bool    `json:"allow_computer_use"`
	NoAsk            bool    `json:"-"`
	Delay            float64 `json:"delay"`
	// Temperature settings
	Temperature float64 `json:"temperature"`
	// Active provider name (disambiguates when multiple providers share the same model)
	ActiveProviderName string `json:"active_provider_name,omitempty"`
	// Gemini-specific settings
	GeminiAPIKey string `json:"gemini_api_key,omitempty"`
	GeminiModel  string `json:"gemini_model,omitempty"`
	// Anthropic Messages API providers (can have multiple)
	AnthropicProviders []OpenAICompatibleProviderConfig `json:"anthropic_providers,omitempty"`
	// OpenAI Compatible providers (can have multiple)
	OpenAICompatibleProviders []OpenAICompatibleProviderConfig `json:"openai_compatible_providers,omitempty"`
	// OpenAI Responses API Compatible providers (can have multiple)
	OpenAIResponsesCompatibleProviders []OpenAICompatibleProviderConfig `json:"openai_responses_compatible_providers,omitempty"`
	// MCP Servers
	MCPServers []types.MCPServerConfig `json:"mcp_servers,omitempty"`
	// Tool enable/disable settings. Missing entries use the application defaults.
	ToolStates map[string]bool `json:"tool_states,omitempty"`
	// Reasoning / Thinking settings (migrated from the legacy thinking_enabled /
	// reasoning_effort scalars in schema v5).
	Reasoning ReasoningConfig `json:"reasoning,omitempty"`
	// ReasoningByProvider overrides the reasoning settings for specific
	// providers, keyed by the active provider name (or its type). See
	// config/reasoning.go for the merge rules.
	ReasoningByProvider map[string]ReasoningProviderConfig `json:"reasoning_by_provider,omitempty"`
	// Auto-compaction settings. The context size used for the threshold is
	// always estimated locally (see tokens.go); provider token counts are only
	// informational and are never used to trigger compaction.
	CompactionEnabled   bool `json:"compaction_enabled"`
	CompactionThreshold int  `json:"compaction_threshold"`
	// CompactionMidTurn additionally checks completed tool-batch boundaries.
	// It is opt-in and has no effect while CompactionEnabled is false.
	CompactionMidTurn bool `json:"compaction_mid_turn"`
	// CompactionModel selects who writes the summary. A zero value means
	// "current": the model active at compaction time (see
	// config/compaction_model.go).
	CompactionModel CompactionModelConfig `json:"compaction_model,omitempty"`
	// CompactionOnFailure is the failure policy of the compaction request:
	// "fallback_current" (default) or "abort".
	CompactionOnFailure string `json:"compaction_on_failure,omitempty"`
	// AGENTS.md project-instruction settings (see config/agentsmd.go and
	// tools/agentsmd.go). The instruction chain is injected into the system
	// prompt as a <project_context> block.
	AgentsMDEnabled           bool     `json:"agents_md_enabled"`
	AgentsMDMaxBytes          int      `json:"agents_md_max_bytes"`
	AgentsMDFallbackFilenames []string `json:"agents_md_fallback_filenames"`
	// Codex-specific settings (OAuth)
	CodexModel          string `json:"codex_model,omitempty"`
	CodexAccessToken    string `json:"codex_access_token,omitempty"`
	CodexRefreshToken   string `json:"codex_refresh_token,omitempty"`
	CodexTokenExpiresAt int64  `json:"codex_token_expires_at,omitempty"`
	// WaylandRestoreToken persists the XDG-portal RemoteDesktop restore token so
	// later runs do not show the compositor consent dialog again.
	WaylandRestoreToken string `json:"wayland_restore_token,omitempty"`
	// Hooks holds the pre/post tool-execution hooks.
	Hooks HooksConfig `json:"hooks,omitempty"`
	// Git holds the git tool permission model (see config/git.go).
	Git GitConfig `json:"git,omitempty"`
	// LSP holds the language-server discovery overrides (see config/lsp.go).
	LSP LSPConfig `json:"lsp,omitempty"`
	// BrowserPaths lists explicit browser executables (non-canonical installs)
	// probed by the crawler before the built-in locations; web_search is
	// disabled when none of them, nor any built-in one, yields a browser.
	// See crawl/findBrowser.
	BrowserPaths []string `json:"browser_paths,omitempty"`
	// rawSecrets holds the on-disk expression of every secret field captured at
	// load time (see config/secrets.go). It is never persisted: SaveE uses it to
	// keep an untouched ${VAR} placeholder instead of overwriting it with a
	// generated name, and to tell a value changed at runtime from a stored one.
	rawSecrets map[string]string
}

// CurrentSchemaVersion is the configuration file format understood by this
// build. Bump it and add a migration in persistence.go when the on-disk shape
// changes.
const CurrentSchemaVersion = 11

// SchemaVersion implements store.SchemaVersioner so the store package knows
// which migration target to reach when loading config.json.
func (Config) SchemaVersion() int { return CurrentSchemaVersion }

// UnmarshalJSON keeps compact mode enabled when the optional setting is absent.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	decoded := plain{CompactBanner: true}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	return nil
}

// geminiModelInfo holds the fields we care about from the Gemini models list API.
type geminiModelInfo struct {
	Name                       string   `json:"name"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}
