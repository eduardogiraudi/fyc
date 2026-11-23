package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/models"
	"fuckyourcli/modes"
	"fuckyourcli/reasoning"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"fuckyourcli/ui"
)

func toggleAutoMode(sink types.OutputSink) {
	cfg := config.Get()
	current := cfg.NoAsk
	config.SetNoAsk(!current)
	modeStr := "manual"
	if !current {
		modeStr = "auto"
	}
	providerInfo := config.GetCurrentProviderInfo()
	providerLine := ui.RenderProviderInfo(providerInfo, currentTemperatureLabel(), modeStr)
	sink.WriteStats(providerLine, "")
	if !current {
		sink.WriteText(ui.OkStyle.Render("Auto mode enabled (session only)"))
		sink.WriteNewline()
	} else {
		sink.WriteText(ui.InfoStyle.Render("Auto mode disabled"))
		sink.WriteNewline()
	}
}

func padTo(styled, original string, w int) string {
	visualLen := len(original)
	padNeeded := w - visualLen
	if padNeeded < 0 {
		padNeeded = 0
	}
	return styled + strings.Repeat(" ", padNeeded)
}

func handleSlashCommand(input string, s *ChatSession) (bool, string) {
	// A command run beside a generation captures its output (see
	// runLiveCommand): the transcript belongs to the model, so the caller shows
	// it as a transient notice instead.
	sink := s.commandSink()
	if !strings.HasPrefix(input, "/") {
		return false, ""
	}

	// Handle /help command
	if input == "/help" {
		allCmds := modes.GetCommands(s.webSearchEnabled())
		maxCmd := 0
		maxDesc := 0
		for _, cmd := range allCmds {
			if len(cmd.Name) > maxCmd {
				maxCmd = len(cmd.Name)
			}
			if len(cmd.Description) > maxDesc {
				maxDesc = len(cmd.Description)
			}
		}
		if maxCmd < 7 {
			maxCmd = 7
		}
		if maxDesc < 11 {
			maxDesc = 11
		}
		cmdW := maxCmd + 2
		descW := maxDesc + 2
		hSep := strings.Repeat("─", cmdW)
		dSep := strings.Repeat("─", descW)

		sink.WriteText(fmt.Sprintf("┌%s┬%s┐", hSep, dSep))
		sink.WriteNewline()
		sink.WriteText(fmt.Sprintf("│ %s │ %s │",
			padTo(ui.TitleStyle.Render("Command"), "Command", cmdW-2),
			padTo(ui.TitleStyle.Render("Description"), "Description", descW-2)))
		sink.WriteNewline()
		sink.WriteText(fmt.Sprintf("├%s┼%s┤", hSep, dSep))
		sink.WriteNewline()
		for _, cmd := range allCmds {
			sink.WriteText(fmt.Sprintf("│ %s │ %s │",
				padTo(ui.OkStyle.Render(cmd.Name), cmd.Name, cmdW-2),
				padTo(ui.InfoStyle.Render(cmd.Description), cmd.Description, descW-2)))
			sink.WriteNewline()
		}
		sink.WriteText(fmt.Sprintf("└%s┴%s┘", hSep, dSep))
		sink.WriteNewline()
		sink.WriteNewline()
		return true, ""
	}

	// Handle /auto command
	if input == "/auto" || strings.HasPrefix(input, "/auto ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/auto"))
		if arg == "on" {
			if !config.Get().NoAsk {
				toggleAutoMode(sink)
			} else {
				sink.WriteText(ui.OkStyle.Render("Auto mode enabled (session only)"))
				sink.WriteNewline()
			}
		} else if arg == "off" {
			if config.Get().NoAsk {
				toggleAutoMode(sink)
			} else {
				sink.WriteText(ui.InfoStyle.Render("Auto mode disabled"))
				sink.WriteNewline()
			}
		} else {
			toggleAutoMode(sink)
		}
		return true, ""
	}

	// Handle /airplane command
	if input == "/airplane" || strings.HasPrefix(input, "/airplane ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/airplane"))
		if arg == "" {
			s.setToolEnabled("web_search", !s.webSearchEnabled())
		} else if arg == "on" {
			s.setToolEnabled("web_search", false)
		} else if arg == "off" {
			s.setToolEnabled("web_search", true)
		} else {
			sink.WriteText(ui.InfoStyle.Render("Usage: /airplane [on|off]"))
			sink.WriteNewline()
			return true, ""
		}
		if !s.webSearchEnabled() && s.CurrentMode != nil && s.CurrentMode.RequiresWebSearch() {
			s.CurrentMode = nil
			s.persistentModeInjected = ""
		}
		updateSystemPrompt(s)
		if !s.webSearchEnabled() {
			sink.WriteText(ui.InfoStyle.Render("Airplane mode enabled: web_search and /deep disabled"))
			sink.WriteNewline()
		} else {
			sink.WriteText(ui.OkStyle.Render("Airplane mode disabled: web_search and /deep enabled"))
			sink.WriteNewline()
		}
		return true, ""
	}

	if input == "/retry" || strings.HasPrefix(input, "/retry ") {
		if strings.TrimSpace(strings.TrimPrefix(input, "/retry")) != "" {
			sinkLine(sink, ui.InfoStyle.Render("Usage: /retry"))
		} else if retryFailedRequestWith(s, func(s *ChatSession) { streamChatWithRetry(s, true) }) {
			s.autosave()
		}
		return true, ""
	}

	// Handle /undo command
	if input == "/undo" || strings.HasPrefix(input, "/undo ") {
		handleUndoCommand(s, input)
		return true, ""
	}

	// Handle /cd command: the roots the session is allowed to touch.
	if input == "/cd" || strings.HasPrefix(input, "/cd ") {
		handleCDCommand(s, input)
		return true, ""
	}

	// Handle /clear command
	if input == "/clear" || strings.HasPrefix(input, "/clear ") {
		var filtered []types.Message
		for _, msg := range s.ChatHistory {
			if msg.Role == "system" {
				filtered = append(filtered, msg)
			}
		}
		s.ChatHistory = filtered
		s.retryRequest = nil
		// The turns the journal described are gone with the conversation: drop the
		// per-turn state (and the journal of this session) instead of leaving
		// records that can never be rewound to.
		if s.journal != nil {
			s.journal.Discard()
		}
		s.realignTurnState(0)
		s.resetTurnSnapshots()
		tools.ResetAnchorState()
		// The cleared context no longer carries the persistent mode's
		// instructions, and /clear returns to plain chat: drop both so the mode
		// does not linger without its instructions.
		s.persistentModeInjected = ""
		s.CurrentMode = nil
		updateSystemPrompt(s)
		sink.WriteText(ui.OkStyle.Render("Context cleared"))
		sink.WriteNewline()
		return true, ""
	}

	// Handle /compact command
	if input == "/compact" || strings.HasPrefix(input, "/compact ") {
		if len(s.ChatHistory) <= 1 {
			sink.WriteText(ui.InfoStyle.Render("Nothing to compact"))
			sink.WriteNewline()
			return true, ""
		}
		originalCount := len(s.ChatHistory)
		before, after, res, err := compactContext(s, sink)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				sinkLine(sink, ui.InfoStyle.Render("Compaction interrupted."))
			} else {
				sinkLine(sink, ui.ErrorStyle.Render("✗ Error during compaction: "+err.Error()+"."+compactionFailureHint(config.Get(), false)))
			}
			return true, ""
		}

		if res.FellBack {
			sinkLine(sink, ui.TipStyle.Render(compactionFallbackNotice(res)))
		}
		sinkLine(sink, ui.OkStyle.Render(fmt.Sprintf("✓ Context compacted. History reduced from %d to %d messages (~%d → ~%d tokens, compactor: %s).", originalCount, len(s.ChatHistory), before, after, res.Model)))
		return true, ""
	}

	// Handle /usage command
	if input == "/usage" || strings.HasPrefix(input, "/usage ") {
		if strings.TrimSpace(strings.TrimPrefix(input, "/usage")) != "" {
			sinkLine(sink, ui.InfoStyle.Render("Usage: /usage"))
		} else {
			showUsage(s)
		}
		return true, ""
	}

	// Handle /sub command
	if input == "/sub" || strings.HasPrefix(input, "/sub ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/sub"))
		if arg == "" {
			if s.subagentsEnabled() {
				sink.WriteText(ui.InfoStyle.Render("Sub-agent tools: enabled"))
				sink.WriteNewline()
			} else {
				sink.WriteText(ui.InfoStyle.Render("Sub-agent tools: disabled"))
				sink.WriteNewline()
			}
		} else if arg == "off" {
			s.setToolEnabled("spawn_subagents", false)
			sink.WriteText(ui.InfoStyle.Render("Sub-agent tools disabled"))
			sink.WriteNewline()
		} else if arg == "on" {
			s.setToolEnabled("spawn_subagents", true)
			sink.WriteText(ui.OkStyle.Render("Sub-agent tools enabled"))
			sink.WriteNewline()
		} else {
			sink.WriteText(ui.InfoStyle.Render("Usage: /sub on|off"))
			sink.WriteNewline()
		}
		updateSystemPrompt(s)
		return true, ""
	}

	if input == "/tools" || strings.HasPrefix(input, "/tools ") {
		changed, toolMsg := config.ToggleTools(s.allToolNames())
		lspChanged := s.refreshLSPToolAvailability()
		gitChanged := s.refreshGitToolAvailability()
		if toolMsg != "" {
			sink.WriteText(toolMsg)
			sink.WriteNewline()
		}
		if !s.webSearchEnabled() && s.CurrentMode != nil && s.CurrentMode.RequiresWebSearch() {
			s.CurrentMode = nil
			s.persistentModeInjected = ""
		}
		if changed || lspChanged || gitChanged {
			updateSystemPrompt(s)
		}
		if notice := toolImageNotice(s); notice != "" {
			sink.WriteText(ui.InfoStyle.Render(notice))
			sink.WriteNewline()
		}
		return true, ""
	}

	if input == "/hooks" || strings.HasPrefix(input, "/hooks ") {
		if config.EditHooks() {
			sink.WriteText(ui.OkStyle.Render("Tool hooks updated."))
		} else {
			sink.WriteText(ui.InfoStyle.Render("No changes made."))
		}
		sink.WriteNewline()
		return true, ""
	}

	if input == "/sysprompt" || strings.HasPrefix(input, "/sysprompt ") {
		config.EditSystemPrompt(func(line string) {
			sink.WriteText(line)
			sink.WriteNewline()
		})
		updateSystemPrompt(s)
		sink.WriteText(ui.OkStyle.Render("System prompt reloaded."))
		sink.WriteNewline()
		return true, ""
	}

	if strings.HasPrefix(input, "/delay ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/delay "))
		var d float64
		_, err := fmt.Sscanf(arg, "%f", &d)
		if err != nil {
			sink.WriteText(ui.InfoStyle.Render("Invalid delay value. Please use a number (e.g., /delay 1.5)"))
			sink.WriteNewline()
		} else {
			config.SetDelay(d)
			config.Save()
			sink.WriteText(fmt.Sprintf(ui.OkStyle.Render("Delay set to %.1f seconds"), d))
			sink.WriteNewline()
		}
		return true, ""
	}

	// Handle /temp command
	if input == "/temp" || strings.HasPrefix(input, "/temp ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/temp"))
		if arg == "" {
			cfg := config.Get()
			sink.WriteText(fmt.Sprintf(ui.InfoStyle.Render("Current temperature: %.2f"), cfg.Temperature))
			sink.WriteNewline()
			sink.WriteText(ui.InfoStyle.Render("Range: 0.0 - 2.0 (lower = more focused, higher = more creative)"))
			sink.WriteNewline()
		} else {
			var temp float64
			_, err := fmt.Sscanf(arg, "%f", &temp)
			if err != nil {
				sink.WriteText(ui.InfoStyle.Render(fmt.Sprintf("Current temperature: %.1f. Use /temp number (e.g., /temp 0.8) to change it.", config.Get().Temperature)))
				sink.WriteNewline()
			} else if temp < 0.0 || temp > 2.0 {
				sink.WriteText(ui.InfoStyle.Render("Temperature must be between 0.0 and 2.0"))
				sink.WriteNewline()
			} else {
				config.SetTemperature(temp)
				config.Save()
				sink.WriteText(fmt.Sprintf(ui.OkStyle.Render("Temperature set to %.2f"), temp))
				sink.WriteNewline()
				if currentTemperatureOmitted() {
					sink.WriteText(ui.TipStyle.Render("Warning: Temperature is not supported by the current model; preference saved. It will be used when a model that supports it is selected."))
					sink.WriteNewline()
				}
				sink.WriteStats(currentProviderLine(), "")
			}
		}
		return true, ""
	}

	// Handle /model command
	if input == "/model" || strings.HasPrefix(input, "/model ") {
		sink.WriteText(ui.InfoStyle.Render("Fetching available models..."))
		sink.WriteNewline()
		models, err := config.GetAvailableModelsForCurrentProvider()
		if err != nil {
			sink.WriteText(ui.InfoStyle.Render("Error fetching models: " + err.Error()))
			sink.WriteNewline()
			return true, ""
		}
		if len(models) == 0 {
			sink.WriteText(ui.InfoStyle.Render("No models available for current provider"))
			sink.WriteNewline()
			return true, ""
		}
		currentModel := config.Get().Model
		var modelOptions []string
		for _, m := range models {
			if m == currentModel {
				modelOptions = append(modelOptions, fmt.Sprintf("%s (current)", m))
			} else {
				modelOptions = append(modelOptions, m)
			}
		}
		idx := ui.SelectModelOption(sink, "Select model", modelOptions)
		if idx < 0 {
			sink.WriteText(ui.InfoStyle.Render("Model selection cancelled"))
			sink.WriteNewline()
			return true, ""
		}
		selectedModel := models[idx]
		saveToFile := sink.SelectOption("Save this model selection to config file?", []string{
			"No, use only for this session",
			"Yes, save permanently",
		}) == 1

		currentProvider := config.GetProvider()
		err = config.SwitchProviderAndModel(currentProvider, selectedModel, saveToFile)
		if err != nil {
			sink.WriteText(ui.InfoStyle.Render("Error switching model: " + err.Error()))
			sink.WriteNewline()
		} else {
			sink.WriteText(ui.OkStyle.Render("Model switched to: " + selectedModel))
			sink.WriteNewline()
			if saveToFile {
				sink.WriteText(ui.InfoStyle.Render("Model saved to config file"))
				sink.WriteNewline()
			}
		}
		updateSystemPrompt(s)
		if notice := toolImageNotice(s); notice != "" {
			sink.WriteText(ui.InfoStyle.Render(notice))
			sink.WriteNewline()
		}
		return true, ""
	}

	// Handle /reasoning command: the single entry point for thinking/reasoning.
	if input == "/reasoning" || strings.HasPrefix(input, "/reasoning ") {
		handleReasoningCommand(s)
		return true, ""
	}

	// Handle /provider command
	if input == "/provider" || strings.HasPrefix(input, "/provider ") {
		providers := config.GetAvailableProviders()
		if len(providers) == 0 {
			sink.WriteText(ui.InfoStyle.Render("No providers configured. Run 'fuckyourcli config' first."))
			sink.WriteNewline()
			return true, ""
		}
		currentProviderInfo := config.GetCurrentProviderInfo()
		var providerOptions []string
		for _, p := range providers {
			if p == currentProviderInfo {
				providerOptions = append(providerOptions, fmt.Sprintf("%s (current)", p))
			} else {
				providerOptions = append(providerOptions, p)
			}
		}
		idx := sink.SelectOption("Select provider", providerOptions)
		if idx < 0 {
			sink.WriteText(ui.InfoStyle.Render("Provider selection cancelled"))
			sink.WriteNewline()
			return true, ""
		}
		selectedProviderStr := providers[idx]
		providerName := selectedProviderStr
		if bracketIdx := strings.Index(selectedProviderStr, " ("); bracketIdx != -1 {
			providerName = selectedProviderStr[:bracketIdx]
		}
		providerType, model, err := config.ParseProviderSelection(selectedProviderStr)
		if err != nil {
			sink.WriteText(ui.InfoStyle.Render("Error parsing provider: " + err.Error()))
			sink.WriteNewline()
			return true, ""
		}
		if providerType == config.ProviderOpenAIResponsesCompatible {
			providerName = strings.TrimSuffix(providerName, " [Responses]")
		} else if providerType == config.ProviderAnthropic {
			providerName = strings.TrimSuffix(providerName, " [Anthropic]")
		}
		finalModel := model
		chooseModel := model == ""
		if !chooseModel {
			choice := sink.SelectOption(fmt.Sprintf("Use current model for %s?", providerName), []string{
				"Yes, use " + model,
				"No, choose another model",
			})
			if choice < 0 {
				sinkLine(sink, ui.InfoStyle.Render("Provider selection cancelled"))
				return true, ""
			}
			chooseModel = choice == 1
		}
		if chooseModel {
			sinkLine(sink, ui.InfoStyle.Render(fmt.Sprintf("Fetching available models for %s...", selectedProviderStr)))
			models, fetchErr := config.GetAvailableModelsForProvider(providerType, providerName, model)
			if fetchErr != nil || len(models) == 0 {
				if model == "" {
					sinkLine(sink, ui.InfoStyle.Render("No models available for this provider"))
					return true, ""
				}
				sinkLine(sink, ui.InfoStyle.Render("Could not fetch models for this provider. Using current model."))
				models = []string{model}
			}
			modelIdx := ui.SelectModelOption(sink, fmt.Sprintf("Select model for %s", selectedProviderStr), models)
			if modelIdx < 0 {
				sinkLine(sink, ui.InfoStyle.Render("Model selection cancelled"))
				return true, ""
			}
			finalModel = models[modelIdx]
		}
		saveToFile := sink.SelectOption("Save this provider and model selection to config file?", []string{
			"No, use only for this session",
			"Yes, save permanently",
		}) == 1
		if providerType == config.ProviderOpenAICompatible {
			err = config.SwitchToOpenAIProvider(providerName, finalModel, saveToFile)
		} else if providerType == config.ProviderOpenAIResponsesCompatible {
			err = config.SwitchToOpenAIResponsesProvider(providerName, finalModel, saveToFile)
		} else if providerType == config.ProviderAnthropic {
			err = config.SwitchToAnthropicProvider(providerName, finalModel, saveToFile)
		} else {
			err = config.SwitchProviderAndModel(providerType, finalModel, saveToFile)
		}
		if err != nil {
			sink.WriteText(ui.InfoStyle.Render("Error switching provider: " + err.Error()))
			sink.WriteNewline()
		} else {
			sink.WriteText(fmt.Sprintf(ui.OkStyle.Render("Switched to provider: %s, model: %s"), selectedProviderStr, finalModel))
			sink.WriteNewline()
			if saveToFile {
				sink.WriteText(ui.InfoStyle.Render("Configuration saved to config file"))
				sink.WriteNewline()
			}
		}
		updateSystemPrompt(s)
		if notice := toolImageNotice(s); notice != "" {
			sink.WriteText(ui.InfoStyle.Render(notice))
			sink.WriteNewline()
		}
		return true, ""
	}

	// Handle /mode command
	if input == "/mode" || strings.HasPrefix(input, "/mode ") {
		mName := strings.TrimSpace(strings.TrimPrefix(input, "/mode "))
		if mName == "" || mName == "/mode" {
			sink.WriteText(ui.TitleStyle.Render("Available Modes:"))
			sink.WriteNewline()
			for _, m := range modes.Modes {
				if !s.webSearchEnabled() && m.RequiresWebSearch() {
					continue
				}
				sink.WriteText(fmt.Sprintf("  - %s: %s (%s)", ui.OkStyle.Render(m.Name), m.Description, ui.InfoStyle.Render(m.Prefix)))
				sink.WriteNewline()
			}
			sink.WriteText(fmt.Sprintf("  - %s: Back to normal chat", ui.OkStyle.Render("chat")))
			sink.WriteNewline()
			return true, ""
		}
		if mName == "chat" || mName == "default" {
			s.CurrentMode = nil
			s.persistentModeInjected = ""
			updateSystemPrompt(s)
			sink.WriteText(ui.OkStyle.Render("Switched to Chat mode"))
			sink.WriteNewline()
		} else {
			m := modes.GetMode(mName)
			if m != nil {
				if !s.webSearchEnabled() && m.RequiresWebSearch() {
					sink.WriteText(ui.InfoStyle.Render("Deep Research mode is disabled while airplane mode is enabled"))
					sink.WriteNewline()
					return true, ""
				}
				// A (re)selection clears the injection flag so the mode's
				// instructions are appended again, exactly once.
				s.persistentModeInjected = ""
				s.CurrentMode = m
				updateSystemPrompt(s)
				sink.WriteText(ui.OkStyle.Render("Switched to " + m.Name + " mode"))
				sink.WriteNewline()
			} else {
				sink.WriteText(ui.InfoStyle.Render("Unknown mode: " + mName))
				sink.WriteNewline()
			}
		}
		return true, ""
	}

	// Handle /skills command
	if input == "/skills" || strings.HasPrefix(input, "/skills ") {
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(input, "/skills")))
		if len(parts) == 0 {
			sink.WriteText(ui.InfoStyle.Render("Usage: /skills <name> <prompt>"))
			sink.WriteNewline()
			return true, ""
		}
		skillName := parts[0]
		skillPath := tools.FindSkillPathIn(s.workspace(), skillName)
		if skillPath == "" {
			sink.WriteText(ui.InfoStyle.Render("Unknown skill: " + skillName))
			sink.WriteNewline()
			return true, ""
		}
		data, err := os.ReadFile(skillPath)
		if err != nil {
			sink.WriteText(ui.InfoStyle.Render("Failed to read skill: " + err.Error()))
			sink.WriteNewline()
			return true, ""
		}
		modifiedInput := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(input, "/skills")), skillName))
		if modifiedInput == "" {
			sink.WriteText(ui.InfoStyle.Render("Add a prompt after the skill name."))
			sink.WriteNewline()
			return true, ""
		}
		modifiedInput = "[SKILL: " + skillName + "]\n" + string(data) + "\n\n" + modifiedInput
		return true, modifiedInput
	}

	// Handle /init command: generation of the repository AGENTS.md. The task
	// goes to the model (see init_cmd.go), never to the editor here, so the
	// documented commands are the ones actually verified in this repository.
	if input == "/init" || strings.HasPrefix(input, "/init ") {
		return handleInitCommand(s, input)
	}
	// Handle /sessions command
	if input == "/sessions" || strings.HasPrefix(input, "/sessions ") {
		handleSessionsCommand(s, input)
		return true, ""
	}

	return false, ""
}

// liveSlashCommands are the commands a running generation does not block: they
// only read or change display and behaviour state and never touch the
// conversation. Everything else has to wait for the model — a prompt, the modes
// (they inject their instructions and tool policy into the turn in flight), and
// every command that rewrites the history, the system prompt or the provider.
var liveSlashCommands = []string{
	"/help", "/auto", "/airplane", "/sub", "/tools", "/hooks", "/sysprompt",
	"/delay", "/temp", "/reasoning", "/usage",
}

// liveSlashCommand reports whether input is a command allowed while the model is
// generating. Matching is on the command word alone, so arguments ("/temp 0.8")
// are fine and a prompt that merely starts with a path ("/usr/bin/...") is not.
func liveSlashCommand(input string) bool {
	name := strings.TrimSpace(input)
	if i := strings.IndexAny(name, " \t\n"); i >= 0 {
		name = name[:i]
	}
	for _, cmd := range liveSlashCommands {
		if name == cmd {
			return true
		}
	}
	return false
}

// runLiveCommand runs a command beside a running generation and returns what it
// printed instead of committing it to the transcript: the chat shows it as a
// transient notice above the input (see chatModel.notice). Live commands are
// serialized because the redirection is session state, and the capture only
// exists while one of them runs.
func (s *ChatSession) runLiveCommand(input string) string {
	s.liveCommandMu.Lock()
	defer s.liveCommandMu.Unlock()
	capture := ui.NewCaptureSink(s.Sink)
	s.liveCommandSink = capture
	defer func() { s.liveCommandSink = nil }()
	if handled, _ := handleSlashCommand(input, s); !handled {
		return ""
	}
	return capture.Text()
}

// handleSessionsCommand opens the interactive session manager. It is the single
// entry point for /sessions: arguments are ignored (the old typed
// "/sessions <action>" form is gone) and everything happens through the menu.
func handleSessionsCommand(s *ChatSession, input string) {
	runSessionMenu(s, sinkSessionOps(s))
}

// applySessionChoice switches the active chat; an empty id starts a new one.
func applySessionChoice(s *ChatSession, id string) {
	sink := s.commandSink()
	if id == "" {
		if err := startNewChat(s); err != nil {
			sink.WriteText(ui.InfoStyle.Render("Cannot start a new chat: " + err.Error()))
			sink.WriteNewline()
			return
		}
		sink.WriteText(ui.OkStyle.Render("Started a new chat"))
		sink.WriteNewline()
		return
	}
	if err := switchChatSession(s, id); err != nil {
		sink.WriteText(ui.InfoStyle.Render("Cannot switch session: " + err.Error()))
		sink.WriteNewline()
		return
	}
	sink.WriteText(ui.OkStyle.Render("Switched to session " + id))
	sink.WriteNewline()
}

// reasoningModeOption is one row of the /reasoning mode menu. The key is what
// reasoningSubmenu dispatches on, so the list can hide entries the model cannot
// express without shifting the meaning of the other rows.
type reasoningModeOption struct {
	key   string
	label string
}

// reasoningModeOptions builds the wizard from the resolved profile: only the
// modes the model can actually express are listed, each with its concrete wire
// effect, and the raw editors move behind "Advanced".
func reasoningModeOptions(p reasoning.Profile) []reasoningModeOption {
	choices := reasoning.Choices(p)
	opts := make([]reasoningModeOption, 0, len(choices)+2)
	for _, c := range choices {
		opts = append(opts, reasoningModeOption{key: c.Key, label: c.Label})
	}
	return append(opts,
		reasoningModeOption{key: "advanced", label: "Advanced — custom effort, extra body, wire fields"},
		reasoningModeOption{key: "cancel", label: "Cancel"},
	)
}

// reasoningAdvancedOptions are the raw escape hatches, kept out of the normal
// path so a user who just wants to pick a depth never sees a wire path.
func reasoningAdvancedOptions() []reasoningModeOption {
	return []reasoningModeOption{
		{key: "effort_custom", label: "Custom effort — type a label"},
		{key: "extra", label: "Extra body — set a custom field (escape hatch)"},
		{key: "provider", label: "Wire fields — where the thinking fields go (per provider)"},
		{key: "back", label: "← Back"},
	}
}

// defaultBudgets backs the token-budget menu when the catalog documents no
// range for the model.
var defaultBudgets = []int{1024, 2048, 4096, 8192, 16384, 32768}

// budgetChoices lists the token budgets, preferring the model's documented
// range (already capped to its output limit).
func budgetChoices(caps models.Capabilities) []int {
	if !caps.HasBudget {
		return defaultBudgets
	}
	min, max := caps.BudgetMin, caps.BudgetMax
	if min < 0 { // -1 = "dynamic": not a lower bound
		min = 0
	}
	out := make([]int, 0, len(defaultBudgets)+1)
	if min > 0 {
		out = append(out, min)
	}
	for _, b := range defaultBudgets {
		if b > min && (max == 0 || b <= max) {
			out = append(out, b)
		}
	}
	if max > 0 && (len(out) == 0 || out[len(out)-1] != max) {
		out = append(out, max)
	}
	if len(out) == 0 {
		return defaultBudgets
	}
	return out
}

// transportForProvider maps the active provider onto the wire protocol, so the
// transport table can be consulted outside the provider implementations.
func transportForProvider(pt config.ProviderType) reasoning.Transport {
	switch pt {
	case config.ProviderAnthropic:
		return reasoning.AnthropicMessages
	case config.ProviderGemini:
		return reasoning.GeminiGenerateContent
	case config.ProviderOpenAIResponsesCompatible, config.ProviderCodex:
		return reasoning.OpenAIResponses
	default:
		return reasoning.ChatCompletions
	}
}

// handleReasoningCommand implements /reasoning as an auto wizard: it resolves the
// active model's Profile (built-in table + catalog + optional per-provider
// override), shows what that profile can express and what is sent right now, and
// offers only the choices it can actually encode. The raw escape hatches live
// under "Advanced".
func handleReasoningCommand(s *ChatSession) {
	sink := s.commandSink()
	cfg := config.Get()
	baseURL, _ := config.ActiveProviderEndpoint()
	host := hostFromURL(baseURL)
	caps, inCatalog := reasoning.CatalogCapabilities(baseURL, cfg.Model)

	// The exact intent + override the providers resolve at request time, so the
	// preview is what the next turn will really send.
	intent, override := config.ActiveReasoning()
	profile, det := reasoning.Lookup(transportForProvider(cfg.Provider), host, cfg.Model, override)

	sink.WriteText(ui.InfoStyle.Render(fmt.Sprintf("Model: %s  ·  current reasoning: %s", cfg.Model, cfg.Reasoning.Label())))
	sink.WriteNewline()
	catalogNote := ""
	if inCatalog {
		catalogNote = "  ·  catalog ✓"
	}
	sink.WriteText(ui.InfoStyle.Render(fmt.Sprintf("detected: %s (%s)%s", familyLabel(det), det.Source, catalogNote)))
	sink.WriteNewline()
	if sum := profile.Summary(); sum != "" {
		sink.WriteText(ui.InfoStyle.Render("wire profile: " + sum))
		sink.WriteNewline()
	}
	if pv := reasoning.Preview(intent, profile); pv != "" {
		sink.WriteText(ui.OkStyle.Render("sends now: " + pv))
	} else {
		sink.WriteText(ui.TipStyle.Render("sends now: nothing (defensive — the server decides)"))
	}
	sink.WriteNewline()

	if !det.Confident {
		sink.WriteText(ui.TipStyle.Render("⚠ model not recognised: the default sends no thinking parameters (defensive)"))
		sink.WriteNewline()
	}
	if info := models.ReasoningInfo(cfg.Model); info != "" {
		sink.WriteText(ui.InfoStyle.Render(info))
		sink.WriteNewline()
	}
	// A per-provider override (Wire fields) replaces the auto-detected profile
	// entirely: show it so the user always sees what will be sent.
	if key := config.CurrentProviderKey(); key != "" {
		if pc, ok := config.ReasoningForProvider(key); ok {
			if summary := pc.Summary(); summary != "" {
				sink.WriteText(ui.InfoStyle.Render("provider override (" + key + "): " + summary))
				sink.WriteNewline()
			}
		}
	}
	if inCatalog && caps.Status != "" {
		sink.WriteText(ui.TipStyle.Render("⚠ catalog marks this model as " + caps.Status))
		sink.WriteNewline()
	}
	if inCatalog && caps.NoOptions {
		sink.WriteText(ui.TipStyle.Render("⚠ this model thinks but exposes no documented reasoning control"))
		sink.WriteNewline()
	}

	options := reasoningModeOptions(profile)
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o.label
	}

	keep := cfg.Reasoning.ExtraBody
	for {
		idx := sink.SelectOption("Reasoning mode", labels)
		if idx < 0 || idx >= len(options) || options[idx].key == "cancel" {
			return // Cancel (or dismissed)
		}

		if options[idx].key == "advanced" {
			rc, ok := reasoningAdvancedMenu(s, keep, caps)
			keep = config.Get().Reasoning.ExtraBody
			if !ok {
				continue // an escape hatch ran, or the user backed out
			}
			applyReasoningChoice(s, rc, profile)
			return
		}

		rc, back := reasoningSubmenu(s, options[idx].key, keep, caps)
		if back {
			// A submenu may have persisted its own changes (extra body, wire
			// fields); re-read so the next choice does not clobber them.
			keep = config.Get().Reasoning.ExtraBody
			continue
		}
		applyReasoningChoice(s, rc, profile)
		return
	}
}

// applyReasoningChoice persists a wizard selection and shows its concrete wire
// effect, so the user always sees what the next request will carry.
func applyReasoningChoice(s *ChatSession, rc config.ReasoningConfig, profile reasoning.Profile) {
	sink := s.commandSink()
	config.SetReasoning(rc)
	config.Save()
	sink.WriteText(ui.OkStyle.Render("Reasoning set to: " + rc.Label()))
	if pv := reasoning.Preview(rc.Intent(), profile); pv != "" {
		sink.WriteText(ui.InfoStyle.Render("  ·  sends " + pv))
	} else {
		sink.WriteText(ui.InfoStyle.Render("  ·  sends nothing"))
	}
	sink.WriteNewline()
}

// reasoningAdvancedMenu is the raw escape-hatch submenu. It returns ok=true with
// a ready-to-persist config only when the user picked something concrete (custom
// effort); the other rows persist their own edits and redraw the menu.
func reasoningAdvancedMenu(s *ChatSession, keep map[string]any, caps models.Capabilities) (config.ReasoningConfig, bool) {
	sink := s.commandSink()
	opts := reasoningAdvancedOptions()
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.label
	}
	for {
		idx := sink.SelectOption("Advanced reasoning options", labels)
		if idx < 0 || opts[idx].key == "back" {
			return config.ReasoningConfig{}, false
		}
		rc, back := reasoningSubmenu(s, opts[idx].key, keep, caps)
		if !back {
			return rc, true
		}
		keep = config.Get().Reasoning.ExtraBody
	}
}

// familyLabel renders the detected family for the header.
func familyLabel(det reasoning.Detection) string {
	if det.Family == reasoning.FamilyUnknown {
		return "unknown"
	}
	return string(det.Family)
}

// reasoningSubmenu resolves a selection from the /reasoning wizard. key is the
// chosen reasoningModeOption.key; caps are the catalog capabilities backing the
// budget list. Level keys arrive as "effort:<level>" and need no lookup: the
// graded scale is decided by the resolved profile (see reasoning.Levels).
//
// back asks the caller to redraw the mode menu: it is returned by the explicit
// "← Back" row and by any dismissal inside a submenu (Esc, or q/Ctrl+C in the
// selectors). A stray escape therefore never closes /reasoning; only Cancel or
// Esc on the mode menu itself does. The returned config is ready to be
// persisted.
func reasoningSubmenu(s *ChatSession, key string, keep map[string]any, caps models.Capabilities) (rc config.ReasoningConfig, back bool) {
	sink := s.commandSink()
	const backLabel = "← Back"
	if level, ok := strings.CutPrefix(key, "effort:"); ok {
		return config.ReasoningConfig{Mode: "effort", Effort: level, ExtraBody: keep}, false
	}
	switch key {
	case "default":
		return config.ReasoningConfig{Mode: "default", ExtraBody: keep}, false
	case "off":
		return config.ReasoningConfig{Mode: "off", ExtraBody: keep}, false
	case "adaptive":
		return config.ReasoningConfig{Mode: "adaptive", ExtraBody: keep}, false
	case "provider":
		// The wire-field editor persists its own changes; it never sets a mode,
		// so the caller only redraws the mode menu.
		reasoningProviderFieldsMenu(s, config.CurrentProviderKey())
		return config.ReasoningConfig{}, true
	case "effort_custom":
		line, ok := promptLine("Custom effort label ('back' to return; e.g. high, ultra): ")
		if !ok { // Esc / Ctrl+C: back to the mode menu, never out of /reasoning
			return config.ReasoningConfig{}, true
		}
		if isBack(line) {
			return config.ReasoningConfig{}, true
		}
		return config.ReasoningConfig{Mode: "effort", Effort: line, ExtraBody: keep}, false
	case "budget":
		// A selector over the documented budgets: typing a number was
		// error-prone and, more importantly, the text input is a heavier prompt.
		choices := budgetChoices(caps)
		labels := make([]string, 0, len(choices)+1)
		labels = append(labels, backLabel)
		for _, b := range choices {
			labels = append(labels, strconv.Itoa(b))
		}
		bi := sink.SelectOption("Thinking token budget", labels)
		if bi <= 0 { // row 0 is "← Back"; a dismissal is treated the same way
			return config.ReasoningConfig{}, true
		}
		n, err := strconv.Atoi(labels[bi])
		if err != nil {
			return config.ReasoningConfig{}, true
		}
		return config.ReasoningConfig{Mode: "budget", Budget: n, ExtraBody: keep}, false
	case "extra":
		line, ok := promptLine("Extra body as key=value ('back' to return; e.g. reasoning_effort=custom): ")
		if !ok { // Esc / Ctrl+C: back to the mode menu
			return config.ReasoningConfig{}, true
		}
		if isBack(line) {
			return config.ReasoningConfig{}, true
		}
		key, val, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			sink.WriteText(ui.InfoStyle.Render("Expected key=value"))
			sink.WriteNewline()
			return config.ReasoningConfig{}, true
		}
		eb := map[string]any{}
		for k, v := range keep {
			eb[k] = v
		}
		eb[key] = parseExtraValue(strings.TrimSpace(val))
		saved := config.Get().Reasoning
		saved.ExtraBody = eb
		config.SetReasoning(saved)
		config.Save()
		sink.WriteText(ui.OkStyle.Render("Extra body set: " + key + "=" + fmt.Sprint(eb[key])))
		sink.WriteNewline()
		return config.ReasoningConfig{}, true // back to the mode menu
	}
	return config.ReasoningConfig{}, true
}

// promptText reads a single line for the slash submenus. It defaults to the
// in-TUI prompt, which pauses the chat program while it runs (a bare nested
// tea.Program would fight the main one for stdin); a variable so tests can stub.
var promptText = ui.InputLine

// promptLine reads a single line; ok is false when the user cancels (Esc/Ctrl+C).
func promptLine(prompt string) (string, bool) {
	line, ok := promptText(prompt)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// isBack reports whether a typed line asks to return to the previous menu.
func isBack(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "back", "-", ":b":
		return true
	}
	return false
}

// hostFromURL extracts the host from a base URL, tolerating values without a
// scheme (e.g. "api.deepseek.com/v1").
func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// parseExtraValue interprets a typed value as JSON when possible (numbers,
// booleans, objects, arrays), falling back to the raw string.
func parseExtraValue(v string) any {
	var out any
	if err := json.Unmarshal([]byte(v), &out); err == nil && v != "" {
		return out
	}
	return v
}
