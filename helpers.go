// Package main — helper functions for chat history, config, UI, and generation lifecycle.
package main

import (
	"context"
	"fmt"
	"fuckyourcli/config"
	"fuckyourcli/logger"
	"fuckyourcli/mcp"
	"fuckyourcli/modes"
	"fuckyourcli/store"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"fuckyourcli/ui"
	"runtime"
	"strings"
	"time"
)

// applyDelay sleeps for the configured delay duration (if any).
func applyDelay() {
	cfg := config.Get()
	if cfg.Delay > 0 {
		time.Sleep(time.Duration(cfg.Delay * float64(time.Second)))
	}
}

// maxKeptScreenshots is the number of most recent computer-use screenshots
// that stay attached to the conversation when a request is sent to the model.
const maxKeptScreenshots = 2

// pruneScreenshotImages returns a copy of history where all but the most
// recent `keep` screenshot images are replaced by a text placeholder, so old
// screenshots do not inflate the context indefinitely.
func pruneScreenshotImages(history []types.Message, keep int) []types.Message {
	out := make([]types.Message, len(history))
	copy(out, history)
	seen := 0
	for i := len(out) - 1; i >= 0; i-- {
		if len(out[i].Images) == 0 {
			continue
		}
		seen++
		if seen <= keep {
			continue
		}
		out[i].Images = nil
		const placeholder = "[screenshot image omitted to save context]"
		if out[i].Content == "" {
			out[i].Content = placeholder
		} else {
			out[i].Content += "\n" + placeholder
		}
	}
	return out
}

// toolImageNotice returns a user-facing warning for every tool that is enabled
// but cannot run because the active provider cannot receive images inside tool
// results (computer use, read_media). Empty string when nothing is blocked.
func toolImageNotice(s *ChatSession) string {
	if s == nil {
		return ""
	}
	const hint = ". Switch to Anthropic, Gemini 3 or a Responses-compatible provider to use the"
	var notices []string
	if s.toolVisible("computer") {
		if reason := tools.ComputerUseBlockReason(); reason != "" {
			notices = append(notices, "Computer use disabled: "+reason+hint+" 'computer' tool.")
		}
	}
	if s.toolVisible("read_media") {
		if reason := tools.ToolImageBlockReason(); reason != "" {
			notices = append(notices, "read_media disabled: "+reason+hint+" 'read_media' tool.")
		}
	}
	return strings.Join(notices, "\n")
}

// webSearchNotice returns a user-facing warning when web_search is enabled but
// no browser was detected, so the tool has been hidden. Empty string otherwise.
func webSearchNotice(s *ChatSession) string {
	if s == nil || !s.isToolEnabled("web_search") {
		return ""
	}
	if reason := tools.WebSearchBlockReason(); reason != "" {
		return "web_search disabled: " + reason + ". Set browser_paths in the config to point at your browser executable."
	}
	return ""
}

// getChatHistoryDir returns the per-workspace cache directory (see store).
func getChatHistoryDir() string {
	dir, err := store.CurrentWorkspaceDir()
	if err != nil {
		return store.WorkspaceDir(".")
	}
	return dir
}

// Allocate the identity before the first request, not only at end-of-turn save.
func ensureSessionIdentity(s *ChatSession) error {
	if s.SessionID == "" {
		id, err := newSessionID(time.Now())
		if err != nil {
			return err
		}
		s.SessionID = id
	}
	if s.SessionCreatedAt.IsZero() {
		s.SessionCreatedAt = time.Now().UTC()
	}
	if s.SessionTitle == "" {
		s.SessionTitle = sessionTitle(s.ChatHistory)
	}
	return nil
}

func sessionLogContext(ctx context.Context, s *ChatSession) context.Context {
	if s == nil {
		return ctx
	}
	if err := ensureSessionIdentity(s); err != nil {
		store.Notice("logs: cannot identify conversation: %v", err)
		return ctx
	}
	return logger.WithSession(ctx, s.SessionID)
}

// newGenerationContext creates the cancellable context of a turn and sets the
// generating flag. It is derived from the active run context (RunCtx in the
// AG-UI server, a background context in the TUI) and lives for the whole turn:
// the provider call AND the tools it runs, so interrupting the turn (ctrl+c, a
// client disconnect) cancels both. Tools reach it through runContext().
func newGenerationContext(s *ChatSession) context.Context {
	parent := context.Background()
	if s != nil && s.RunCtx != nil {
		parent = s.RunCtx
	}
	parent = sessionLogContext(parent, s)
	ctx, cancel := context.WithCancel(parent)
	s.GenerationCancelMu.Lock()
	s.GenerationCancel = cancel
	s.turnCtx = ctx
	s.GenerationCancelMu.Unlock()
	s.IsGenerating.Store(1)
	return ctx
}

// finishGeneration clears the generating flag and cancels the turn context.
func finishGeneration(s *ChatSession) {
	s.IsGenerating.Store(0)
	s.GenerationCancelMu.Lock()
	if s.GenerationCancel != nil {
		s.GenerationCancel()
		s.GenerationCancel = nil
	}
	s.turnCtx = nil
	s.GenerationCancelMu.Unlock()
}

// showChatPrompt offers the workspace's recent chats through a small menu: a
// "Resume a chat" entry opens the session list (with a "← Back" row pinned on
// top). "New chat" leaves s untouched, so a fresh session is created on the next
// turn; a picked session becomes the active one. It returns true when the user
// dismissed the menu (q / Ctrl+C / Esc) instead of choosing, so the caller can
// quit instead of silently starting a new chat.
func showChatPrompt(s *ChatSession) bool {
	workspaceDir := getChatHistoryDir()
	metas, err := listSessions(workspaceDir)
	if err != nil {
		store.Notice("session list: %v", err)
		return false
	}
	if len(metas) == 0 {
		return false
	}

	const maxShown = 50
	// resumeWindow is how many session rows the list shows at once before it
	// scrolls; the "← Back" row is pinned on top and never scrolls away.
	const resumeWindow = 3

	fmt.Println()
	for {
		// Menu: start fresh or open the resume list.
		switch ui.SelectOption("Chats", []string{"New chat", "Resume a chat"}) {
		case 1:
			// fall through to the session list below
		case ui.SelectorCanceled:
			return true // dismissed (q / Ctrl+C / Esc): let the caller quit
		default:
			return false // "New chat"
		}

		// Session list: "← Back" is pinned on top and the sessions scroll.
		options := []string{"← Back"}
		ids := []string{""}
		shown := 0
		for _, m := range metas {
			if shown >= maxShown {
				break
			}
			title := m.Title
			if title == "" {
				title = "(untitled)"
			}
			options = append(options, fmt.Sprintf("%s — %s (%d msgs)", title, sessionAge(m.UpdatedAt), m.MsgCount))
			ids = append(ids, m.ID)
			shown++
		}

		choice := ui.SelectOptionWindow("Resume a chat", options, resumeWindow, 1)
		if choice <= 0 || choice >= len(ids) {
			continue // "← Back" (or cancelled): back to the menu
		}
		if err := resumeSession(s, workspaceDir, ids[choice]); err != nil {
			store.Notice("session %s: %v", ids[choice], err)
			continue
		}
		return false
	}
}

// resumeSession replaces the active history with the stored session, keeping the
// authoritative system prompt (index 0) intact.
func resumeSession(s *ChatSession, workspaceDir, id string) error {
	sf, err := readSessionFile(workspaceDir, id)
	if err != nil {
		return err
	}
	msgs := make([]types.Message, 0, len(sf.Messages)+1)
	if len(s.ChatHistory) > 0 && s.ChatHistory[0].Role == "system" {
		msgs = append(msgs, s.ChatHistory[0])
	}
	for _, m := range sf.Messages {
		if m.Role == "system" {
			continue
		}
		msgs = append(msgs, m)
	}
	s.ChatHistory = msgs
	s.SessionID = sf.ID
	s.SessionCreatedAt = sf.CreatedAt
	s.SessionTitle = sessionTitle(msgs)
	return nil
}

// printHelp displays the CLI usage information.
func printHelp() {
	fmt.Println(ui.TitleStyle.Render("fuckyourcli"))
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  fuckyourcli           start chat")
	fmt.Println("  fuckyourcli auto      start chat in auto mode (no confirmation)")
	fmt.Println("  fuckyourcli config    select provider, model & context (auto-entry to chat after config)")
	fmt.Println("  fuckyourcli --help    show help")
	fmt.Println("  fuckyourcli --version    show version")
	fmt.Println("  fuckyourcli --hash       print the full binary SHA-256")
	fmt.Println("  fuckyourcli serve        start the AG-UI HTTP backend")
	fmt.Println("  fuckyourcli sandbox      dedicated-user sandbox: setup | status | reset (Windows)")
	fmt.Println("  fuckyourcli status       show whether a newer release is available")
	fmt.Println("  fuckyourcli update       update to the latest release (or: update <version>)")
	fmt.Println("  fuckyourcli models       model catalog: 'models' shows stats, 'models update' refreshes from models.dev")
	fmt.Println()
	fmt.Println("Providers:")
	fmt.Println("  - Google Gemini (cloud)")
	fmt.Println("  - OpenAI Completions API (Ollama, Deepseek, etc.)")
	fmt.Println("  - OpenAI Responses API (OpenAI, Ollama, etc.)")
	fmt.Println("  - OpenAI Codex (cloud)")
	fmt.Println("  - Anthropic Messages API (cloud)")
	fmt.Println()
}

func buildSystemPrompt(today, userPreferences string, s *ChatSession) string {
	var rules []string

	if s.toolVisible("ls") || s.toolVisible("read") || s.toolVisible("grep") || s.toolVisible("context_snapshot") {
		rules = append(rules, "Verify before acting: never assume file contents or structure. Use ls, read, grep, or context_snapshot first.")
	}
	if s.toolVisible("read") || s.toolVisible("grep") || s.toolVisible("get_anchors") {
		rules = append(rules, "Truncated answers: read/grep/get_anchors results may begin with a note like \"[read: lines 1-2000 of 51234 …]\" or \"[grep: showing 200 of 1832 matches …]\". That is a cut, not the whole result — continue with offset/limit or refine pattern/include_glob. Never report the missing part as absent or empty.")
	}
	if s.toolVisible("get_anchors") && s.toolVisible("replace_anchors") {
		rules = append(rules, `Editing existing files:
  1. get_anchors → reads file with anchor-annotated lines (Word:hash§content, § = display separator, not part of the anchor)
  2. replace_anchors(path, start_anchor, end_anchor, replacement) → replaces the range from start_anchor to end_anchor INCLUSIVE.
     • After an edit, consumed anchors are gone — use only anchors from the "new anchors:" response
     • Single line replace: start_anchor = end_anchor = that line's anchor
     • Delete range: replacement = ""
     • Insert after line: both anchors = that line, include original line content first in replacement
Note: The anchor word might be a deterministic combination of two words (e.g., WordWord:hash). Treat it as a single opaque identifier. Do not attempt to split or reconstruct these identifiers; they are generated automatically when the anchor pool is exhausted.
Never use write on existing files.`)
	} else if s.toolVisible("write") {
		rules = append(rules, "write is intended for new files. Avoid using it for existing files.")
	}
	if s.webSearchEnabled() {
		rules = append(rules, "Web search: if default engine fails, retry with another. Always crawl and fetch the full content of the target URLs found in the search results before reporting.")
	}
	// after the web search block, before skills:
	var failureRules []string
	failureRules = append(failureRules,
		//"• Transient (timeout, process exit): retry once, then report",
		"• User rejection/stop: stop immediately. Do not rephrase or try to bypass. If the user provided a reason for the stop, acknowledge it, adapt your future strategy based on it, or ask for new instructions.", "• File/path not found: verify with ls before retrying",
		//"• Unknown error: report verbatim, ask user before proceeding",
		"• Unknown error: report the exact error message and summary (avoiding raw dump if too verbose), then ask user how to proceed",
	)
	if s.toolVisible("get_anchors") && s.toolVisible("replace_anchors") {
		failureRules = append(failureRules,
			"• replace_anchors anchor not found: re-read with get_anchors, do not guess anchors",
			"• replace_anchors stale-file conflict: re-read with get_anchors and reconsider the patch using the current content. Preserve external/user edits; never bypass the conflict with write or sh.",
		)
	}
	rules = append(rules, "Tool failures:\n  "+strings.Join(failureRules, "\n  "))
	if s.toolVisible("skills_discovery") && s.toolVisible("read_skill") {
		rules = append(rules, "Skills: run skills_discovery at session start for non-trivial tasks. If a skill matches, read_skill before proceeding.")
	}
	if active := s.activeGitTools(); len(active) > 0 {
		// The rule only names the git tools actually exposed to the model:
		// listing the disabled ones (e.g. every mutating tool while only the
		// read-only set is active) advertised tools the model cannot call.
		var readOnly, localWrite, network, destructive []string
		forcePush := false
		for _, name := range active {
			if name == "git_push" {
				forcePush = true
			}
			switch tier, _ := config.GitToolTierFor(name); tier {
			case config.GitTierRead:
				readOnly = append(readOnly, name)
			case config.GitTierWrite:
				localWrite = append(localWrite, name)
			case config.GitTierNetwork:
				network = append(network, name)
			case config.GitTierDestructive:
				destructive = append(destructive, name)
			}
		}
		parts := []string{"Git: always use the git_* tools, never `git` through sh (the sandbox masks .git)."}
		if len(readOnly) > 0 {
			parts = append(parts, "Read-only tools ("+strings.Join(readOnly, "/")+") need no confirmation.")
		}
		var gated []string
		if len(localWrite) > 0 {
			gated = append(gated, strings.Join(localWrite, "/")+" (local-write)")
		}
		if len(network) > 0 {
			gated = append(gated, strings.Join(network, "/")+" (network)")
		}
		if len(destructive) > 0 {
			gated = append(gated, strings.Join(destructive, "/")+" (destructive)")
		}
		if len(gated) > 0 {
			parts = append(parts, "Mutating git tools require a permission prompt: "+strings.Join(gated, ", ")+".")
		}
		if forcePush {
			parts = append(parts, "Force push additionally requires git.allow_force_push.")
		}
		parts = append(parts, "Git tools are not available to sub-agents.")
		rules = append(rules, strings.Join(parts, " "))
	}
	if s.toolVisible("computer") && tools.ComputerUseBlockReason() == "" {
		rules = append(rules, "Computer use: the 'computer' tool controls the user's real desktop. Always take a screenshot first and give coordinates (x, y, x2, y2) in the screenshot's pixel space — they are mapped to real screen coordinates automatically. After each action, take a fresh screenshot to verify the result before acting again.")
	}
	if s.toolVisible("read_user_preferences") {
		rules = append(rules, "User preferences: they are already injected in the prompt (pre-loaded) — do NOT call read_user_preferences unless you are about to change them; call it only immediately before update_user_preferences.")
	}
	rules = append(rules, "Brevity: ≤4 lines for conversational replies. Let tool output speak for itself.")

	numbered := make([]string, len(rules))
	for i, r := range rules {
		numbered[i] = fmt.Sprintf("%d. %s", i+1, r)
	}

	// The base "persona" preamble is user-editable (config UI → CLI Settings →
	// System Prompt) and stored as a template with ${VAR} placeholders. Only the
	// environment-variable substitution happens here — no shell is invoked —
	// and the tool-derived <rules> block below stays fully dynamic.
	vars := map[string]string{
		"FYC_TODAY":       today,
		"FYC_CWD":         s.RestrictedRoot,
		"FYC_OS":          runtime.GOOS,
		"FYC_SHELL":       config.ShellNote(runtime.GOOS),
		"FYC_PREFERENCES": userPreferences,
	}
	preamble := config.RenderSystemPrompt(config.SystemPromptTemplate(), vars)

	base := preamble + "\n\n<rules>\n" + strings.Join(numbered, "\n\n") + "\n</rules>"
	if s != nil && s.ProjectContext != "" {
		base += "\n\n" + s.ProjectContext
	}
	return base
}

func updateSystemPrompt(s *ChatSession) {
	today := time.Now().Format("Monday, January 2, 2006")
	sysPrompt := buildSystemPrompt(today, loadUserPreferences(), s)
	for i := range s.ChatHistory {
		if s.ChatHistory[i].Role == "system" {
			s.ChatHistory[i].Content = sysPrompt
			return
		}
	}
	s.ChatHistory = append([]types.Message{{Role: "system", Content: sysPrompt}}, s.ChatHistory...)
}

// newSession builds and bootstraps a ChatSession without the interactive TUI.
// It mirrors the non-visual part of the startup sequence in main.go and is used
// by the AG-UI server (`fuckyourcli serve`): system prompt, MCP servers, ignore
// rules, and the LSP/git availability layer are all initialized synchronously.
func newSession() *ChatSession {
	s := &ChatSession{AvailableTools: availableTools}
	s.Version = version
	if s.Version == "" {
		s.Version = "dev"
	}
	s.initRestrictedRoot()
	s.Commit = computeShortSHA()

	cfg := config.Get()

	s.MCPManager = mcp.NewMCPManager(s.Version)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.MCPManager.Initialize(ctx, cfg.MCPServers); err == nil {
		if mcpTools, err := s.MCPManager.ListTools(ctx); err == nil {
			s.AvailableTools = append(s.AvailableTools, mcpTools...)
		}
	}
	tools.SetGlobalSubagentTools(s.AvailableTools)

	s.GlobalIgnore = tools.LoadIgnoreFiles(s.RestrictedRoot)
	s.ProjectContext, _ = tools.LoadProjectContext(s.RestrictedRoot)
	s.refreshLSPToolAvailability()
	s.refreshGitToolAvailability()
	s.refreshWebSearchAvailability()

	s.ChatHistory = append(s.ChatHistory, types.Message{
		Role:    "system",
		Content: buildSystemPrompt(time.Now().Format("Monday, January 2, 2006"), loadUserPreferences(), s),
	})
	return s
}

// buildSuggestionProvider returns a function that filters commands by prefix for autocomplete.
func buildSuggestionProvider(s *ChatSession) ui.SuggestionProvider {
	return func(line string) (matches []ui.Suggestion, extraLine string) {
		if strings.HasPrefix(line, "/skills") {
			skills := tools.DiscoverSkills(s.RestrictedRoot)
			query := strings.TrimSpace(strings.TrimPrefix(line, "/skills"))
			for name, desc := range skills {
				if query == "" || strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
					matches = append(matches, ui.Suggestion{Name: "/skills " + name, Description: desc})
				}
			}
			if len(matches) > 0 {
				return matches, "Use ↑/↓ to select, Enter to insert"
			}
			return nil, "No matching skills"
		}
		if !strings.HasPrefix(line, "/") || strings.Contains(line, " ") {
			return nil, ""
		}
		for _, cmd := range modes.GetCommands(s.webSearchEnabled()) {
			s := ui.Suggestion{Name: cmd.Name, Description: cmd.Description}
			if strings.HasPrefix(s.Name, line) {
				matches = append(matches, s)
			}
		}
		return matches, ""
	}
}

// runConfig launches the interactive configuration flow.
func runConfig() bool {
	return config.RunConfig()
}
