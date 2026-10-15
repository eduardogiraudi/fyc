package tools

import (
	"context"
	"fmt"
	"fuckyourcli/config"
	"fuckyourcli/logger"
	"fuckyourcli/mcp"
	"fuckyourcli/providers"
	"fuckyourcli/types"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SubagentConfig holds the parameters for spawning a sub-agent.
type SubagentConfig struct {
	Subdirectory  string
	Instructions  string
	AllowedTools  []string // empty = all tools
	MaxIterations int
	ID            string // unique identifier for multi-agent scenarios
}

// RunSubagent spawns a sub-agent in the given subdirectory with a custom
// system prompt and a filtered toolset. It inherits ignore rules from the
// parent and runs the same provider/model as the main agent.
// progressSink is an optional OutputSink (e.g. *types.SubagentBuffer) for
// live UI updates; if nil, a SilentOutputSink is used internally.
func RunSubagent(parentCtx context.Context, cfg SubagentConfig, parentIgnore *IgnoreManager, parentRoot string, mcpManager *mcp.MCPManager, progressSink types.OutputSink) string {
	// 1. Resolve and validate subdirectory
	absSubdir := cfg.Subdirectory
	if !filepath.IsAbs(absSubdir) {
		absSubdir = filepath.Join(parentRoot, absSubdir)
	}
	absSubdir, err := filepath.Abs(absSubdir)
	if err != nil {
		return fmt.Sprintf("subagent error: cannot resolve subdirectory: %v", err)
	}
	if !strings.HasPrefix(absSubdir, parentRoot) {
		return fmt.Sprintf("subagent error: subdirectory %s is outside the restricted root", cfg.Subdirectory)
	}
	if fi, stErr := os.Stat(absSubdir); stErr != nil || !fi.IsDir() {
		return fmt.Sprintf("subagent error: subdirectory %s does not exist or is not a directory", cfg.Subdirectory)
	}

	// 2. Build sub-agent's ignore manager (parent + any local ignore files)
	subIgnore := buildSubagentIgnore(parentIgnore, absSubdir)

	// 3. Build the sub-agent's system prompt
	dateStr := time.Now().Format("Monday, January 2, 2006")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are a focused sub-agent spawned by the main agent.
Today is %s. Your working directory is %s (all writes restricted here).
%s

OPERATIONAL INSTRUCTIONS (from main agent):
%s

`, dateStr, absSubdir, config.ShellNote(runtime.GOOS), cfg.Instructions))

	// Build the system prompt with allowed tool names
	if len(cfg.AllowedTools) > 0 {
		sb.WriteString("AVAILABLE TOOLS:\n")
		for _, name := range cfg.AllowedTools {
			sb.WriteString(fmt.Sprintf("- %s\n", name))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`RULES:
1. Answer concisely — let tool output speak.
2. Verify first: always use ls, read, grep, context_snapshot before changing files.
3. Never use spawn_subagents — you cannot spawn sub-sub-agents.
3b. git tools are not available to sub-agents: report what the main agent should run instead.
4. All file operations are restricted to your working directory.
5. Use the available tools to complete the operational instructions above.
6. END OF RESPONSE: after completing the task, output a structured summary in this format:

   ## Sub-agent Result
   **Status**: success | partial | error

   ### Summary
   Brief description of what was done and the outcome.

   ### Files affected
   - 'path/to/file' (created|modified|deleted|read)

   ### Errors / Warnings
   - any errors or issues encountered

   ### Raw output
   (your full response text)
`)

	// Inject the AGENTS.md instruction chain scoped to the sub-agent's own
	// directory (repository root → subdirectory), exactly like the main agent.
	if pc := RenderAgentsDocs(LoadAgentsDocs(absSubdir)); pc != "" {
		sb.WriteString("\n" + pc + "\n")
	}

	sysPrompt := sb.String()

	// 5. Determine max iterations
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 10
	}

	// 6. Get provider config
	cfgProvider := config.Get()

	// Reasoning: same resolution as the main loop — the per-provider override
	// when the user declared one, else the global preference.
	subIntent, subOverride := config.ActiveReasoning()

	// 7. Build sub-agent chat history
	subHistory := []types.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: "Proceed with the operational instructions above."},
	}

	// 8. Determine allowed tool definitions to pass to provider.
	// The sub-agent policy (tools/subagents_policy.go) removes every tool that
	// must never be reachable by a sub-agent: spawn_subagents, computer and all
	// git tools.
	allowedToolDefs := FilterToolsForSubagent(globalSubagentTools, cfg.AllowedTools)

	// 9. Run the tool-call loop
	ctx, cancel := context.WithTimeout(logger.WithAgent(parentCtx, "subagent", cfg.ID), 5*time.Minute)
	defer cancel()
	sink := progressSink
	if sink == nil {
		sink = providers.NewSilentOutputSink()
	}

	for iter := 0; iter < maxIter; iter++ {
		sink.Reset()
		// Create provider instance
		var result *types.StreamResult
		var err error

		if cfgProvider.Provider == config.ProviderGemini {
			provider := providers.NewGeminiProvider(cfgProvider.GeminiAPIKey, cfgProvider.Model, cfgProvider.Temperature)
			provider.SystemInstruction = sysPrompt
			provider.SetReasoning(subIntent, subOverride)

			// For Gemini, we strip the system message from subHistory since it's in SystemInstruction
			var geminiHistory []types.Message
			for _, m := range subHistory {
				if m.Role != "system" {
					geminiHistory = append(geminiHistory, m)
				}
			}
			result, err = providers.WithLogging(provider).StreamChat(ctx, geminiHistory, allowedToolDefs, sink)
		} else if cfgProvider.Provider == config.ProviderAnthropic {
			// Find active Anthropic provider
			var activeProvider *config.OpenAICompatibleProviderConfig
			if cfgProvider.ActiveProviderName != "" {
				for _, p := range cfgProvider.AnthropicProviders {
					if p.Name == cfgProvider.ActiveProviderName {
						activeProvider = &p
						break
					}
				}
			}
			if activeProvider == nil {
				for _, p := range cfgProvider.AnthropicProviders {
					if p.Model == cfgProvider.Model {
						activeProvider = &p
						break
					}
				}
			}
			if activeProvider == nil {
				return "subagent error: no active Anthropic provider found"
			}
			provider := providers.NewAnthropicProvider(
				activeProvider.BaseURL,
				activeProvider.APIKey,
				activeProvider.Model,
				activeProvider.Name,
				cfgProvider.Temperature,
			)
			provider.SetReasoning(subIntent, subOverride)
			result, err = providers.WithLogging(provider).StreamChat(ctx, subHistory, allowedToolDefs, sink)
		} else if cfgProvider.Provider == config.ProviderCodex {
			codexToken, tokenErr := config.CodexFreshAccessToken()
			if tokenErr != nil {
				return "subagent error: Codex not authenticated"
			}
			provider := providers.NewCodexProvider(
				codexToken,
				cfgProvider.Model,
				cfgProvider.Temperature,
				false,
				"",
			)
			provider.SetReasoning(subIntent, subOverride)
			provider.Refresh = config.CodexForceRefresh
			result, err = providers.WithLogging(provider).StreamChat(ctx, subHistory, allowedToolDefs, sink)
		} else if cfgProvider.Provider == config.ProviderOpenAIResponsesCompatible {
			var activeProvider *config.OpenAICompatibleProviderConfig
			for _, p := range cfgProvider.OpenAIResponsesCompatibleProviders {
				if p.Model == cfgProvider.Model {
					activeProvider = &p
					break
				}
			}
			if activeProvider == nil {
				return "subagent error: no active Responses provider found"
			}
			provider := providers.NewOpenAIResponsesCompatibleProvider(
				activeProvider.BaseURL,
				activeProvider.APIKey,
				activeProvider.Model,
				activeProvider.Name,
				cfgProvider.Temperature,
				false,
				"",
			)
			provider.SetReasoning(subIntent, subOverride)
			result, err = providers.WithLogging(provider).StreamChat(ctx, subHistory, allowedToolDefs, sink)
		} else {
			// Find active OpenAI-compatible provider
			var activeProvider *config.OpenAICompatibleProviderConfig
			for _, p := range cfgProvider.OpenAICompatibleProviders {
				if p.Model == cfgProvider.Model {
					activeProvider = &p
					break
				}
			}
			if activeProvider == nil {
				return "subagent error: no active provider found"
			}

			provider := providers.NewOpenAICompatibleProvider(
				activeProvider.BaseURL,
				activeProvider.APIKey,
				activeProvider.Model,
				activeProvider.Name,
				cfgProvider.Temperature,
				false,
				"",
			)
			provider.SetReasoning(subIntent, subOverride)
			result, err = providers.WithLogging(provider).StreamChat(ctx, subHistory, allowedToolDefs, sink)
		}

		if err != nil {
			return fmt.Sprintf("subagent error: %v", err)
		}

		if result.Interrupted {
			return "subagent interrupted"
		}

		// Append assistant response to sub-history. The reasoning signature is
		// carried for the providers that require it back, mirroring the main
		// loop: Anthropic rejects a thinking block without its signature, and
		// Gemini 3 needs its thoughtSignature to keep reasoning continuity
		// across tool calls.
		msg := types.Message{Role: "assistant", Content: result.Content, ReasoningContent: result.ReasoningContent}
		if cfgProvider.Provider == config.ProviderAnthropic || cfgProvider.Provider == config.ProviderGemini {
			msg.ReasoningSignature = result.ReasoningSignature
		}
		if len(result.ToolCalls) > 0 {
			msg.ToolCalls = make([]types.ToolCall, len(result.ToolCalls))
			copy(msg.ToolCalls, result.ToolCalls)
		}
		subHistory = append(subHistory, msg)

		// If no tool calls, we're done
		if len(result.ToolCalls) == 0 {
			if result.Content != "" {
				return result.Content
			}
			return "subagent completed with no output"
		}

		// Execute tool calls
		for _, tc := range result.ToolCalls {
			// Defense in depth: even if the tool slipped into the list, block
			// it using the central sub-agent policy.
			if reason := SubagentToolBlockReason(tc.Function.Name); reason != "" {
				subHistory = append(subHistory, types.Message{
					Role:       "tool",
					Name:       tc.Function.Name,
					ToolCallID: tc.ID,
					Content:    reason,
				})
				continue
			}
			// Report tool call to progress sink using the same universal labels
			// as the main agent (never the raw tool name).
			lbl := DescribeTool(tc.Function.Name, tc.Function.Arguments)
			sink.WriteToolOutput(strings.TrimSuffix(lbl.Present, "..."), "")
			res := ExecuteToolWithSpinner(ctx, tc, subIgnore, absSubdir, mcpManager)
			doneLabel := lbl.Done
			if types.ToolResultFailed(res.Text) {
				doneLabel = lbl.Failed
			}
			sink.WriteToolOutput(doneLabel, res.Text)
			subHistory = append(subHistory, types.Message{
				Role:       "tool",
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
				Content:    res.Text,
			})
		}
	}

	return "subagent reached maximum iterations without completing"
}

// RunSubagents spawns multiple sub-agents concurrently and returns an
// aggregated report of all results. Each sub-agent runs independently.
func RunSubagents(ctx context.Context, agents []SubagentConfig, parentIgnore *IgnoreManager, parentRoot string, mcpManager *mcp.MCPManager) string {
	type agentResult struct {
		ID     string
		Result string
	}

	results := make(chan agentResult, len(agents))
	var wg sync.WaitGroup

	for _, cfg := range agents {
		buf := SubagentBufferProvider
		var agentBuf types.OutputSink
		if buf != nil {
			agentBuf = buf(cfg.ID, cfg.Instructions)
		}
		wg.Add(1)
		go func(cfg SubagentConfig, agentBuf types.OutputSink) {
			defer wg.Done()
			res := RunSubagent(ctx, cfg, parentIgnore, parentRoot, mcpManager, agentBuf)
			if agentBuf != nil {
				if setter, ok := agentBuf.(interface{ SetDone(string) }); ok {
					setter.SetDone(res)
				}
			}
			results <- agentResult{ID: cfg.ID, Result: res}
		}(cfg, agentBuf)
	}

	wg.Wait()
	close(results)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Sub-agents Results (%d agents)\n\n", len(agents)))

	for r := range results {
		sb.WriteString(fmt.Sprintf("## Agent: %s\n", r.ID))
		sb.WriteString(r.Result)
		sb.WriteString("\n---\n\n")
	}

	return sb.String()
}

// SubagentBufferProvider is set from main before spawn_subagents runs.
// It creates a *types.SubagentBuffer per agent for live UI progress.
var SubagentBufferProvider func(id, label string) types.OutputSink

// globalSubagentTools is set once from main to give subagents access to the full tool list.
var globalSubagentTools []types.Tool

// SetGlobalSubagentTools sets the global tool list for subagent use.
func SetGlobalSubagentTools(tools []types.Tool) {
	globalSubagentTools = tools
}

// buildSubagentIgnore returns the IgnoreManager that polices a sub-agent's
// filesystem access. It merges the parent's scoped rules with any ignore file
// found in the sub-agent's working directory.
//
// The .git guard is ALWAYS re-rooted on the sub-agent's own restricted root
// (absSubdir) — never inherited from the parent. Every path handed to
// isIgnored is built from absSubdir (see RunSubagent, which passes absSubdir
// as restrictedRoot), so a manager still pointing at <parentRoot>/.git would
// leave the sub-agent's local .git unprotected when absSubdir is a proper
// subdirectory. Note LoadIgnoreFiles sets gitDir unconditionally, so it is a
// valid value even when the subdirectory has no .git and no ignore file.
//
// It returns nil when there is no parent manager (sub-agents then run without
// ignore rules, matching a main agent started outside a workspace).
func buildSubagentIgnore(parentIgnore *IgnoreManager, absSubdir string) *IgnoreManager {
	if parentIgnore == nil {
		return nil
	}

	// Serve both the "reuse parent rules" and "merge" cases with a single
	// manager: copy-on-merge never aliases the parent's backing array.
	files := parentIgnore.files
	if localIgnore := LoadIgnoreFiles(absSubdir); len(localIgnore.files) > 0 {
		files = make([]ignoreFile, 0, len(parentIgnore.files)+len(localIgnore.files))
		files = append(files, parentIgnore.files...)
		files = append(files, localIgnore.files...)
	}

	gitDir := filepath.Join(absSubdir, ".git")
	return &IgnoreManager{
		files:         files,
		gitDir:        gitDir,
		gitDirWithSep: gitDir + string(filepath.Separator),
	}
}
