// Package main — streaming logic: routes to the active provider and handles the chat/tool loop.
package main

import (
	"context"
	"fmt"
	"fuckyourcli/agui"
	"fuckyourcli/config"
	"fuckyourcli/logger"
	"fuckyourcli/providers"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"fuckyourcli/ui"
	"strings"
)

// streamOptions controls per-provider behaviour inside the shared loop.
type streamOptions struct {
	includeReasoningSignature bool
	includeToolCallID         bool
}

// streamChat dispatches to the correct provider's streaming function.
func streamChat(s *ChatSession) {
	cfg := config.Get()
	switch cfg.Provider {
	case config.ProviderGemini:
		// Gemini 3 attaches a thoughtSignature to keep reasoning state across
		// turns; it must be carried in history or the model can 400 on tool calls.
		streamProviderChat(s, streamOptions{includeReasoningSignature: true, includeToolCallID: false})
	case config.ProviderOpenAICompatible:
		streamProviderChat(s, streamOptions{includeReasoningSignature: false, includeToolCallID: true})
	case config.ProviderOpenAIResponsesCompatible:
		streamProviderChat(s, streamOptions{includeReasoningSignature: false, includeToolCallID: true})
	case config.ProviderAnthropic:
		streamProviderChat(s, streamOptions{includeReasoningSignature: true, includeToolCallID: true})
	case config.ProviderCodex:
		streamProviderChat(s, streamOptions{includeReasoningSignature: false, includeToolCallID: true})
	}
}

// streamProviderChat is the single, shared streaming/tool loop used by every provider.
func streamProviderChat(s *ChatSession, opts streamOptions) {
	cfg := config.Get()
	sink := chatSink(s)

	provider, err := buildProvider(cfg, s)
	if err != nil {
		sinkLine(sink, ui.InfoStyle.Render(err.Error()+". Please run 'fuckyourcli config' to configure."))
		return
	}

	streamProviderLoop(s, opts, cfg, provider, getCompactionResponse)
}

func streamProviderLoop(s *ChatSession, opts streamOptions, cfg config.Config, provider types.Provider, respond compactionResponder) {
	provider = providers.WithLogging(provider)
	sink := chatSink(s)
	var lastResult *types.StreamResult
	// A single cancellable context spans the whole turn — the provider call and
	// the tool calls it produces — so ctrl+c in the TUI (or a client disconnect
	// in the AG-UI server) aborts both. runContext() returns this context, so a
	// tool that is running is stopped with the turn (see exec_tool.go).
	ctx := newGenerationContext(s)
	defer finishGeneration(s)
	const maxCompactionsPerTurn = 2
	attempts := 0
	firstRequest := true
	for {
		if ctx.Err() != nil {
			return
		}
		// This boundary is reached only before sampling or after a complete
		// tool batch; interrupted AG-UI calls exit below without reaching it.
		if (firstRequest || cfg.CompactionMidTurn) && attempts < maxCompactionsPerTurn {
			if maybeAutoCompactWith(ctx, s, sink, cfg.CompactionMidTurn, respond) {
				attempts++
			}
		}
		firstRequest = false
		if ctx.Err() != nil {
			return
		}
		restoreModeInstructions(s, cfg.Provider)
		applyDelay()
		if ctx.Err() != nil {
			return
		}
		sink.Reset()

		// Bound context growth: only the most recent screenshots stay attached.
		messages := pruneScreenshotImages(s.ChatHistory, maxKeptScreenshots)
		result, err := provider.StreamChat(ctx, messages, s.getToolsForAI(), sink)

		if err != nil {
			sinkError(sink, err)
			return
		}

		appendAssistantStreamResult(s, result, opts.includeReasoningSignature, cfg.Model)

		if result.Interrupted {
			sinkLine(sink, ui.InfoStyle.Render("[generation interrupted — saved to context]"))
			return
		}

		// A truncated turn (Anthropic max_tokens / context window) must not look
		// like a completed one: tell the user the reply is incomplete.
		if responseTruncated(result.FinishReason) {
			sinkLine(sink, ui.TipStyle.Render("[truncated response: stop_reason="+result.FinishReason+"]"))
		}

		s.LastCtx = result.PromptTokens
		lastResult = result

		if result.FinishReason != "tool_calls" && len(result.ToolCalls) == 0 {
			break
		}

		handleToolCalls(s, sink, result.ToolCalls, opts.includeToolCallID)
		if agui.RunInterrupted(s.runContext()) != nil {
			// The run was suspended for a standard AG-UI interrupt: the turn ends
			// here and the run finishes with the interrupt outcome, so no stats
			// are emitted and no further provider call happens.
			return
		}
		if ctx.Err() != nil {
			// The turn was aborted while a tool was running (ctrl+c in the TUI, a
			// disconnect in the server): the command was killed (see exec_tool.go)
			// and the turn stops here instead of asking the provider again.
			sinkLine(sink, ui.InfoStyle.Render("[turn aborted — running command stopped]"))
			return
		}
		printProviderAndTokenStats(sink, cfg, result, s)
	}

	printProviderAndTokenStats(sink, cfg, lastResult, s)

	// Refresh the custom status-bar hook (if any). It runs independently of the
	// token stats (some providers report no prompt tokens) and never blocks the
	// turn: the command runs in the background.
	root := ""
	if s != nil {
		root = s.RestrictedRoot
	}
	refreshStatusHookOnTurn(sink, root)
}

func chatSink(s *ChatSession) types.OutputSink {
	if s != nil && s.Sink != nil {
		return s.Sink
	}
	return providers.NewSilentOutputSink()
}

func sinkLine(sink types.OutputSink, text string) {
	sink.WriteText(text)
	sink.WriteNewline()
}

func sinkBlankLine(sink types.OutputSink) {
	sink.WriteNewline()
}

func sinkError(sink types.OutputSink, err error) {
	sinkLine(sink, ui.InfoStyle.Render("Error: "+err.Error()))
}

func appendAssistantStreamResult(s *ChatSession, result *types.StreamResult, includeReasoningSignature bool, model string) {
	if result == nil || (result.Content == "" && result.ReasoningContent == "" && result.ReasoningItem == "" && len(result.ToolCalls) == 0) {
		return
	}
	msg := types.Message{Role: "assistant", Content: result.Content, ReasoningContent: result.ReasoningContent}
	if includeReasoningSignature {
		msg.ReasoningSignature = result.ReasoningSignature
	}
	// Encrypted reasoning state (Responses API) is replayed verbatim next turn;
	// it is only ever set by providers that requested it. The producing model is
	// recorded so a hot model switch drops a stale (model-bound) item instead of
	// replaying it to a model that cannot read it.
	msg.ReasoningItem = result.ReasoningItem
	if result.ReasoningItem != "" {
		msg.ReasoningModel = model
	}
	if len(result.ToolCalls) > 0 {
		msg.ToolCalls = make([]types.ToolCall, len(result.ToolCalls))
		copy(msg.ToolCalls, result.ToolCalls)
	}
	s.ChatHistory = append(s.ChatHistory, msg)
}

// errToolAborted is the result recorded for the tool calls of a batch that never
// ran because the turn was aborted (ctrl+c / client disconnect) while an earlier
// call of the same batch was executing.
const errToolAborted = "error: turn aborted before this tool ran"

// handleToolCalls executes the tool calls of one assistant turn.
func handleToolCalls(s *ChatSession, sink types.OutputSink, toolCalls []types.ToolCall, includeToolCallID bool) {
	handleToolCallsWithSink(s, sink, toolCalls, includeToolCallID, true)
}

// handleToolCallsWithSink executes the tool calls of one assistant turn.
//
// announce controls the TOOL_CALL_START/ARGS/END emission: a resumed run replays
// the tool calls an interrupted run left pending and, per the AG-UI interrupt
// rules, must not announce them again — it only reports their TOOL_CALL_RESULT.
func handleToolCallsWithSink(s *ChatSession, sink types.OutputSink, toolCalls []types.ToolCall, includeToolCallID, announce bool) {
	// Structured tool-event sinks (AG-UI) receive TOOL_CALL_START/ARGS/END/RESULT
	// instead of the unstructured WriteToolOutput used by the TUI.
	structured, hasStructured := sink.(types.ToolEventSink)
	runCtx := s.runContext()

	for i, tc := range toolCalls {
		if i > 0 {
			sinkBlankLine(sink)
		}

		lbl := tools.DescribeTool(tc.Function.Name, tc.Function.Arguments)
		spinnerLabel := strings.TrimSuffix(lbl.Present, "...")

		// AG-UI requires a stable toolCallId; some providers (e.g. Gemini in
		// certain paths) do not populate ToolCall.ID, so generate a synthetic one.
		// It is kept in the history as well: a resumed run replays the call and
		// must use the very same id, because the interrupt is bound to it.
		callID := tc.ID
		if hasStructured && callID == "" {
			callID = agui.NewID("call")
			assignToolCallID(s, i, callID)
		}
		if hasStructured && announce {
			structured.ToolCallStarted(callID, tc.Function.Name, tc.Function.Arguments)
		}

		// If spawn_subagents, set up the live panel
		if tc.Function.Name == "spawn_subagents" {
			panel := ui.NewSubagentPanel()
			sink.StartSubagentPanel(panel)
			tools.SubagentBufferProvider = panel.AddAgent
		}
		// Bind the call to the context: an interaction raised inside the tool
		// becomes a tool-bound AG-UI interrupt (level 2, reason "tool_call").
		callCtx := agui.WithToolCall(runCtx, callID, tc.Function.Name)
		sink.StartSpinner(spinnerLabel)
		res := tools.ExecuteToolWithSpinner(callCtx, tc, s.GlobalIgnore, s.RestrictedRoot, s.MCPManager)
		sink.StopSpinner()

		// Clean up subagent panel after spawn_subagents completes
		if tc.Function.Name == "spawn_subagents" {
			tools.SubagentBufferProvider = nil
			// subagentPanel cleared on StreamToolOutputMsg in chatModel
		}

		if agui.RunInterrupted(runCtx) != nil {
			// The run was suspended for a standard AG-UI interrupt: this call
			// stays pending (its TOOL_CALL_RESULT belongs to the resumed run) and
			// so does every following call of the turn.
			return
		}

		outputLabel := lbl.Done
		if types.ToolResultFailed(res.Text) {
			outputLabel = lbl.Failed
		}
		if hasStructured {
			structured.ToolCallFinished(callID, tc.Function.Name, res.Text)
		} else {
			sink.WriteToolOutput(outputLabel, res.Text)
		}
		msg := types.Message{
			Role:    "tool",
			Name:    tc.Function.Name,
			Content: res.Text,
		}
		if len(res.Images) > 0 {
			msg.Images = res.Images
		}
		if includeToolCallID {
			msg.ToolCallID = tc.ID
		}
		s.ChatHistory = append(s.ChatHistory, msg)

		if runCtx.Err() != nil {
			// The turn was aborted while this tool ran (ctrl+c / client
			// disconnect): record a result for every call of the batch that never
			// executed, so the replayed history has one result per announced call.
			for _, rest := range toolCalls[i+1:] {
				aborted := types.Message{
					Role:    "tool",
					Name:    rest.Function.Name,
					Content: errToolAborted,
				}
				if includeToolCallID {
					aborted.ToolCallID = rest.ID
				}
				s.ChatHistory = append(s.ChatHistory, aborted)
			}
			return
		}
	}
}

// assignToolCallID stores a synthetic tool-call id into the last assistant
// message of the history, so a resumed run replays that call under the same id
// (the interrupt raised for it is bound to it).
func assignToolCallID(s *ChatSession, i int, callID string) {
	if s == nil || callID == "" {
		return
	}
	for j := len(s.ChatHistory) - 1; j >= 0; j-- {
		msg := &s.ChatHistory[j]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		if i < len(msg.ToolCalls) {
			msg.ToolCalls[i].ID = callID
		}
		return
	}
}

func printProviderAndTokenStats(sink types.OutputSink, cfg config.Config, result *types.StreamResult, s *ChatSession) {
	if result == nil || result.PromptTokens <= 0 {
		return
	}
	modeStr := "manual"
	if config.Get().NoAsk {
		modeStr = "auto"
	}
	providerInfo := config.GetCurrentProviderInfo()
	providerLine := ui.RenderProviderInfo(providerInfo, currentTemperatureLabel(), modeStr)
	tokenLine := tokenStatsLine(result, s)
	sink.WriteStats(providerLine, tokenLine)
}

// findActiveProvider searches first by active name, then by model as fallback.
func findActiveProvider(list []config.OpenAICompatibleProviderConfig, activeName, model string) *config.OpenAICompatibleProviderConfig {
	if activeName != "" {
		for _, p := range list {
			if p.Name == activeName {
				return &p
			}
		}
	}
	for _, p := range list {
		if p.Model == model {
			return &p
		}
	}
	return nil
}

// buildProvider constructs the active provider based on the current configuration.
func buildProvider(cfg config.Config, s *ChatSession) (providers.Provider, error) {
	// The reasoning intent and, when the user declared one, the placement
	// override for the active provider. Without an override the profile is
	// auto-detected by the providers (built-in table + models.dev catalog).
	intent, override := config.ActiveReasoning()
	switch cfg.Provider {
	case config.ProviderGemini:
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("Gemini API key not set")
		}
		p := providers.NewGeminiProvider(cfg.GeminiAPIKey, cfg.Model, cfg.Temperature)
		if s != nil && s.CurrentMode != nil {
			p.SystemInstruction = s.CurrentMode.GetInstructions(s.subagentsEnabled())
		}
		p.SetReasoning(intent, override)
		return p, nil

	case config.ProviderOpenAICompatible:
		ap := findActiveProvider(cfg.OpenAICompatibleProviders, cfg.ActiveProviderName, cfg.Model)
		if ap == nil {
			return nil, fmt.Errorf("OpenAI Compatible provider not found")
		}
		p := providers.NewOpenAICompatibleProvider(
			ap.BaseURL, ap.APIKey, ap.Model, ap.Name,
			cfg.Temperature, false, "",
		)
		p.SetReasoning(intent, override)
		return p, nil

	case config.ProviderOpenAIResponsesCompatible:
		ap := findActiveProvider(cfg.OpenAIResponsesCompatibleProviders, cfg.ActiveProviderName, cfg.Model)
		if ap == nil {
			return nil, fmt.Errorf("OpenAI Responses API Compatible provider not found")
		}
		p := providers.NewOpenAIResponsesCompatibleProvider(
			ap.BaseURL, ap.APIKey, ap.Model, ap.Name,
			cfg.Temperature, false, "",
		)
		p.SetReasoning(intent, override)
		return p, nil

	case config.ProviderAnthropic:
		ap := findActiveProvider(cfg.AnthropicProviders, cfg.ActiveProviderName, cfg.Model)
		if ap == nil {
			return nil, fmt.Errorf("Anthropic provider not found")
		}
		p := providers.NewAnthropicProvider(ap.BaseURL, ap.APIKey, ap.Model, ap.Name, cfg.Temperature)
		p.SetReasoning(intent, override)
		return p, nil

	case config.ProviderCodex:
		token, err := config.CodexFreshAccessToken()
		if err != nil {
			return nil, err
		}
		p := providers.NewCodexProvider(token, cfg.Model, cfg.Temperature, false, "")
		p.SetReasoning(intent, override)
		p.Refresh = config.CodexForceRefresh
		return p, nil

	default:
		return nil, fmt.Errorf("unsupported provider")
	}
}

// restoreModeInstructions repairs legacy pre-turn compaction before sampling,
// not on the next user input. In-turn compaction already retained the block.
func restoreModeInstructions(s *ChatSession, provider config.ProviderType) {
	active := s.activeMode()
	if active == nil || provider == config.ProviderGemini {
		return
	}
	instruction := "[INSTRUCTIONS]: " + active.GetInstructions(s.subagentsEnabled())
	for _, m := range s.ChatHistory {
		if m.Role == "user" && m.Content == instruction {
			return
		}
	}
	if s.turnModeOverride == nil && !s.claimModeInstructions(active, false) {
		return
	}
	at := 0
	for at < len(s.ChatHistory) && s.ChatHistory[at].Role == "system" {
		at++
	}
	history := make([]types.Message, 0, len(s.ChatHistory)+1)
	history = append(history, s.ChatHistory[:at]...)
	history = append(history, types.Message{Role: "user", Content: instruction})
	s.ChatHistory = append(history, s.ChatHistory[at:]...)
}

func responseTruncated(reason string) bool {
	switch strings.ToLower(reason) {
	case "length", "max_tokens", "max_output_tokens", "model_context_window_exceeded", "incomplete":
		return true
	}
	return false
}

func validateCompactionResponse(ctx context.Context, result *types.StreamResult) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if result == nil || result.Interrupted {
		return "", fmt.Errorf("compaction response interrupted or missing")
	}
	if responseTruncated(result.FinishReason) {
		return "", fmt.Errorf("compaction response truncated: %s", result.FinishReason)
	}
	switch strings.ToLower(result.FinishReason) {
	case "":
		// Compatible backends may omit the finish reason on a successful stream.
		// Missing metadata is not an explicit failure or truncation signal.
	case "stop", "end_turn", "stop_sequence":
	default:
		return "", fmt.Errorf("compaction response not complete: %q", result.FinishReason)
	}
	if len(result.ToolCalls) != 0 || strings.TrimSpace(result.Content) == "" {
		return "", fmt.Errorf("compaction response has tools or an empty summary")
	}
	return result.Content, nil
}

func getCompactionResponse(ctx context.Context, messages []types.Message, s *ChatSession) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// A utility request must not inherit the active mode's instructions.
	provider, err := buildProvider(config.Get(), nil)
	if err != nil {
		return "", err
	}
	ctx = logger.WithAgent(sessionLogContext(ctx, s), "compaction", "")
	result, err := providers.WithLogging(provider).StreamChat(ctx, pruneScreenshotImages(messages, 0), nil, providers.NewSilentOutputSink())
	if err != nil {
		return "", err
	}
	return validateCompactionResponse(ctx, result)
}

// tokenStatsLine returns the formatted string with token stats.
// Provider tokens are for information only; the context estimate is always
// calculated locally (see tokens.go) and shown alongside as "context".
func tokenStatsLine(result *types.StreamResult, s *ChatSession) string {
	if result == nil {
		return ""
	}
	cacheInfo := ""
	if result.CachedTokens > 0 {
		cacheInfo = fmt.Sprintf(" %s %s %s",
			ui.InfoStyle.Render("| cache hit:"),
			ui.OkStyle.Render(fmt.Sprintf("%d", result.CachedTokens)),
			ui.InfoStyle.Render("tokens"),
		)
	}

	return fmt.Sprintf("%s %s %s %s %s %s%s",
		ui.InfoStyle.Render("Tokens:"),
		ui.OkStyle.Render(fmt.Sprintf("%d", result.PromptTokens)),
		ui.InfoStyle.Render("prompt"),
		ui.InfoStyle.Render(" | "),
		ui.OkStyle.Render(fmt.Sprintf("%d", result.CompletionTokens)),
		ui.InfoStyle.Render("completion"),
		cacheInfo,
	)
}
