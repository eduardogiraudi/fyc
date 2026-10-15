// Package main — shared state struct, tool definitions, and init helpers.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/lsp"
	"fuckyourcli/mcp"
	"fuckyourcli/modes"
	"fuckyourcli/store"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"os"
	"path/filepath"
	"strings"
)

var version string

// ChatSession holds all mutable session state.
// This is the single object that will become Bubble Tea's Model.
type ChatSession struct {
	MCPManager   *mcp.MCPManager
	GlobalIgnore *tools.IgnoreManager
	Version      string
	Commit       string

	ChatHistory []types.Message
	CurrentMode *modes.Mode
	// turnModeOverride holds a one-shot prefix mode (e.g. "/debug ...") for the
	// duration of a single turn. It is cleared once the turn ends, so the tool
	// policy of a one-shot mode never leaks into the following turns.
	turnModeOverride *modes.Mode
	// persistentModeInjected is the name of the persistent /mode whose
	// instructions have already been appended to the chat history. It is
	// cleared when the persistent mode is (re)selected, so the (potentially
	// large) instructions block is injected once instead of on every turn —
	// re-injecting it each turn inflated the context for no benefit. The mode's
	// tool policy keeps applying regardless (see getToolsForAI).
	persistentModeInjected string
	RestrictedRoot         string
	// ProjectContext is the rendered <project_context> block (the AGENTS.md
	// instruction chain) injected into the system prompt. Empty when there is
	// no instruction file or the feature is disabled.
	ProjectContext string

	LastCtx int

	// Auto-compaction runtime guard state (not persisted). lastAutoCompactSize
	// is the estimated context size right after the last auto-compaction;
	// autoCompactSuppressed latches on failures or insufficient headroom, so
	// automatic checks do not retry at every tool-batch boundary.
	lastAutoCompactSize   int
	autoCompactSuppressed bool

	IsGenerating       atomic.Int32
	GenerationCancel   context.CancelFunc
	GenerationCancelMu sync.Mutex

	SubagentToolsSessionEnabled *bool
	WebSearchSessionOverride    *bool
	// WebSearchAutoDisabled hides web_search at runtime when no browser was
	// detected among the configured and built-in paths (mirrors LSP/git).
	WebSearchAutoDisabled atomic.Bool
	LSPToolsAutoDisabled  atomic.Bool
	// GitToolsAutoDisabled hides every git tool at runtime when the git binary
	// is not usable at all (mirrors LSP).
	GitToolsAutoDisabled atomic.Bool
	// gitInRepo caches whether the workspace is inside a repository, so the
	// per-tool visibility can keep git_init/git_clone available for bootstrapping.
	gitInRepo atomic.Bool
	Sink      types.OutputSink
	// RunCtx is the context of the active run. The AG-UI server sets it so that
	// tool interactions (level-1 human-in-the-loop) can reach the run's own SSE
	// stream and be canceled when the client disconnects. nil in the TUI.
	RunCtx context.Context
	// turnCtx is the context of the turn currently in flight (the provider call
	// plus the tools it runs). It is derived from RunCtx by newGenerationContext
	// and cleared by finishGeneration; runContext prefers it, so cancelling the
	// turn (ctrl+c in the TUI, client disconnect in the server) also aborts a
	// tool that is running. Guarded by GenerationCancelMu.
	turnCtx context.Context

	AvailableTools []types.Tool
	// Headless marks a session running behind the AG-UI server: tools that
	// require a real TTY (questionary) are hidden from the model.
	Headless bool

	// Session persistence metadata, managed by sessionstore.go. They identify
	// the on-disk chat session this process is reading from / writing to.
	SessionID        string
	SessionCreatedAt time.Time
	SessionTitle     string
}

// runContext returns the context of the active turn or run. A turn in flight
// (see turnCtx) wins: it is a child of RunCtx and is cancelled by ctrl+c / turn
// abort, so tools parented to it stop with the turn. Without a turn it falls
// back to the run (AG-UI) or to a background context for the interactive CLI.
func (s *ChatSession) runContext() context.Context {
	if s == nil {
		return context.Background()
	}
	s.GenerationCancelMu.Lock()
	ctx := s.turnCtx
	s.GenerationCancelMu.Unlock()
	if ctx != nil {
		return ctx
	}
	if s.RunCtx != nil {
		return s.RunCtx
	}
	return context.Background()
}

func loadUserPreferences() string {
	path := filepath.Join(store.ConfigDir(), "PREFERENCES.txt")

	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

func (s *ChatSession) initRestrictedRoot() {
	var err error
	s.RestrictedRoot, err = os.Getwd()
	if err != nil {
		s.RestrictedRoot = "."
	}
}

func computeShortSHA() string {
	hash, err := computeBinarySHA()
	if err != nil {
		return "unknown"
	}
	return hash[:16]
}

func computeBinarySHA() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	return computeFileSHA(exe)
}

func computeFileSHA(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open binary: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("read binary: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// defaultDisabledTools lists tools that start disabled when there is no saved state.
var defaultDisabledTools = map[string]bool{
	"spawn_subagents": true,
	"computer":        true,
	"read_media":      true,
}

func defaultToolEnabled(name string) bool {
	if defaultDisabledTools[name] {
		return false
	}
	// Git tools: only the read-only ones are enabled by default. The
	// authoritative lists live in config/git.go.
	if config.IsGitTool(name) {
		return config.GitToolEnabledByDefault(name)
	}
	return true
}

func (s *ChatSession) isToolEnabled(name string) bool {
	if name == "spawn_subagents" && s.SubagentToolsSessionEnabled != nil {
		return *s.SubagentToolsSessionEnabled
	}
	states := config.GetToolStates()
	if enabled, ok := states[name]; ok {
		return enabled
	}
	return defaultToolEnabled(name)
}

func (s *ChatSession) setToolEnabled(name string, enabled bool) {
	if name == "spawn_subagents" {
		s.SubagentToolsSessionEnabled = &enabled
	} else if name == "web_search" {
		s.WebSearchSessionOverride = &enabled
	}
}

var lspToolNames = map[string]bool{
	"lsp_active_servers":    true,
	"lsp_hover":             true,
	"lsp_definition":        true,
	"lsp_references":        true,
	"lsp_diagnostics":       true,
	"lsp_workspace_symbols": true,
}

func isLSPTool(name string) bool {
	return lspToolNames[name]
}

func allLSPToolsUserDisabled() bool {
	states := config.GetToolStates()
	for name := range lspToolNames {
		if enabled, ok := states[name]; !ok || enabled {
			return false
		}
	}
	return true
}

func (s *ChatSession) relevantLSPServersAvailable() bool {
	ignored := func(absPath string) bool {
		return s.GlobalIgnore != nil && s.GlobalIgnore.IsIgnored(absPath, s.RestrictedRoot)
	}
	presentExts := lsp.ScanExtensions(s.RestrictedRoot, ignored)
	return len(lsp.FilterRelevant(lsp.Discover(tools.LSPOptions()), presentExts)) > 0
}

func (s *ChatSession) refreshLSPToolAvailability() bool {
	old := s.LSPToolsAutoDisabled.Load()
	if allLSPToolsUserDisabled() {
		s.LSPToolsAutoDisabled.Store(false)
		return old != false
	}
	s.LSPToolsAutoDisabled.Store(!s.relevantLSPServersAvailable())
	return old != s.LSPToolsAutoDisabled.Load()
}

// refreshWebSearchAvailability re-detects the browser executable and reports
// whether web_search's availability changed. It mirrors the LSP/git runtime
// layer: web_search is hidden while no browser is found among the configured
// browser_paths and the built-in locations.
func (s *ChatSession) refreshWebSearchAvailability() bool {
	old := s.WebSearchAutoDisabled.Load()
	s.WebSearchAutoDisabled.Store(tools.WebSearchBlockReason() != "")
	return old != s.WebSearchAutoDisabled.Load()
}

// allGitToolsUserDisabled reports whether every git tool has been explicitly
// disabled in the tool settings (in which case the runtime layer stays out of
// the way).
func allGitToolsUserDisabled() bool {
	states := config.GetToolStates()
	for _, name := range config.GitToolNames() {
		if enabled, ok := states[name]; !ok || enabled {
			return false
		}
	}
	return true
}

// refreshGitToolAvailability re-detects git and the repository and reports
// whether anything changed (callers then rebuild the system prompt). It mirrors
// refreshLSPToolAvailability:
//
//   - every git tool hidden by the user → nothing to do;
//   - git missing → all git tools are hidden;
//   - no repository → only the bootstrap tools (git_init, git_clone) stay
//     visible, so the model can still create a repository.
func (s *ChatSession) refreshGitToolAvailability() bool {
	oldDisabled := s.GitToolsAutoDisabled.Load()
	oldInRepo := s.gitInRepo.Load()

	available := tools.GitAvailable()
	inRepo := false
	if available {
		_, inRepo = tools.RefreshGitRepoRoot(s.RestrictedRoot)
	}
	if allGitToolsUserDisabled() {
		s.GitToolsAutoDisabled.Store(false)
	} else {
		s.GitToolsAutoDisabled.Store(!available)
	}
	s.gitInRepo.Store(inRepo)

	return oldDisabled != s.GitToolsAutoDisabled.Load() || oldInRepo != inRepo
}

// gitToolHidden reports whether a single git tool is currently hidden from the
// model (used by getToolsForAI).
func (s *ChatSession) gitToolHidden(name string) bool {
	if s.GitToolsAutoDisabled.Load() {
		return true
	}
	if s.gitInRepo.Load() {
		return false
	}
	return !tools.GitToolUsableOutsideRepo(name)
}

// activeGitTools returns the git tool names currently exposed to the model
// (enabled by the user and not hidden by the runtime layer), in registry order.
func (s *ChatSession) activeGitTools() []string {
	var out []string
	for _, name := range config.GitToolNames() {
		if s.toolVisible(name) && !s.gitToolHidden(name) {
			out = append(out, name)
		}
	}
	return out
}

// gitToolsVisible reports whether any git tool is currently exposed to the model.
func (s *ChatSession) gitToolsVisible() bool {
	return len(s.activeGitTools()) > 0
}

// activeMode returns the mode whose tool policy applies to the current turn: a
// one-shot prefix mode wins over the persistent /mode selection. nil means
// plain chat (no mode policy).
func (s *ChatSession) activeMode() *modes.Mode {
	if s.turnModeOverride != nil {
		return s.turnModeOverride
	}
	return s.CurrentMode
}

// claimModeInstructions reports whether the mode's instructions must be
// appended to the chat history for this turn and, for a persistent mode,
// records the injection so it happens only once. A one-shot prefix mode injects
// on every turn it is used; a persistent /mode injects on its first turn only
// (the flag is reset whenever the persistent mode is (re)selected). Providers
// that carry the mode in a system instruction bypass this entirely.
func (s *ChatSession) claimModeInstructions(m *modes.Mode, oneShot bool) bool {
	if oneShot {
		return true
	}
	if s.persistentModeInjected == m.Name {
		return false
	}
	s.persistentModeInjected = m.Name
	return true
}

// toolVisible reports whether the named tool is exposed to the model: it must
// be enabled by the user AND allowed by the active mode. Modes only ever narrow
// the set, so a tool the user disabled is never re-enabled by a mode.
func (s *ChatSession) toolVisible(name string) bool {
	if !s.isToolEnabled(name) {
		return false
	}
	return s.activeMode().ExposesTool(name)
}

func (s *ChatSession) webSearchEnabled() bool {
	// A missing browser disables web_search regardless of the user preference:
	// the tool could not run anyway (see refreshWebSearchAvailability).
	if s.WebSearchAutoDisabled.Load() {
		return false
	}
	if s.WebSearchSessionOverride != nil {
		return *s.WebSearchSessionOverride
	}
	return s.isToolEnabled("web_search")
}

func (s *ChatSession) subagentsEnabled() bool {
	return s.isToolEnabled("spawn_subagents")
}

func (s *ChatSession) allToolNames() []string {
	names := make([]string, 0, len(s.AvailableTools))
	seen := map[string]bool{}
	for _, t := range s.AvailableTools {
		name := t.Function.Name
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names
}

// getToolsForAI returns the list of tools visible to the AI provider: disabled
// tools are filtered out, then the active mode narrows the set further.
func (s *ChatSession) getToolsForAI() []types.Tool {
	mcpAvailable := s.MCPManager != nil && s.MCPManager.HasSessions()
	filtered := make([]types.Tool, 0, len(s.AvailableTools))
	for _, t := range s.AvailableTools {
		name := t.Function.Name
		if !s.isToolEnabled(name) {
			continue
		}
		// web_search needs a real browser: hide it entirely while none is available.
		if name == "web_search" && s.WebSearchAutoDisabled.Load() {
			continue
		}
		if isLSPTool(name) && s.LSPToolsAutoDisabled.Load() {
			continue
		}
		// Headless server sessions never expose tools that need interactive
		// input from a terminal.
		if s.Headless && name == "questionary" {
			continue
		}
		// Git tools are hidden while git is missing, and only the bootstrap
		// tools (git_init, git_clone) stay visible outside a repository.
		if tools.IsGitTool(name) && s.gitToolHidden(name) {
			continue
		}
		// Computer use requires a provider that can ingest images inside tool
		// results: hide the tool entirely when the active provider cannot.
		if tools.IsComputerTool(name) && tools.ComputerUseBlockReason() != "" {
			continue
		}

		// read_media returns images too: same provider constraint as computer use.
		if tools.IsMediaTool(name) && tools.ToolImageBlockReason() != "" {
			continue
		}
		if !mcpAvailable && (name == "list_mcp_resources" || name == "read_mcp_resource" || name == "list_mcp_resource_templates") {
			continue
		}
		filtered = append(filtered, t)
	}
	return s.activeMode().FilterTools(filtered)
}

var availableTools = []types.Tool{
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "web_search",
			Description: "Multi-purpose tool: 1. If input is a search query, returns search results and links. 2. If input is a full URL (http/https), it crawls and returns the page text and all links found on that page. Long pages are truncated and returned one window at a time: the JSON result carries `page`, `pages`, `total_chars`, `truncated` and `next_page`, and you read the remaining parts by calling the tool again with an incremented `page` (sliding window). Use this to dive deep into web search.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"input":     {Type: "string", Description: "URL or search query string"},
					"engine":    {Type: "string", Description: "Search engine to use (default: startpage, alternatives: duckduckgo,brave)"},
					"page":      {Type: "number", Description: "1-based page of the result to return. Use the `next_page` field of a truncated result to read the rest."},
					"max_chars": {Type: "number", Description: "Maximum characters per page (default 24000, clamped between 2000 and 200000)."},
					"find":      {Type: "string", Description: "Optional. Case-insensitive term to look for in the whole document. Returns the pages where it appears with short snippets, without the page content. Then call again with `page` set to read the right one."},
				},
				Required: []string{"input"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "context_snapshot",
			Description: "Get a one-shot high-level overview of the entire codebase. Returns files, functions, methods, types, variables and constants. Call this tool first if you need to understand project structure before using grep or read or ls. This tool provides only a high-level overview. After using this tool, inspect relevant files with read when deeper understanding is needed. Do not assume behavior or logic without reading the relevant files.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {
						Type:        "string",
						Description: "Root path to analyze (default: current directory)",
					},
				},
				Required: []string{},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "ls",
			Description: "List directory (non recursive)",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {Type: "string", Description: "Path to list (defaults to '.' if omitted)"},
				},
				Required: []string{},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "grep",
			Description: "Fast, sandboxed, parallel recursive grep. Uses ripgrep-like engine with automatic .gitignore/.fycignore support and os.Root sandboxing. Returns matching lines with line numbers.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"pattern":          {Type: "string", Description: "Regex or string to search"},
					"path":             {Type: "string", Description: "File or directory to search in (defaults to '.' if omitted)"},
					"case_insensitive": {Type: "boolean", Description: "If true, makes the search case-insensitive (default: false)"},
					"include_glob":     {Type: "string", Description: "Glob pattern to filter files (e.g. '*.go'). Only files matching this glob are searched."},
					"exclude_glob":     {Type: "string", Description: "Glob pattern to exclude files (matched against the path and the file name, e.g. '*_test.go'). Files matching this glob are skipped."},
					"include_binary":   {Type: "boolean", Description: "If true, also search binary files (default: false). By default files containing NUL bytes are skipped, like ripgrep."},
					"max_file_size":    {Type: "number", Description: "Skip files larger than this many bytes (0 = no limit)"},
					"workers":          {Type: "number", Description: "Number of parallel file workers (0 = auto = NumCPU)"},
				},
				Required: []string{"pattern"},
			},
		},
	},
	//deprecated tool
	/*{
	Type: "function",
	Function: types.ToolFunction{
		Name:        "update_file",
		Description: "Apply a unified diff to a file. The diff must be in standard unified diff format (e.g., @@ -1,5 +1,6 @@ lines with context, -removed, +added lines). Line numbers are search hints; the tool also tries nearby positions (within 10 lines) if the exact position fails. Context lines (space-prefixed) and deletions must match the file content exactly. If a hunk fails, the error specifies the exact line and expected content. Supports multiple hunks; failing hunks are reported individually while successful ones are still applied. Use 'read' first to read current content.",
		Parameters: types.ToolParameters{
			Type: "object",
			Properties: map[string]types.ToolProperty{
				"path": {Type: "string", Description: "Path to the file"},
				"diff": {Type: "string", Description: "Unified diff to apply (e.g., @@ -1,5 +1,6 @@\\n context\\n-old line\\n+new line\\n context)"},
			},
			Required: []string{"path", "diff"},
		},
	},
	},*/
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "read",
			Description: "Read-only file view. Do NOT use if you intend to edit — use get_anchors instead.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {Type: "string"},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "read_media",
			Description: "Read an image file from the workspace (PNG, JPEG, GIF) and attach it to the result so you can see it. Use it to inspect screenshots, mockups, diagrams or UI states. For text, source or config files use read instead.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {Type: "string", Description: "Path to the image file"},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "get_anchors",
			Description: "Required first step before any edit. Reads a file with word-anchor prefixes \u2014 returns each line as 'WORD:hash\xa7content', e.g. 'Agate:a3\xa7package main'. Unchanged lines keep their anchors across get_anchors calls and successful edits. Any external change to the file makes a patch fail without writing; call get_anchors again and reconsider the patch using the current content. /clear invalidates all cached anchors. Always call this before replace_anchors.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {Type: "string"},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name: "replace_anchors",
			Description: `Edit an existing file. CRITICAL: after calling this, the anchors of modified lines are GONE \u2014 use only the new anchors returned in 'new anchors: ...'. Always call get_anchors first to get anchors. The anchor is 'WORD:hash' (the part before the \u00a7 symbol). Never include WORD:hash\u00a7 prefixes in replacement \u2014 plain source code only. Returns newly inserted lines with fresh anchors.
If the file changed on disk, the patch fails without writing. Re-read with get_anchors and reconsider the patch; never bypass a stale-file conflict with write or sh.
Patterns:
Replace single line: start_anchor=end_anchor=same_line, replacement=new_content
Delete range: start_anchor=first, end_anchor=last, replacement=\"\" (one call)
Insert after line: both anchors=that line, include original content in replacement before new content

Never loop calling replace_anchors on the same line \u2014 write the final content in one shot.`,
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path":         {Type: "string"},
					"start_anchor": {Type: "string", Description: "Anchor of the FIRST line to replace (inclusive). Format: WORD:hash only, no §."},
					"end_anchor":   {Type: "string", Description: "Anchor of the LAST line to replace (inclusive). Format: WORD:hash only, no §."},
					"replacement":  {Type: "string", Description: "Plain source code only. These lines replace start_anchor through end_anchor entirely. Never include WORD:hash§ prefixes."},
				},
				Required: []string{"path", "start_anchor", "end_anchor"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "write",
			Description: "New files only. Never use on existing files — use get_anchors + replace_anchors to edit. Overwrites without warning.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path":    {Type: "string"},
					"content": {Type: "string"},
				},
				Required: []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "delete",
			Description: "Permanently delete a file or directory (recursive). Cannot be undone. Always confirm the path with ls or read before calling.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {Type: "string"},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "make_dir",
			Description: "Create a directory and all necessary parent directories (equivalent to 'mkdir -p'). Does not fail if the directory already exists.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path": {Type: "string"},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "sh",
			Description: "Execute a shell command in a tmpfs sandbox. System packages cannot be installed. Use for build, test, lint, or run commands. One-shot commands return as soon as they finish, with output and exit status (default timeout 120 seconds, extendable to 300). For long-running processes (like web servers), set 'persistence': true to keep the process alive after the tool returns. Use 'kill' to terminate persistent processes.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"command":         {Type: "string"},
					"persistence":     {Type: "boolean", Description: "Default false: waits for completion and never returns a background PID. If true, a command still running after 10 seconds is registered and kept alive: read its log with sniff(pid), stop it with kill(pid). A command that exits earlier returns immediately."},
					"timeout_seconds": {Type: "integer", Description: "One-shot timeout in seconds (default 120, range 1-300). On timeout the command is terminated and its collected output returned. Ignored when persistence is true."},
				},
				Required: []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "kill",
			Description: "Stop a persistent process started with 'sh' and persistence:true, by passing its PID.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"pid": {Type: "number", Description: "The PID of the process to stop"},
				},
				Required: []string{"pid"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "sniff",
			Description: "Read the recent output (log tail) of a persistent process started with 'sh' and persistence:true. It never returns the whole log: by default it returns the last 50 lines, hard-capped at 16 KiB. Use last_n (max 500) to widen the tail window, max_bytes (1024-65536) for the byte ceiling, and pattern (regex) to keep only matching lines (prefixed with their line number) from a huge log. The process keeps running; sniff is read-only.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"pid":       {Type: "number", Description: "PID returned when the persistent process was started"},
					"last_n":    {Type: "number", Description: "Number of trailing lines to return (default 50, max 500)"},
					"max_bytes": {Type: "number", Description: "Hard byte ceiling for the returned text (default 16384, range 1024-65536)"},
					"pattern":   {Type: "string", Description: "Optional regex: only matching lines are kept (each prefixed with its line number inside the buffered window), then the tail is taken"},
				},
				Required: []string{"pid"},
			},
		},
	},

	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "list_mcp_resources",
			Description: "List all available static resources from connected MCP servers. Resources are read-only data sources like documentation or logs.",
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: map[string]types.ToolProperty{},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "list_mcp_resource_templates",
			Description: "List all available resource templates from connected MCP servers. Templates allow accessing dynamic data by filling variables in a URI (e.g., mcp://github/{owner}/{repo}/issue/{number}).",
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: map[string]types.ToolProperty{},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "read_mcp_resource",
			Description: "Read the content of a specific MCP resource using its URI. Use list_mcp_resources or list_mcp_resource_templates to find available resources.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"server": {Type: "string", Description: "The name of the MCP server providing the resource"},
					"uri":    {Type: "string", Description: "The URI of the resource to read"},
				},
				Required: []string{"server", "uri"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "skills_discovery",
			Description: "List all specialized skills available to the AI. Skills provide domain-specific knowledge or workflows stored in SKILL.md files. Call this to see what extra powers you have in this project.",
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: map[string]types.ToolProperty{},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "read_skill",
			Description: "Read the full content and instructions of a specific skill. By default reads SKILL.md. Use the optional 'file' parameter to read other files within the skill directory (e.g., 'agents/grader.md', 'references/schemas.md'). Use this after skills_discovery to understand how to perform a specialized task.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"name": {Type: "string", Description: "The name of the skill to read"},
					"file": {Type: "string", Description: "Optional: file path relative to the skill directory (default: 'SKILL.md'). E.g.,'references/schemas.md'.Path relative to the skill directory (default: 'SKILL.md'). Read any file referenced in SKILL.md using this parameter"},
				},
				Required: []string{"name"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "read_user_preferences",
			Description: "Read global user preferences. Use this when you need to update the user preferences.",
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: map[string]types.ToolProperty{},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "update_user_preferences",
			Description: "Update global user preferences. If preferences file does NOT exist yet, pass old_str=\"\" and new_str will become the initial content (the file will be created). If preferences already exist, pass old_str matching the content to replace and new_str as the replacement.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"old_str": {
						Type:        "string",
						Description: "Exact string to replace in preferences file. If preferences file does NOT exist yet, pass an empty string \"\" (it will be ignored and new_str becomes the initial content). If preferences already exist, this is required and must match existing content exactly.",
					},
					"new_str": {
						Type:        "string",
						Description: "New string that will replace old_str or become the initial content if file does not exist.",
					},
				},
				Required: []string{"new_str"},
			},
		}},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "questionary",
			Description: "Ask the user up to 10 questions in a single round. Each question independently supports multiple-choice (provide options array) or free-text input (set allow_input=true, omit options). You can mix both types in the same call. Returns a JSON array of answers.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"questions": {
						Type:        "array",
						Description: "Array of question objects. Each object supports multiple-choice or free-text input.",
						Items: &types.ToolProperty{
							Type:        "object",
							Description: "A question object",
							Properties: map[string]types.ToolProperty{
								"question":    {Type: "string", Description: "The question text to display to the user"},
								"options":     {Type: "array", Description: "Multiple choice options (strings)", Items: &types.ToolProperty{Type: "string"}},
								"allow_input": {Type: "boolean", Description: "If true, allows free-text input. If false, restricts to options only"},
							},
						},
					},
				},
				Required: []string{"questions"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "copy",
			Description: "Copy a file or directory from source to destination within the project. Supports recursive directory copy. Checks ignore rules.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"source":      {Type: "string", Description: "Source path to copy from"},
					"destination": {Type: "string", Description: "Destination path to copy to"},
				},
				Required: []string{"source", "destination"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "rename",
			Description: "Rename a file or directory. The new_name is just the basename — the item stays in the same parent directory.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"path":     {Type: "string", Description: "Current path of the file or directory to rename"},
					"new_name": {Type: "string", Description: "New name (basename only, same parent directory)"},
				},
				Required: []string{"path", "new_name"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "move",
			Description: "Move a file or directory from source to destination. Works across directories within the project.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"source":      {Type: "string", Description: "Source path to move from"},
					"destination": {Type: "string", Description: "Destination path to move to"},
				},
				Required: []string{"source", "destination"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "lsp_active_servers",
			Description: "Lazily start relevant language servers for the workspace and list active LSP servers.",
			Parameters:  types.ToolParameters{Type: "object", Properties: map[string]types.ToolProperty{}},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "lsp_hover",
			Description: "Return LSP hover documentation/type info for a symbol position. Lines and characters are 0-based.",
			Parameters: types.ToolParameters{Type: "object", Properties: map[string]types.ToolProperty{
				"file":      {Type: "string", Description: "Source file path"},
				"line":      {Type: "number", Description: "0-based line"},
				"character": {Type: "number", Description: "0-based character"},
			}, Required: []string{"file", "line", "character"}},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "lsp_definition",
			Description: "Return LSP definition locations for a symbol position. Lines and characters are 0-based.",
			Parameters: types.ToolParameters{Type: "object", Properties: map[string]types.ToolProperty{
				"file":      {Type: "string", Description: "Source file path"},
				"line":      {Type: "number", Description: "0-based line"},
				"character": {Type: "number", Description: "0-based character"},
			}, Required: []string{"file", "line", "character"}},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "lsp_references",
			Description: "Return LSP references for a symbol position. Lines and characters are 0-based.",
			Parameters: types.ToolParameters{Type: "object", Properties: map[string]types.ToolProperty{
				"file":      {Type: "string", Description: "Source file path"},
				"line":      {Type: "number", Description: "0-based line"},
				"character": {Type: "number", Description: "0-based character"},
			}, Required: []string{"file", "line", "character"}},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "lsp_diagnostics",
			Description: "Return LSP diagnostics for a file.",
			Parameters: types.ToolParameters{Type: "object", Properties: map[string]types.ToolProperty{
				"file": {Type: "string", Description: "Source file path"},
			}, Required: []string{"file"}},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "lsp_workspace_symbols",
			Description: "Search workspace symbols through a named active LSP. Use lsp_active_servers first to get server names.",
			Parameters: types.ToolParameters{Type: "object", Properties: map[string]types.ToolProperty{
				"lsp_name": {Type: "string", Description: "LSP server name from lsp_active_servers"},
				"query":    {Type: "string", Description: "Symbol query"},
			}, Required: []string{"lsp_name", "query"}},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name: "computer",
			Description: `Control the user's real desktop. Actions: "screenshot" (capture the screen — the image is attached to the tool result), "move", "click", "double_click", "right_click", "drag", "scroll" (mouse), "type" (write text), "key" (press a key combo like "ctrl+shift+t", or a lone modifier like "super").
Rules: always take a "screenshot" first; all coordinates (x, y, x2, y2) must be expressed in the LAST screenshot's pixel space — they are mapped to real screen coordinates automatically. After every action, take a new screenshot to verify the result before acting again. Works on the user's actual screen: clicks and keystrokes affect whatever window is focused.`,
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"action":    {Type: "string", Description: "One of: screenshot, move, click, double_click, right_click, drag, scroll, type, key"},
					"x":         {Type: "number", Description: "X coordinate in the last screenshot's space (move/click/drag/scroll)"},
					"y":         {Type: "number", Description: "Y coordinate in the last screenshot's space (move/click/drag/scroll)"},
					"x2":        {Type: "number", Description: "Drag end X coordinate (screenshot space)"},
					"y2":        {Type: "number", Description: "Drag end Y coordinate (screenshot space)"},
					"button":    {Type: "string", Description: "Mouse button: left (default), right, middle"},
					"direction": {Type: "string", Description: "Scroll direction: up, down, left, right"},
					"amount":    {Type: "number", Description: "Scroll amount in wheel steps (default: 3)"},
					"text":      {Type: "string", Description: "Text to type (action='type')"},
					"keys":      {Type: "string", Description: "Key or combo to press (action='key'), e.g. \"enter\", \"ctrl+c\", \"alt+f4\"; a lone modifier like \"super\" taps it alone (separator '+')"},
				},
				Required: []string{"action"},
			},
		},
	},
	{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "spawn_subagents",
			Description: "Spawn one or more sub-agents concurrently. Pass an array with a single element for one sub-agent, or multiple elements for parallel execution. Each sub-agent runs independently and returns an aggregated report.",
			Parameters: types.ToolParameters{
				Type: "object",
				Properties: map[string]types.ToolProperty{
					"agents": {
						Type:        "array",
						Description: "Array of agent config objects. Pass one element for a single agent, multiple for parallel execution.",
						Items: &types.ToolProperty{
							Type:        "object",
							Description: "Configuration for a single sub-agent",
							Properties: map[string]types.ToolProperty{
								"id":             {Type: "string", Description: "Unique identifier for this sub-agent"},
								"subdirectory":   {Type: "string", Description: "Working directory path for this agent, relative to the current root. The directory must exist."},
								"instructions":   {Type: "string", Description: "Task instructions for this agent"},
								"tools":          {Type: "array", Description: "List of tool names this agent can use", Items: &types.ToolProperty{Type: "string"}},
								"max_iterations": {Type: "number", Description: "Max iterations before the agent stops. Default: 10"},
							},
							Required: []string{"id", "subdirectory", "instructions"},
						},
					},
				},
				Required: []string{"agents"},
			},
		},
	},
	// ── git tools ────────────────────────────────────────────────────────────
	// The read-only ones are enabled by default; every tool that can mutate
	// the repository starts disabled (config/git.go) and each mutating call is
	// additionally gated by a permission prompt. All git tools are hidden when
	// git is unavailable or the workspace is not inside a repository, and they
	// are never exposed to sub-agents (tools/subagents_policy.go).
	gitTool("git_status",
		"Show the repository status (porcelain v2: branch info + per-file XY state). Read-only. With a path, limit the output to a path inside the workspace.",
		map[string]types.ToolProperty{
			"path": {Type: "string", Description: "Optional path (inside the workspace) to limit the status to"},
		}),
	gitTool("git_diff",
		"Show changes as a unified diff. Read-only. Use staged=true for the index vs HEAD, or rev=<rev|range> to diff against a revision (mutually exclusive). Optionally restrict to paths inside the workspace.",
		map[string]types.ToolProperty{
			"path":          {Type: "string", Description: "Single path to diff (inside the workspace)"},
			"paths":         {Type: "array", Description: "List of paths to diff", Items: &types.ToolProperty{Type: "string"}},
			"staged":        {Type: "boolean", Description: "Diff the index (staged changes) instead of the working tree"},
			"rev":           {Type: "string", Description: "Revision or range to diff against, e.g. HEAD~2 or main..HEAD"},
			"stat_only":     {Type: "boolean", Description: "Return only the diffstat"},
			"name_only":     {Type: "boolean", Description: "Return only the changed file names"},
			"context_lines": {Type: "number", Description: "Number of context lines (-U<n>, default git's 3)"},
		}),
	gitTool("git_log",
		"Show the commit history. Read-only. Filters: author, grep (message), since/until (date expressions).",
		map[string]types.ToolProperty{
			"count":   {Type: "number", Description: "Number of commits (default 20, max 2000)"},
			"oneline": {Type: "boolean", Description: "Compact one-line format (default true)"},
			"stat":    {Type: "boolean", Description: "Include a diffstat per commit"},
			"author":  {Type: "string", Description: "Only commits by this author (name or email pattern)"},
			"grep":    {Type: "string", Description: "Only commits whose message matches this pattern"},
			"since":   {Type: "string", Description: "Only commits after this date, e.g. '2 weeks ago' or 2026-01-01"},
			"until":   {Type: "string", Description: "Only commits before this date"},
			"path":    {Type: "string", Description: "Limit history to a path inside the workspace"},
			"paths":   {Type: "array", Description: "Limit history to these paths", Items: &types.ToolProperty{Type: "string"}},
		}),
	gitTool("git_show",
		"Show a commit, tag or tree object (metadata + patch). Read-only. Defaults to HEAD.",
		map[string]types.ToolProperty{
			"rev":       {Type: "string", Description: "Revision to show (default HEAD)"},
			"stat_only": {Type: "boolean", Description: "Show only the diffstat"},
			"path":      {Type: "string", Description: "Limit the output to a path inside the workspace"},
			"paths":     {Type: "array", Description: "Limit the output to these paths", Items: &types.ToolProperty{Type: "string"}},
		}),
	gitTool("git_blame",
		"Show line-by-line authorship for a file (read-only). Use start/end for a line range and max_lines to cap the output.",
		map[string]types.ToolProperty{
			"path":      {Type: "string", Description: "File path (required, inside the workspace)"},
			"start":     {Type: "number", Description: "First line of the range (1-based)"},
			"end":       {Type: "number", Description: "Last line of the range (1-based, default = start)"},
			"max_lines": {Type: "number", Description: "Maximum number of lines to return (default 200)"},
		}, "path"),
	gitTool("git_ls_files",
		"List files tracked by git (read-only). Flags: cached (default), others (untracked), ignored, modified, deleted.",
		map[string]types.ToolProperty{
			"cached":   {Type: "boolean", Description: "Files in the index (default)"},
			"others":   {Type: "boolean", Description: "Untracked files (uses --exclude-standard)"},
			"ignored":  {Type: "boolean", Description: "Ignored files (uses --exclude-standard)"},
			"modified": {Type: "boolean", Description: "Modified tracked files"},
			"deleted":  {Type: "boolean", Description: "Deleted tracked files"},
			"path":     {Type: "string", Description: "Limit to a path inside the workspace"},
			"paths":    {Type: "array", Description: "Limit to these paths", Items: &types.ToolProperty{Type: "string"}},
		}),
	gitTool("git_config",
		"Read git configuration (read-only). With key, return a single value; otherwise list the scope. Default scope is the repository (local); pass scope=global/effective explicitly to read the user configuration.",
		map[string]types.ToolProperty{
			"key":   {Type: "string", Description: "Configuration key to read, e.g. user.email or branch.main.remote"},
			"scope": {Type: "string", Description: "local (default), global, system, worktree or effective"},
		}),
	gitTool("git_add",
		"Stage changes (git add). Disabled by default and requires the local-write permission. Pass paths, or all=true to stage every change in the repository.",
		map[string]types.ToolProperty{
			"path":  {Type: "string", Description: "File or directory to stage"},
			"paths": {Type: "array", Description: "Files/directories to stage", Items: &types.ToolProperty{Type: "string"}},
			"all":   {Type: "boolean", Description: "Stage all changes (git add --all)"},
		}),
	gitTool("git_commit",
		"Create a commit with the given message (passed on stdin, never through a shell). Disabled by default; the prompt shows the staged diffstat plus an option to review the full staged diff.",
		map[string]types.ToolProperty{
			"message": {Type: "string", Description: "Commit message (required)"},
			"all":     {Type: "boolean", Description: "Also stage modifications of tracked files (git commit --all)"},
			"amend":   {Type: "boolean", Description: "Amend the previous commit instead of creating a new one"},
		}, "message"),
	gitTool("git_switch",
		"Switch to an existing branch, or create it with create=true. Disabled by default (local-write permission required).",
		map[string]types.ToolProperty{
			"branch": {Type: "string", Description: "Branch name (required)"},
			"create": {Type: "boolean", Description: "Create the branch before switching to it"},
		}, "branch"),
	gitTool("git_restore",
		"Restore files: unstage paths (staged=true) or discard working-tree changes. Disabled by default (local-write permission required); it can discard uncommitted work.",
		map[string]types.ToolProperty{
			"path":   {Type: "string", Description: "File or directory to restore"},
			"paths":  {Type: "array", Description: "Files/directories to restore", Items: &types.ToolProperty{Type: "string"}},
			"staged": {Type: "boolean", Description: "Restore the index (unstage) instead of the working tree"},
			"source": {Type: "string", Description: "Revision to restore from (git restore --source)"},
		}),
	gitTool("git_stash",
		"Manage the stash. action=list (read-only) shows the entries; push/pop/apply/drop require the local-write permission. ref must be stash@{n}.",
		map[string]types.ToolProperty{
			"action":  {Type: "string", Description: "list (default), push, pop, apply or drop"},
			"ref":     {Type: "string", Description: "Stash reference, e.g. stash@{2}"},
			"message": {Type: "string", Description: "Message for stash push"},
			"path":    {Type: "string", Description: "Limit stash push to a path"},
			"paths":   {Type: "array", Description: "Limit stash push to these paths", Items: &types.ToolProperty{Type: "string"}},
		}),
	gitTool("git_init",
		"Initialize a git repository in the workspace (or in a subdirectory). Disabled by default (local-write permission required).",
		map[string]types.ToolProperty{
			"path":           {Type: "string", Description: "Directory to initialize (default: the workspace)"},
			"default_branch": {Type: "string", Description: "Initial branch name, e.g. main"},
		}),
	gitTool("git_branch",
		"Manage branches: action=list (read-only), create, rename or delete. Mutating actions require the local-write permission (force delete requires the destructive one).",
		map[string]types.ToolProperty{
			"action":   {Type: "string", Description: "list (default), create, rename or delete"},
			"name":     {Type: "string", Description: "Branch name (create/rename/delete)"},
			"new_name": {Type: "string", Description: "New name (rename)"},
			"rev":      {Type: "string", Description: "Start point for create (default HEAD)"},
			"force":    {Type: "boolean", Description: "Force deletion (-D) of an unmerged branch"},
		}),
	gitTool("git_remote",
		"Manage remotes: action=list (read-only), add, set-url or remove. Mutating actions require the local-write permission.",
		map[string]types.ToolProperty{
			"action": {Type: "string", Description: "list (default), add, set-url or remove"},
			"name":   {Type: "string", Description: "Remote name, e.g. origin"},
			"url":    {Type: "string", Description: "Remote URL (add/set-url)"},
		}),
	gitTool("git_fetch",
		"Fetch refs from a remote (network permission required; disabled by default).",
		map[string]types.ToolProperty{
			"remote": {Type: "string", Description: "Remote name (default: the configured default remote, origin)"},
			"prune":  {Type: "boolean", Description: "Delete remote-tracking refs that no longer exist on the remote"},
		}),
	gitTool("git_pull",
		"Fast-forward the current branch from a remote (git pull --ff-only). Network permission required; disabled by default.",
		map[string]types.ToolProperty{
			"remote": {Type: "string", Description: "Remote name (default: origin)"},
			"branch": {Type: "string", Description: "Branch to pull (default: the upstream of the current branch)"},
		}),
	gitTool("git_push",
		"Push the current branch to a remote (network permission required; disabled by default). plain --force is never available: force_with_lease also requires git.allow_force_push=true in the configuration.",
		map[string]types.ToolProperty{
			"remote":           {Type: "string", Description: "Remote name (default: origin)"},
			"branch":           {Type: "string", Description: "Branch to push (default: the current branch)"},
			"set_upstream":     {Type: "boolean", Description: "Set the upstream branch (-u)"},
			"force_with_lease": {Type: "boolean", Description: "Use --force-with-lease (requires git.allow_force_push)"},
		}),
	gitTool("git_clone",
		"Clone a repository into the workspace (network permission required; disabled by default). The destination must be inside the workspace and must not be a non-empty directory.",
		map[string]types.ToolProperty{
			"url":         {Type: "string", Description: "Repository URL (required)"},
			"destination": {Type: "string", Description: "Destination directory inside the workspace (default: the workspace itself)"},
		}, "url"),
	gitTool("git_reset",
		"Reset the current branch (soft/mixed/hard). Destructive permission required; disabled by default. mode=hard additionally requires a typed confirmation because it discards working-tree changes.",
		map[string]types.ToolProperty{
			"mode":  {Type: "string", Description: "soft, mixed (default) or hard"},
			"rev":   {Type: "string", Description: "Revision to reset to (default HEAD)"},
			"path":  {Type: "string", Description: "Path to reset (only with mode=mixed)"},
			"paths": {Type: "array", Description: "Paths to reset (only with mode=mixed)", Items: &types.ToolProperty{Type: "string"}},
		}),
	gitTool("git_merge",
		"Merge a branch into the current branch, or abort an in-progress merge (abort=true). Destructive permission required; disabled by default.",
		map[string]types.ToolProperty{
			"branch": {Type: "string", Description: "Branch to merge"},
			"abort":  {Type: "boolean", Description: "Abort the in-progress merge"},
		}),
	gitTool("git_rebase",
		"Rebase the current branch onto upstream, or --continue/--abort an in-progress rebase. Interactive rebase is never offered. Destructive permission required; disabled by default.",
		map[string]types.ToolProperty{
			"upstream": {Type: "string", Description: "Upstream revision/branch to rebase onto"},
			"continue": {Type: "boolean", Description: "Continue an in-progress rebase (--continue)"},
			"abort":    {Type: "boolean", Description: "Abort the in-progress rebase"},
		}),
	gitTool("git_cherry_pick",
		"Apply commits from another branch. Destructive permission required; disabled by default.",
		map[string]types.ToolProperty{
			"rev":   {Type: "string", Description: "Single commit to cherry-pick"},
			"revs":  {Type: "array", Description: "Commits to cherry-pick", Items: &types.ToolProperty{Type: "string"}},
			"abort": {Type: "boolean", Description: "Abort the in-progress cherry-pick"},
		}),
	gitTool("git_revert",
		"Create revert commits for the given revisions (never opens an editor). Destructive permission required; disabled by default.",
		map[string]types.ToolProperty{
			"rev":   {Type: "string", Description: "Single commit to revert"},
			"revs":  {Type: "array", Description: "Commits to revert", Items: &types.ToolProperty{Type: "string"}},
			"abort": {Type: "boolean", Description: "Abort the in-progress revert"},
		}),
	gitTool("git_tag",
		"Manage tags: action=list (read-only), create or delete. Creation/deletion require the destructive permission and are disabled by default.",
		map[string]types.ToolProperty{
			"action":  {Type: "string", Description: "list (default), create or delete"},
			"name":    {Type: "string", Description: "Tag name (create/delete)"},
			"rev":     {Type: "string", Description: "Commit to tag (create, default HEAD)"},
			"message": {Type: "string", Description: "Create an annotated tag with this message"},
		}),
}

// gitTool builds a git tool declaration, mirroring the types.Tool literal
// shape used by the other built-in tools. The optional arguments list the
// required parameter names.
func gitTool(name, description string, props map[string]types.ToolProperty, required ...string) types.Tool {
	return types.Tool{
		Type: "function",
		Function: types.ToolFunction{
			Name:        name,
			Description: description,
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: props,
				Required:   required,
			},
		},
	}
}
