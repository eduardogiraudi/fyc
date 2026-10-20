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
	info := []string{
		fmt.Sprintf("%s %s %s %s %s", ui.ValueStyle.Render("v"+session.Version), ui.KeyStyle.Render("• OS:"), ui.ValueStyle.Render(runtime.GOOS), ui.KeyStyle.Render("• sha:"), ui.ValueStyle.Render(session.Commit)),
		fmt.Sprintf("%s %s", ui.KeyStyle.Render("cwd:"), ui.ValueStyle.Render(session.RestrictedRoot)),
	}
	providerInfo := config.GetCurrentProviderInfo()
	info = append(info, fmt.Sprintf("%s %s %s %s", ui.KeyStyle.Render("provider:"), ui.ValueStyle.Render(providerInfo), ui.KeyStyle.Render("• model:"), ui.ValueStyle.Render(cfg.Model)))
	if notice := toolImageNotice(session); notice != "" {
		info = append(info, ui.TipStyle.Render(notice))
	}
	info = append(info, fmt.Sprintf("%s %s %s %s", ui.KeyStyle.Render("reasoning:"), ui.ValueStyle.Render(cfg.Reasoning.Label()), ui.KeyStyle.Render("• temperature:"), ui.ValueStyle.Render(currentTemperatureLabel())))
	if cfg.CompactionEnabled {
		label := fmt.Sprintf("on @ %d tokens", config.EffectiveCompactionThreshold())
		label += " • compactor: " + cfg.CompactorLabel()
		if cfg.CompactionMidTurn {
			label += " • mid-turn: on"
		}
		info = append(info, fmt.Sprintf("%s %s", ui.KeyStyle.Render("compaction:"), ui.ValueStyle.Render(label)))
	} else {
		info = append(info, fmt.Sprintf("%s %s", ui.KeyStyle.Render("compaction:"), ui.ValueStyle.Render("off")))
	}

	var ctxSummary string
	session.ProjectContext, ctxSummary = tools.LoadProjectContext(session.RestrictedRoot)
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
	ui.RenderStartupHeader(cfg.CompactBanner, info)
	today := time.Now().Format("Monday, January 2, 2006")
	userPreferences := loadUserPreferences()
	sysPrompt := buildSystemPrompt(today, userPreferences, session)
	session.ChatHistory = append(session.ChatHistory, types.Message{
		Role:    "system",
		Content: sysPrompt,
	})
	ui.RenderTips()

	// Initialize MCP servers in background with spinner
	session.MCPManager = mcp.NewMCPManager(session.Version)
	mcpDone := make(chan struct{})
	var mcpWg sync.WaitGroup
	mcpWg.Add(1)
	var mcpWarnings []string
	go func() {
		defer close(mcpDone)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := session.MCPManager.Initialize(ctx, config.Get().MCPServers); err != nil {
			mcpWarnings = append(mcpWarnings, fmt.Sprintf("Warning: MCP initialization failed: %v", err))
		} else {
			mcpTools, err := session.MCPManager.ListTools(ctx)
			if err != nil {
				mcpWarnings = append(mcpWarnings, fmt.Sprintf("Warning: failed to list MCP tools: %v", err))
			} else {
				session.AvailableTools = append(session.AvailableTools, mcpTools...)
				if len(mcpTools) > 0 {
					mcpWarnings = append(mcpWarnings, fmt.Sprintf("Loaded %d MCP tools", len(mcpTools)))
				}
			}
		}
		tools.SetGlobalSubagentTools(session.AvailableTools)
	}()
	ui.AnimateSpinner("Starting MCP servers...", mcpDone, &mcpWg, "")
	for _, msg := range mcpWarnings {
		fmt.Println(ui.TipStyle.Render(msg))
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
		session.GlobalIgnore = tools.LoadIgnoreFiles(session.RestrictedRoot)
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
		fmt.Println(ui.TipStyle.Render(notice))
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
				// web_search is hidden while no browser is available.
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

	if showChatPrompt(session) {
		// The user dismissed the chat picker (q / Ctrl+C / Esc): quit instead
		// of silently starting a new chat.
		return
	}

	// Load persistent history from file
	persistedHistory := loadHistoryEntries()

	toggleAutoModeCmd := func() tea.Cmd {
		cfg := config.Get()
		current := cfg.NoAsk
		config.SetNoAsk(!current)
		modeStr := "manual"
		if !current {
			modeStr = "auto"
		}
		providerInfo := config.GetCurrentProviderInfo()
		providerLine := ui.RenderProviderInfo(providerInfo, currentTemperatureLabel(), modeStr)
		return func() tea.Msg {
			return ui.StreamStatsMsg{
				ProviderInfo: providerLine,
				TokenStats:   "",
			}
		}
	}

	ui.SetCtrlOHandler(toggleAutoModeCmd)

	session.Sink = ui.NewTerminalOutputSink(persistedHistory, buildSuggestionProvider(session))

	chatSink := session.Sink.(*ui.TerminalOutputSink)
	chat := newChatModel(session, chatSink, persistedHistory, buildSuggestionProvider(session))
	prog := tea.NewProgram(chat, tea.WithoutSignals())
	chatSink.AttachProgram(prog)
	// Stop the status-bar hook refresher on every exit path from main,
	// including a panic unwinding through prog.Run (a plain call after Run
	// would be skipped in that case).
	defer stopStatusHook()
	if _, err := prog.Run(); err != nil {
		fmt.Println(ui.InfoStyle.Render("Error: " + err.Error()))
	}
	if err := saveChatSession(session); err != nil {
		fmt.Println(ui.InfoStyle.Render("Warning: could not save chat: " + err.Error()))
	}
}
