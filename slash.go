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

		s.Sink.WriteText(fmt.Sprintf("┌%s┬%s┐", hSep, dSep))
		s.Sink.WriteNewline()
		s.Sink.WriteText(fmt.Sprintf("│ %s │ %s │",
			padTo(ui.TitleStyle.Render("Command"), "Command", cmdW-2),
			padTo(ui.TitleStyle.Render("Description"), "Description", descW-2)))
		s.Sink.WriteNewline()
		s.Sink.WriteText(fmt.Sprintf("├%s┼%s┤", hSep, dSep))
		s.Sink.WriteNewline()
		for _, cmd := range allCmds {
			s.Sink.WriteText(fmt.Sprintf("│ %s │ %s │",
				padTo(ui.OkStyle.Render(cmd.Name), cmd.Name, cmdW-2),
				padTo(ui.InfoStyle.Render(cmd.Description), cmd.Description, descW-2)))
			s.Sink.WriteNewline()
		}
		s.Sink.WriteText(fmt.Sprintf("└%s┴%s┘", hSep, dSep))
		s.Sink.WriteNewline()
		s.Sink.WriteNewline()
		return true, ""
	}

	// Handle /auto command
	if input == "/auto" || strings.HasPrefix(input, "/auto ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/auto"))
		if arg == "on" {
			if !config.Get().NoAsk {
				toggleAutoMode(s.Sink)
			} else {
				s.Sink.WriteText(ui.OkStyle.Render("Auto mode enabled (session only)"))
				s.Sink.WriteNewline()
			}
		} else if arg == "off" {
			if config.Get().NoAsk {
				toggleAutoMode(s.Sink)
			} else {
				s.Sink.WriteText(ui.InfoStyle.Render("Auto mode disabled"))
				s.Sink.WriteNewline()
			}
		} else {
			toggleAutoMode(s.Sink)
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
			s.Sink.WriteText(ui.InfoStyle.Render("Usage: /airplane [on|off]"))
			s.Sink.WriteNewline()
			return true, ""
		}
		if !s.webSearchEnabled() && s.CurrentMode != nil && s.CurrentMode.RequiresWebSearch() {
			s.CurrentMode = nil
			s.persistentModeInjected = ""
		}
		updateSystemPrompt(s)
		if !s.webSearchEnabled() {
			s.Sink.WriteText(ui.InfoStyle.Render("Airplane mode enabled: web_search and /deep disabled"))
			s.Sink.WriteNewline()
		} else {
			s.Sink.WriteText(ui.OkStyle.Render("Airplane mode disabled: web_search and /deep enabled"))
			s.Sink.WriteNewline()
		}
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
		tools.ResetAnchorState()
		// The cleared context no longer carries the persistent mode's
		// instructions, and /clear returns to plain chat: drop both so the mode
		// does not linger without its instructions.
		s.persistentModeInjected = ""
		s.CurrentMode = nil
		updateSystemPrompt(s)
		s.Sink.WriteText(ui.OkStyle.Render("Context cleared"))
		s.Sink.WriteNewline()
		return true, ""
	}

	// Handle /compact command
	if input == "/compact" || strings.HasPrefix(input, "/compact ") {
		if len(s.ChatHistory) <= 1 {
			s.Sink.WriteText(ui.InfoStyle.Render("Nothing to compact"))
			s.Sink.WriteNewline()
			return true, ""
		}
		originalCount := len(s.ChatHistory)
		before, after, res, err := compactContext(s, s.Sink)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				sinkLine(s.Sink, ui.InfoStyle.Render("Compaction interrupted."))
			} else {
				sinkLine(s.Sink, ui.ErrorStyle.Render("✗ Error during compaction: "+err.Error()+"."+compactionFailureHint(config.Get(), false)))
			}
			return true, ""
		}

		if res.FellBack {
			sinkLine(s.Sink, ui.TipStyle.Render(compactionFallbackNotice(res)))
		}
		sinkLine(s.Sink, ui.OkStyle.Render(fmt.Sprintf("✓ Context compacted. History reduced from %d to %d messages (~%d → ~%d tokens, compactor: %s).", originalCount, len(s.ChatHistory), before, after, res.Model)))
		return true, ""
	}

	// Handle /sub command
	if input == "/sub" || strings.HasPrefix(input, "/sub ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/sub"))
		if arg == "" {
			if s.subagentsEnabled() {
				s.Sink.WriteText(ui.InfoStyle.Render("Sub-agent tools: enabled"))
				s.Sink.WriteNewline()
			} else {
				s.Sink.WriteText(ui.InfoStyle.Render("Sub-agent tools: disabled"))
				s.Sink.WriteNewline()
			}
		} else if arg == "off" {
			s.setToolEnabled("spawn_subagents", false)
			s.Sink.WriteText(ui.InfoStyle.Render("Sub-agent tools disabled"))
			s.Sink.WriteNewline()
		} else if arg == "on" {
			s.setToolEnabled("spawn_subagents", true)
			s.Sink.WriteText(ui.OkStyle.Render("Sub-agent tools enabled"))
			s.Sink.WriteNewline()
		} else {
			s.Sink.WriteText(ui.InfoStyle.Render("Usage: /sub on|off"))
			s.Sink.WriteNewline()
		}
		updateSystemPrompt(s)
		return true, ""
	}

	if input == "/tools" || strings.HasPrefix(input, "/tools ") {
		changed, toolMsg := config.ToggleTools(s.allToolNames())
		lspChanged := s.refreshLSPToolAvailability()
		gitChanged := s.refreshGitToolAvailability()
		if toolMsg != "" {
			s.Sink.WriteText(toolMsg)
			s.Sink.WriteNewline()
		}
		if !s.webSearchEnabled() && s.CurrentMode != nil && s.CurrentMode.RequiresWebSearch() {
			s.CurrentMode = nil
			s.persistentModeInjected = ""
		}
		if changed || lspChanged || gitChanged {
			updateSystemPrompt(s)
		}
		if notice := toolImageNotice(s); notice != "" {
			s.Sink.WriteText(ui.InfoStyle.Render(notice))
			s.Sink.WriteNewline()
		}
		return true, ""
	}

	if input == "/hooks" || strings.HasPrefix(input, "/hooks ") {
		if config.EditHooks() {
			s.Sink.WriteText(ui.OkStyle.Render("Tool hooks updated."))
		} else {
			s.Sink.WriteText(ui.InfoStyle.Render("No changes made."))
		}
		s.Sink.WriteNewline()
		return true, ""
	}

	if input == "/sysprompt" || strings.HasPrefix(input, "/sysprompt ") {
		config.EditSystemPrompt(func(line string) {
			s.Sink.WriteText(line)
			s.Sink.WriteNewline()
		})
		updateSystemPrompt(s)
		s.Sink.WriteText(ui.OkStyle.Render("System prompt reloaded."))
		s.Sink.WriteNewline()
		return true, ""
	}

	if strings.HasPrefix(input, "/delay ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/delay "))
		var d float64
		_, err := fmt.Sscanf(arg, "%f", &d)
		if err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Invalid delay value. Please use a number (e.g., /delay 1.5)"))
			s.Sink.WriteNewline()
		} else {
			config.SetDelay(d)
			config.Save()
			s.Sink.WriteText(fmt.Sprintf(ui.OkStyle.Render("Delay set to %.1f seconds"), d))
			s.Sink.WriteNewline()
		}
		return true, ""
	}

	// Handle /temp command
	if input == "/temp" || strings.HasPrefix(input, "/temp ") {
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/temp"))
		if arg == "" {
			cfg := config.Get()
			s.Sink.WriteText(fmt.Sprintf(ui.InfoStyle.Render("Current temperature: %.2f"), cfg.Temperature))
			s.Sink.WriteNewline()
			s.Sink.WriteText(ui.InfoStyle.Render("Range: 0.0 - 2.0 (lower = more focused, higher = more creative)"))
			s.Sink.WriteNewline()
		} else {
			var temp float64
			_, err := fmt.Sscanf(arg, "%f", &temp)
			if err != nil {
				s.Sink.WriteText(ui.InfoStyle.Render(fmt.Sprintf("Current temperature: %.1f. Use /temp number (e.g., /temp 0.8) to change it.", config.Get().Temperature)))
				s.Sink.WriteNewline()
			} else if temp < 0.0 || temp > 2.0 {
				s.Sink.WriteText(ui.InfoStyle.Render("Temperature must be between 0.0 and 2.0"))
				s.Sink.WriteNewline()
			} else {
				config.SetTemperature(temp)
				config.Save()
				s.Sink.WriteText(fmt.Sprintf(ui.OkStyle.Render("Temperature set to %.2f"), temp))
				s.Sink.WriteNewline()
				if currentTemperatureOmitted() {
					s.Sink.WriteText(ui.TipStyle.Render("Warning: Temperature is not supported by the current model; preference saved. It will be used when a model that supports it is selected."))
					s.Sink.WriteNewline()
				}
				s.Sink.WriteStats(currentProviderLine(), "")
			}
		}
		return true, ""
	}

	// Handle /model command
	if input == "/model" || strings.HasPrefix(input, "/model ") {
		s.Sink.WriteText(ui.InfoStyle.Render("Fetching available models..."))
		s.Sink.WriteNewline()
		models, err := config.GetAvailableModelsForCurrentProvider()
		if err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Error fetching models: " + err.Error()))
			s.Sink.WriteNewline()
			return true, ""
		}
		if len(models) == 0 {
			s.Sink.WriteText(ui.InfoStyle.Render("No models available for current provider"))
			s.Sink.WriteNewline()
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
		idx := s.Sink.SelectOption("Select model", modelOptions)
		if idx < 0 {
			s.Sink.WriteText(ui.InfoStyle.Render("Model selection cancelled"))
			s.Sink.WriteNewline()
			return true, ""
		}
		selectedModel := models[idx]
		saveToFile := s.Sink.SelectOption("Save this model selection to config file?", []string{
			"No, use only for this session",
			"Yes, save permanently",
		}) == 1

		currentProvider := config.GetProvider()
		err = config.SwitchProviderAndModel(currentProvider, selectedModel, saveToFile)
		if err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Error switching model: " + err.Error()))
			s.Sink.WriteNewline()
		} else {
			s.Sink.WriteText(ui.OkStyle.Render("Model switched to: " + selectedModel))
			s.Sink.WriteNewline()
			if saveToFile {
				s.Sink.WriteText(ui.InfoStyle.Render("Model saved to config file"))
				s.Sink.WriteNewline()
			}
		}
		updateSystemPrompt(s)
		if notice := toolImageNotice(s); notice != "" {
			s.Sink.WriteText(ui.InfoStyle.Render(notice))
			s.Sink.WriteNewline()
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
			s.Sink.WriteText(ui.InfoStyle.Render("No providers configured. Run 'fuckyourcli config' first."))
			s.Sink.WriteNewline()
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
		idx := s.Sink.SelectOption("Select provider", providerOptions)
		if idx < 0 {
			s.Sink.WriteText(ui.InfoStyle.Render("Provider selection cancelled"))
			s.Sink.WriteNewline()
			return true, ""
		}
		selectedProviderStr := providers[idx]
		providerName := selectedProviderStr
		if bracketIdx := strings.Index(selectedProviderStr, " ("); bracketIdx != -1 {
			providerName = selectedProviderStr[:bracketIdx]
		}
		providerType, model, err := config.ParseProviderSelection(selectedProviderStr)
		if err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Error parsing provider: " + err.Error()))
			s.Sink.WriteNewline()
			return true, ""
		}
		oldProvider := config.GetProvider()
		oldModel := config.Get().Model
		if providerType == config.ProviderOpenAICompatible {
			config.SwitchToOpenAIProvider(providerName, model, false)
		} else if providerType == config.ProviderOpenAIResponsesCompatible {
			providerName = strings.TrimSuffix(providerName, " [Responses]")
			config.SwitchToOpenAIResponsesProvider(providerName, model, false)
		} else if providerType == config.ProviderAnthropic {
			providerName = strings.TrimSuffix(providerName, " [Anthropic]")
			config.SwitchToAnthropicProvider(providerName, model, false)
		} else {
			config.SwitchProviderAndModel(providerType, model, false)
		}
		s.Sink.WriteText(fmt.Sprintf(ui.InfoStyle.Render("Fetching available models for %s..."), selectedProviderStr))
		s.Sink.WriteNewline()
		models, err := config.GetAvailableModelsForCurrentProvider()
		if err != nil || len(models) == 0 {
			s.Sink.WriteText(ui.InfoStyle.Render("Could not fetch models for this provider. Using default."))
			s.Sink.WriteNewline()
			models = []string{model}
		}
		modelIdx := s.Sink.SelectOption(fmt.Sprintf("Select model for %s", selectedProviderStr), models)
		if modelIdx < 0 {
			config.SwitchProviderAndModel(oldProvider, oldModel, false)
			s.Sink.WriteText(ui.InfoStyle.Render("Model selection cancelled"))
			s.Sink.WriteNewline()
			return true, ""
		}
		finalModel := models[modelIdx]
		config.SwitchProviderAndModel(oldProvider, oldModel, false)
		saveToFile := s.Sink.SelectOption("Save this provider and model selection to config file?", []string{
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
			s.Sink.WriteText(ui.InfoStyle.Render("Error switching provider: " + err.Error()))
			s.Sink.WriteNewline()
		} else {
			s.Sink.WriteText(fmt.Sprintf(ui.OkStyle.Render("Switched to provider: %s, model: %s"), selectedProviderStr, finalModel))
			s.Sink.WriteNewline()
			if saveToFile {
				s.Sink.WriteText(ui.InfoStyle.Render("Configuration saved to config file"))
				s.Sink.WriteNewline()
			}
		}
		updateSystemPrompt(s)
		if notice := toolImageNotice(s); notice != "" {
			s.Sink.WriteText(ui.InfoStyle.Render(notice))
			s.Sink.WriteNewline()
		}
		return true, ""
	}

	// Handle /mode command
	if input == "/mode" || strings.HasPrefix(input, "/mode ") {
		mName := strings.TrimSpace(strings.TrimPrefix(input, "/mode "))
		if mName == "" || mName == "/mode" {
			s.Sink.WriteText(ui.TitleStyle.Render("Available Modes:"))
			s.Sink.WriteNewline()
			for _, m := range modes.Modes {
				if !s.webSearchEnabled() && m.RequiresWebSearch() {
					continue
				}
				s.Sink.WriteText(fmt.Sprintf("  - %s: %s (%s)", ui.OkStyle.Render(m.Name), m.Description, ui.InfoStyle.Render(m.Prefix)))
				s.Sink.WriteNewline()
			}
			s.Sink.WriteText(fmt.Sprintf("  - %s: Back to normal chat", ui.OkStyle.Render("chat")))
			s.Sink.WriteNewline()
			return true, ""
		}
		if mName == "chat" || mName == "default" {
			s.CurrentMode = nil
			s.persistentModeInjected = ""
			updateSystemPrompt(s)
			s.Sink.WriteText(ui.OkStyle.Render("Switched to Chat mode"))
			s.Sink.WriteNewline()
		} else {
			m := modes.GetMode(mName)
			if m != nil {
				if !s.webSearchEnabled() && m.RequiresWebSearch() {
					s.Sink.WriteText(ui.InfoStyle.Render("Deep Research mode is disabled while airplane mode is enabled"))
					s.Sink.WriteNewline()
					return true, ""
				}
				// A (re)selection clears the injection flag so the mode's
				// instructions are appended again, exactly once.
				s.persistentModeInjected = ""
				s.CurrentMode = m
				updateSystemPrompt(s)
				s.Sink.WriteText(ui.OkStyle.Render("Switched to " + m.Name + " mode"))
				s.Sink.WriteNewline()
			} else {
				s.Sink.WriteText(ui.InfoStyle.Render("Unknown mode: " + mName))
				s.Sink.WriteNewline()
			}
		}
		return true, ""
	}

	// Handle /skills command
	if input == "/skills" || strings.HasPrefix(input, "/skills ") {
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(input, "/skills")))
		if len(parts) == 0 {
			s.Sink.WriteText(ui.InfoStyle.Render("Usage: /skills <name> <prompt>"))
			s.Sink.WriteNewline()
			return true, ""
		}
		skillName := parts[0]
		skillPath := tools.FindSkillPath(skillName, s.RestrictedRoot)
		if skillPath == "" {
			s.Sink.WriteText(ui.InfoStyle.Render("Unknown skill: " + skillName))
			s.Sink.WriteNewline()
			return true, ""
		}
		data, err := os.ReadFile(skillPath)
		if err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Failed to read skill: " + err.Error()))
			s.Sink.WriteNewline()
			return true, ""
		}
		modifiedInput := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(input, "/skills")), skillName))
		if modifiedInput == "" {
			s.Sink.WriteText(ui.InfoStyle.Render("Add a prompt after the skill name."))
			s.Sink.WriteNewline()
			return true, ""
		}
		modifiedInput = "[SKILL: " + skillName + "]\n" + string(data) + "\n\n" + modifiedInput
		return true, modifiedInput
	}

	// Handle /sessions command
	if input == "/sessions" || strings.HasPrefix(input, "/sessions ") {
		handleSessionsCommand(s, input)
		return true, ""
	}

	return false, ""
}

// handleSessionsCommand opens the interactive session manager. It is the single
// entry point for /sessions: arguments are ignored (the old typed
// "/sessions <action>" form is gone) and everything happens through the menu.
func handleSessionsCommand(s *ChatSession, input string) {
	runSessionMenu(s, sinkSessionOps(s))
}

// applySessionChoice switches the active chat; an empty id starts a new one.
func applySessionChoice(s *ChatSession, id string) {
	if id == "" {
		if err := startNewChat(s); err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Cannot start a new chat: " + err.Error()))
			s.Sink.WriteNewline()
			return
		}
		s.Sink.WriteText(ui.OkStyle.Render("Started a new chat"))
		s.Sink.WriteNewline()
		return
	}
	if err := switchChatSession(s, id); err != nil {
		s.Sink.WriteText(ui.InfoStyle.Render("Cannot switch session: " + err.Error()))
		s.Sink.WriteNewline()
		return
	}
	s.Sink.WriteText(ui.OkStyle.Render("Switched to session " + id))
	s.Sink.WriteNewline()
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
	cfg := config.Get()
	baseURL, _ := config.ActiveProviderEndpoint()
	host := hostFromURL(baseURL)
	caps, inCatalog := reasoning.CatalogCapabilities(baseURL, cfg.Model)

	// The exact intent + override the providers resolve at request time, so the
	// preview is what the next turn will really send.
	intent, override := config.ActiveReasoning()
	profile, det := reasoning.Lookup(transportForProvider(cfg.Provider), host, cfg.Model, override)

	s.Sink.WriteText(ui.InfoStyle.Render(fmt.Sprintf("Model: %s  ·  current reasoning: %s", cfg.Model, cfg.Reasoning.Label())))
	s.Sink.WriteNewline()
	catalogNote := ""
	if inCatalog {
		catalogNote = "  ·  catalog ✓"
	}
	s.Sink.WriteText(ui.InfoStyle.Render(fmt.Sprintf("detected: %s (%s)%s", familyLabel(det), det.Source, catalogNote)))
	s.Sink.WriteNewline()
	if sum := profile.Summary(); sum != "" {
		s.Sink.WriteText(ui.InfoStyle.Render("wire profile: " + sum))
		s.Sink.WriteNewline()
	}
	if pv := reasoning.Preview(intent, profile); pv != "" {
		s.Sink.WriteText(ui.OkStyle.Render("sends now: " + pv))
	} else {
		s.Sink.WriteText(ui.TipStyle.Render("sends now: nothing (defensive — the server decides)"))
	}
	s.Sink.WriteNewline()

	if !det.Confident {
		s.Sink.WriteText(ui.TipStyle.Render("⚠ model not recognised: the default sends no thinking parameters (defensive)"))
		s.Sink.WriteNewline()
	}
	if info := models.ReasoningInfo(cfg.Model); info != "" {
		s.Sink.WriteText(ui.InfoStyle.Render(info))
		s.Sink.WriteNewline()
	}
	// A per-provider override (Wire fields) replaces the auto-detected profile
	// entirely: show it so the user always sees what will be sent.
	if key := config.CurrentProviderKey(); key != "" {
		if pc, ok := config.ReasoningForProvider(key); ok {
			if summary := pc.Summary(); summary != "" {
				s.Sink.WriteText(ui.InfoStyle.Render("provider override (" + key + "): " + summary))
				s.Sink.WriteNewline()
			}
		}
	}
	if inCatalog && caps.Status != "" {
		s.Sink.WriteText(ui.TipStyle.Render("⚠ catalog marks this model as " + caps.Status))
		s.Sink.WriteNewline()
	}
	if inCatalog && caps.NoOptions {
		s.Sink.WriteText(ui.TipStyle.Render("⚠ this model thinks but exposes no documented reasoning control"))
		s.Sink.WriteNewline()
	}

	options := reasoningModeOptions(profile)
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o.label
	}

	keep := cfg.Reasoning.ExtraBody
	for {
		idx := s.Sink.SelectOption("Reasoning mode", labels)
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
	config.SetReasoning(rc)
	config.Save()
	s.Sink.WriteText(ui.OkStyle.Render("Reasoning set to: " + rc.Label()))
	if pv := reasoning.Preview(rc.Intent(), profile); pv != "" {
		s.Sink.WriteText(ui.InfoStyle.Render("  ·  sends " + pv))
	} else {
		s.Sink.WriteText(ui.InfoStyle.Render("  ·  sends nothing"))
	}
	s.Sink.WriteNewline()
}

// reasoningAdvancedMenu is the raw escape-hatch submenu. It returns ok=true with
// a ready-to-persist config only when the user picked something concrete (custom
// effort); the other rows persist their own edits and redraw the menu.
func reasoningAdvancedMenu(s *ChatSession, keep map[string]any, caps models.Capabilities) (config.ReasoningConfig, bool) {
	opts := reasoningAdvancedOptions()
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.label
	}
	for {
		idx := s.Sink.SelectOption("Advanced reasoning options", labels)
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
		bi := s.Sink.SelectOption("Thinking token budget", labels)
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
			s.Sink.WriteText(ui.InfoStyle.Render("Expected key=value"))
			s.Sink.WriteNewline()
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
		s.Sink.WriteText(ui.OkStyle.Render("Extra body set: " + key + "=" + fmt.Sprint(eb[key])))
		s.Sink.WriteNewline()
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
