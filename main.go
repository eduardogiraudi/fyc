package main

import (
	"context"
	"fmt"
	"fuckyourcli/config"
	"fuckyourcli/crawl"
	"fuckyourcli/mcp"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"fuckyourcli/ui"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	tools.MaybeRunAsSandboxChild()
	tools.MaybeRunAsSandboxRunner()
	// Keep checksum output independent of configuration and UI initialization.
	if len(os.Args) > 1 && os.Args[1] == "--hash" {
		hash, err := computeBinarySHA()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fuckyourcli --hash: "+err.Error())
			os.Exit(1)
		}
		fmt.Println(hash)
		return
	}
	session := &ChatSession{
		AvailableTools: availableTools,
	}
	session.Version = version
	if session.Version == "" {
		session.Version = "dev" // fallback if not set via -ldflags
	}
	session.initRestrictedRoot()
	config.Load()
	// Version info — one line per field
	session.Commit = computeShortSHA()
	ui.InitMarkdownRenderer()

	// First run: when config.json is missing (or carries no provider), both
	// `fuckyourcli` and `fuckyourcli config` go through the provider setup
	// wizard, which then asks what to do next.
	if (len(os.Args) == 1 || os.Args[1] == "config") && config.NeedsSetup() {
		switch config.RunFirstTimeSetup() {
		case config.FirstRunQuit:
			return
		case config.FirstRunConfig:
			if !runConfig() {
				return
			}
			// "Start Chatting" from the config menu: fall through to chat.
		}
		// FirstRunChat falls through to the chat startup.
	} else if len(os.Args) > 1 {

		switch os.Args[1] {

		case "add":
			if err := runAddCommand(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli add: "+err.Error())
				os.Exit(1)
			}
			return
		case "mcp":
			if err := runMCPCommand(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli mcp: "+err.Error())
				os.Exit(1)
			}
			return
		case "config":
			// Run configuration and check if user wants to enter chat
			if runConfig() {
				// User wants to enter chat after configuration
				// Continue to chat interface (don't return)
			} else {
				// User doesn't want to enter chat, exit
				return
			}

		case "auto":
			config.SetNoAsk(true)
		case "serve":
			if err := runServe(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli serve: "+err.Error())
				os.Exit(1)
			}
			return
		case "sandbox":
			if err := runSandboxCommand(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli sandbox: "+err.Error())
				os.Exit(1)
			}
			return
		case "update":
			if err := runUpdate(session, false, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli update: "+err.Error())
				os.Exit(1)
			}
			return
		case "status":
			if err := runUpdate(session, true, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli status: "+err.Error())
				os.Exit(1)
			}
			return
		case "models":
			if err := runModelsCommand(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fuckyourcli models: "+err.Error())
				os.Exit(1)
			}
			return
		case "--help":
			printHelp()
			return
		case "--version":
			fmt.Printf("v%s sha:%s\n", session.Version, session.Commit)
			return
		}
	}

	cfg := config.Get()
	// A remembered workspace whose roots cover this folder offers itself before
	// the session starts (/cd stores those sets in config.json). The prompt is a
	// nested one: the alternate screen is not up yet, and nothing of it reaches
	// the transcript. Dismissing it (q/Esc/Ctrl+C) quits, exactly like the chat
	// picker.
	if startupWorkspaceOffer(session) {
		return
	}
	// altScreen switches the chat between the full-screen TUI (default) and the
	// inline REPL. In alt-screen mode nothing may be printed before the
	// chat transcript instead (see emit).
	altScreen := cfg.AltScreen
	// altSeed keeps the seed order: text blocks and expandable tool results
	// interleaved exactly as they were produced.
	var altSeed []ui.SeedBlock
	emit := func(block string) {
		if altScreen {
			altSeed = append(altSeed, ui.SeedBlock{Text: block})
			return
		}
		fmt.Println(block)
	}
	info := []string{
		fmt.Sprintf("%s %s %s %s %s", ui.ValueStyle.Render("v"+session.Version), ui.KeyStyle.Render("• OS:"), ui.ValueStyle.Render(runtime.GOOS), ui.KeyStyle.Render("• sha:"), ui.ValueStyle.Render(session.Commit)),
		fmt.Sprintf("%s %s", ui.KeyStyle.Render("cwd:"), ui.ValueStyle.Render(session.RestrictedRoot)),
	}
	// A session with extra roots has to show them: they are the difference
	// between "the model cannot touch that" and "it can".
	if roots := session.workspaceRoots(); len(roots) > 1 {
		info = append(info, fmt.Sprintf("%s %s", ui.KeyStyle.Render("roots:"), ui.ValueStyle.Render(strings.Join(roots[1:], " · "))))
	}
	providerInfo := config.GetCurrentProviderInfo()
	info = append(info, fmt.Sprintf("%s %s %s %s", ui.KeyStyle.Render("provider:"), ui.ValueStyle.Render(providerInfo), ui.KeyStyle.Render("• model:"), ui.ValueStyle.Render(cfg.Model)))
	if notice := toolImageNotice(session); notice != "" {
		info = append(info, ui.TipStyle.Render(notice))
	}
	info = append(info, fmt.Sprintf("%s %s %s %s", ui.KeyStyle.Render("reasoning:"), ui.ValueStyle.Render(cfg.Reasoning.Label()), ui.KeyStyle.Render("• temperature:"), ui.ValueStyle.Render(currentTemperatureLabel())))
	if cfg.CompactionEnabled {
		label := fmt.Sprintf("on @%d tokens", config.EffectiveCompactionThreshold())
		label += " • compactor: " + cfg.CompactorLabel()
		if cfg.CompactionMidTurn {
			label += " • mid-turn: on"
		}
		info = append(info, fmt.Sprintf("%s %s", ui.KeyStyle.Render("compaction:"), ui.ValueStyle.Render(label)))
	} else {
		info = append(info, fmt.Sprintf("%s %s", ui.KeyStyle.Render("compaction:"), ui.ValueStyle.Render("off")))
	}

	var ctxSummary string
	session.ProjectContext, ctxSummary = tools.LoadWorkspaceContext(session.workspaceRoots())
	if ctxSummary != "" {
		info = append(info, fmt.Sprintf("%s %s", ui.KeyStyle.Render("context:"), ui.ValueStyle.Render(ctxSummary)))
	}

	var extras []string
	if cfg.NoAsk {
		extras = append(extras, ui.OkStyle.Render("mode: auto"))
	}
	if cfg.Delay > 0 {
		extras = append(extras, ui.InfoStyle.Render(fmt.Sprintf("delay: %.1fs", cfg.Delay)))
	}
	if len(extras) > 0 {
		info = append(info, strings.Join(extras, "  "))
	}
	emit(ui.StartupHeaderBlock(cfg.CompactBanner, info))
	today := time.Now().Format("Monday, January 2, 2006")
	userPreferences := loadUserPreferences()
	sysPrompt := buildSystemPrompt(today, userPreferences, session)
	session.ChatHistory = append(session.ChatHistory, types.Message{
		Role:    "system",
		Content: sysPrompt,
	})
	emit(ui.TipsBlock(cfg.AltScreen))

	// Initialize MCP servers in background with spinner
	session.MCPManager = mcp.NewMCPManager(session.Version, config.MCPOAuthStore{})
	mcpDone := make(chan struct{})
	var mcpWg sync.WaitGroup
	mcpWg.Add(1)
	var mcpWarnings []string
	go func() {
		defer close(mcpDone)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := session.MCPManager.Initialize(ctx, config.SessionMCPServers(session.RestrictedRoot)); err != nil {
			mcpWarnings = append(mcpWarnings, fmt.Sprintf("Warning: MCP initialization failed: %v", err))
		}
		listCtx, listCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer listCancel()
		mcpTools, err := session.MCPManager.ListTools(listCtx)
		if err != nil {
			mcpWarnings = append(mcpWarnings, fmt.Sprintf("Warning: failed to list MCP tools: %v", err))
		}
		session.AvailableTools = append(session.AvailableTools, mcpTools...)
		if len(mcpTools) > 0 {
			mcpWarnings = append(mcpWarnings, fmt.Sprintf("Loaded %d MCP tools", len(mcpTools)))
		}
		tools.SetGlobalSubagentTools(session.AvailableTools)
	}()
	ui.AnimateSpinner("Starting MCP servers...", mcpDone, &mcpWg, "")
	for _, msg := range mcpWarnings {
		emit(ui.TipStyle.Render(msg))
	}

	defer crawl.Shutdown()
	defer tools.CloseLSPManagers()
	defer session.MCPManager.Shutdown(context.Background())
	defer tools.ClosePortalSession()

	// Load ignore rules in background with spinner
	ignoreDone := make(chan struct{})
	var ignoreWg sync.WaitGroup
	ignoreWg.Add(1)
	go func() {
		session.GlobalIgnore = tools.LoadWorkspaceIgnore(session.workspaceRoots())
		close(ignoreDone)
	}()
	ui.AnimateSpinner("Indexing files to ignore...", ignoreDone, &ignoreWg, "")
	lspDone := make(chan struct{})
	var lspWg sync.WaitGroup
	lspWg.Add(1)
	go func() {
		defer close(lspDone)
		session.refreshLSPToolAvailability()
		// Git availability is refreshed with the same cadence: the workspace
		// may be (or stop being) a repository at any time.
		session.refreshGitToolAvailability()
		session.refreshWebSearchAvailability()
	}()
	ui.AnimateSpinner("Starting LSP servers & detecting git/browser...", lspDone, &lspWg, "")
	if notice := webSearchNotice(session); notice != "" {
		emit(ui.TipStyle.Render(notice))
	}
	lspHeartbeatCtx, stopLSPHeartbeat := context.WithCancel(context.Background())
	defer stopLSPHeartbeat()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-lspHeartbeatCtx.Done():
				return
			case <-ticker.C:
				changed := false
				// Git tools keep being refreshed even when every LSP tool is
				// user-disabled.
				if !allGitToolsUserDisabled() && session.refreshGitToolAvailability() {
					changed = true
				}
				if !allLSPToolsUserDisabled() && session.refreshLSPToolAvailability() {
					changed = true
				}
				// HTTP-backed web search keeps its runtime policy hook.
				if session.refreshWebSearchAvailability() {
					changed = true
				}
				if changed {
					updateSystemPrompt(session)
				}
			}
		}
	}()
	updateSystemPrompt(session)

	// in alt-screen mode those blocks have to be seeded into the transcript,
	// because the alternate screen hides whatever stdout showed.
	if altScreen {
		ui.SetPreProgramBlockSink(func(block ui.SeedBlock) { altSeed = append(altSeed, block) })
	}
	dismissed := showChatPrompt(session)
	ui.SetPreProgramBlockSink(nil)
	if dismissed {
		// The user dismissed the chat picker (q / Ctrl+C / Esc): quit instead
		// of silently starting a new chat.
		return
	}

	// Load persistent history from file
	persistedHistory := loadHistoryEntries()

	toggleAutoModeCmd := func() tea.Cmd {
		return func() tea.Msg { return toggleAutoModeStats() }
	}

	ui.SetCtrlOHandler(toggleAutoModeCmd)

	session.Sink = ui.NewTerminalOutputSink(persistedHistory, buildSuggestionProvider(session))

	chatSink := session.Sink.(*ui.TerminalOutputSink)
	chat := newChatModel(session, chatSink, persistedHistory, buildSuggestionProvider(session))
	opts := []tea.ProgramOption{tea.WithoutSignals()}
	if altScreen {
		chat.enableAltScreenBlocks(altSeed, func(width int) string {
			return ui.StartupHeaderBlockAtWidth(cfg.CompactBanner, info, width)
		})
		// Mouse reporting is what turns a wheel turn into a MouseMsg: without
		// it the terminal sends arrow keys and the wheel walks the input
		// history. All-motion (not just button-event) reporting is what the
		// status bar needs: the pointer moves with no button pressed are the
		// hover that tells the user the mode badge and the "rows below" hint
		// are clickable.
		opts = append(opts, tea.WithAltScreen(), tea.WithMouseAllMotion())
	}
	prog := tea.NewProgram(chat, opts...)
	chatSink.AttachProgram(prog)
	if altScreen {
		// Confirmations and menus are drawn inside the frame (see ui.runInApp)
		// and the mouse mode survives a nested prompt: in the alternate screen
		// there is no scrollback to fall back into.
		ui.EnableInAppPrompts()
		ui.SetPromptMouse(true)
	}
	// Stop the status-bar hook refresher on every exit path from main,
	// including a panic unwinding through prog.Run (a plain call after Run
	// would be skipped in that case). The in-app host goes with it: a prompt
	// sent to a program that already returned would block for good.
	defer stopStatusHook()
	defer ui.DisableInAppPrompts()
	if _, err := prog.Run(); err != nil {
		fmt.Println(ui.InfoStyle.Render("Error: " + err.Error()))
	}
	if err := saveChatSession(session); err != nil {
		fmt.Println(ui.InfoStyle.Render("Warning: could not save chat: " + err.Error()))
	}
}
