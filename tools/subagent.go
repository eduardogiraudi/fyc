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
	// Profile names a configured sub-agent profile (model/provider chain).
	// Empty uses the default profile, or the active model when none is set.
	Profile string
}

// subagentStream runs one sub-agent sampling request, resending it when it fails
// before producing any visible output: a sub-agent has no manual /retry.
func subagentStream(provider types.Provider, ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	return providers.WithStreamResend(providers.WithLogging(provider)).StreamChat(ctx, messages, tools, sink)
}

// SubagentProviderFactory builds the provider for one model of a sub-agent's
// chain. It is set from main (see subagent_provider.go) so sub-agents share the
// single provider construction path of the chat, compaction included: same
// credentials, base URL and reasoning rules. systemPrompt is the sub-agent's
// system prompt, for providers that take it out of band.
var SubagentProviderFactory func(cfg config.Config, choice config.CompactorChoice, systemPrompt string) (types.Provider, error)

// subagentAttempt records one request of a chain that failed.
type subagentAttempt struct {
	Model string
	Err   error
}

// streamWithChain sends one sampling request, starting at chain[start] and
// moving to the next model whenever one fails (construction included: a
// deleted provider entry or a revoked key is a failure like any other). It
// returns the index of the model that answered. A cancelled parent context
// stops the chain at once: an interruption is never retried elsewhere.
func streamWithChain(ctx context.Context, cfg config.Config, chain []config.CompactorChoice, start int, sysPrompt string, history []types.Message, toolDefs []types.Tool, sink types.OutputSink) (*types.StreamResult, int, []subagentAttempt, error) {
	var failed []subagentAttempt
	for i := start; i < len(chain); i++ {
		choice := chain[i]
		msgs := history
		if i != start {
			msgs = stripReasoning(history)
		}
		if choice.Kind == config.CompactorGemini {
			// Gemini takes the system prompt as SystemInstruction.
			msgs = withoutSystem(msgs)
		}
		provider, err := SubagentProviderFactory(cfg, choice, sysPrompt)
		var result *types.StreamResult
		if err == nil {
			result, err = subagentStream(provider, ctx, msgs, toolDefs, sink)
		}
		if err == nil {
			return result, i, failed, nil
		}
		failed = append(failed, subagentAttempt{Model: choice.Label(), Err: err})
		if ctx.Err() != nil {
			return nil, i, failed, err
		}
		sink.Reset()
	}
	return nil, len(chain) - 1, failed, failed[len(failed)-1].Err
}

// stripReasoning returns a copy of history without reasoning content or
// signatures, which are only valid for the model that produced them.
func stripReasoning(history []types.Message) []types.Message {
	out := make([]types.Message, len(history))
	copy(out, history)
	for i := range out {
		out[i].ReasoningContent = ""
		out[i].ReasoningSignature = ""
	}
	return out
}

func withoutSystem(history []types.Message) []types.Message {
	out := make([]types.Message, 0, len(history))
	for _, m := range history {
		if m.Role != "system" {
			out = append(out, m)
		}
	}
	return out
}

// subagentFailure renders why no model of the chain answered: every attempt,
// then the profile entries that could not even be resolved.
func subagentFailure(failed []subagentAttempt, skipped []error) string {
	var parts []string
	for _, a := range failed {
		parts = append(parts, fmt.Sprintf("%s: %v", a.Model, a.Err))
	}
	if len(skipped) > 0 {
		parts = append(parts, "skipped: "+joinErrors(skipped))
	}
	return strings.Join(parts, "; ")
}

func joinErrors(errs []error) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// RunSubagent spawns a sub-agent in the given subdirectory with a custom
// system prompt and a filtered toolset. The subdirectory is relative to the
// parent's primary root, or an absolute path inside any allowed root (/cd); the
// sub-agent is then confined to that folder and inherits the parent's ignore
// rules. Its model comes from cfg.Profile (a configured model chain with
// fallbacks) and defaults to the active model of the main agent.
// progressSink is an optional OutputSink (e.g. *types.SubagentBuffer) for
// live UI updates; if nil, a SilentOutputSink is used internally.
func RunSubagent(parentCtx context.Context, cfg SubagentConfig, parentIgnore *IgnoreManager, ws Workspace, mcpManager *mcp.MCPManager, progressSink types.OutputSink) string {
	// 1. Resolve and validate subdirectory. A relative name belongs to the
	// parent's primary root; an absolute one may point inside any allowed root
	// (that is how a sub-agent is spawned in a folder added with /cd).
	rootAbs := ws.primaryRoot()
	if rootAbs == "" {
		return "subagent error: the parent session has no workspace root"
	}
	requested := cfg.Subdirectory
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(rootAbs, requested)
	}
	owner, rel, rerr := ws.resolve(requested, nil)
	if rerr != nil {
		// A name spelled inside an allowed root that resolves elsewhere is a
		// symlink escape, not a path outside the workspace: say which one it is.
		if _, ok := ws.lexicalRoot(requested); ok {
			return fmt.Sprintf("subagent error: subdirectory %s resolves outside the restricted root: %v", cfg.Subdirectory, rerr)
		}
		return fmt.Sprintf("subagent error: subdirectory %s is outside the restricted root", cfg.Subdirectory)
	}
	absSubdir := filepath.Join(owner, rel)
	if abs, aerr := filepath.Abs(absSubdir); aerr == nil {
		absSubdir = abs
	}
	if fi, stErr := os.Stat(absSubdir); stErr != nil || !fi.IsDir() {
		return fmt.Sprintf("subagent error: subdirectory %s does not exist or is not a directory", cfg.Subdirectory)
	}
	// os.Stat follows symlinks and absSubdir becomes the tools' restricted root:
	// a link inside the workspace (e.g. "logs -> /var/log") would re-root the
	// sub-agent outside it.
	resolved, err := realPathWithinRoot(absSubdir, owner)
	if err != nil {
		return fmt.Sprintf("subagent error: subdirectory %s resolves outside the restricted root: %v", cfg.Subdirectory, err)
	}
	absSubdir = resolved
	// The sub-agent is confined to its own directory: it never inherits the
	// parent's extra roots.
	subWorkspace := NewWorkspace(absSubdir, nil)

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
		maxIter = config.Get().SubagentDefaultIterationCount()
	} else {
		maxIter = config.ClampSubagentIterations(maxIter)
	}

	// 6. Resolve the model chain: the profile's model, its fallbacks, then the
	// active model (policy permitting). Without a profile it is just the active
	// model, as before profiles existed. The configuration is read once so every
	// request of this sub-agent sees the same chain.
	cfgSnapshot := config.Get()
	chain, skipped, chainErr := cfgSnapshot.SubagentChain(cfg.Profile)
	if chainErr != nil {
		return "subagent error: " + chainErr.Error()
	}
	if len(chain) == 0 {
		return "subagent error: no usable model: " + joinErrors(skipped)
	}
	if SubagentProviderFactory == nil {
		return "subagent error: no provider factory configured"
	}
	// cur is sticky: once a model of the chain worked, later requests of this
	// sub-agent start from it instead of retrying a model that just failed.
	cur := 0
	fallbackNote := ""

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
	// agentCtx outlives the sub-agent's own deadline: the wrap-up request that
	// reports partial work after a timeout needs a live context.
	agentCtx := logger.WithAgent(parentCtx, "subagent", cfg.ID)
	subTimeout := cfgSnapshot.SubagentTimeout()
	sink := progressSink
	if sink == nil {
		sink = providers.NewSilentOutputSink()
	}
	ctx, cancel := context.WithTimeout(agentCtx, subTimeout)
	defer cancel()
	// timedOut tells the sub-agent's own deadline apart from the user's
	// interruption: only the former is a budget the sub-agent can report on.
	timedOut := func() bool {
		return parentCtx.Err() == nil && ctx.Err() == context.DeadlineExceeded
	}
	var progress []string
	lastContent := ""
	stop := func(reason string) string {
		return subagentStopped(agentCtx, cfgSnapshot, chain, cur, sysPrompt, subHistory, allowedToolDefs, sink, reason, progress, lastContent) + fallbackNote
	}

	for iter := 0; iter < maxIter; iter++ {
		sink.Reset()
		result, used, failed, err := streamWithChain(ctx, cfgSnapshot, chain, cur, sysPrompt, subHistory, allowedToolDefs, sink)
		if err == nil && (used != cur || len(failed) > 0) {
			// A reasoning signature belongs to the model that produced it: the
			// next request goes to another one, so earlier thinking is dropped.
			subHistory = stripReasoning(subHistory)
			cur = used
			fallbackNote = fmt.Sprintf("\n\n(note: %s failed: %v; continued with %s)", failed[0].Model, failed[0].Err, chain[used].Label())
		}
		if err != nil {
			if timedOut() {
				return stop(fmt.Sprintf("reached the time limit (%s)", subTimeout))
			}
			return fmt.Sprintf("subagent error: %v", subagentFailure(failed, skipped)) + subagentDigest(progress, lastContent)
		}

		if result.Interrupted {
			if timedOut() {
				return stop(fmt.Sprintf("reached the time limit (%s)", subTimeout))
			}
			return "subagent interrupted"
		}
		if result.Content != "" {
			lastContent = result.Content
		}

		// Append assistant response to sub-history. The reasoning signature is
		// carried for the providers that require it back, mirroring the main
		// loop: Anthropic rejects a thinking block without its signature, and
		// Gemini 3 needs its thoughtSignature to keep reasoning continuity
		// across tool calls.
		msg := types.Message{Role: "assistant", Content: result.Content, ReasoningContent: result.ReasoningContent}
		if k := chain[cur].Kind; k == config.CompactorAnthropic || k == config.CompactorGemini {
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
				return result.Content + fallbackNote
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
			res := ExecuteToolWithSpinner(ctx, tc, subIgnore, subWorkspace, mcpManager)
			doneLabel := lbl.Done
			if types.ToolResultFailed(res.Text) {
				doneLabel = lbl.Failed
			}
			sink.WriteToolOutput(doneLabel, res.Text)
			progress = append(progress, doneLabel)
			subHistory = append(subHistory, types.Message{
				Role:       "tool",
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
				Content:    res.Text,
			})
		}
	}

	return stop(fmt.Sprintf("reached the iteration limit (%d)", maxIter))
}

// RunSubagents spawns multiple sub-agents concurrently and returns an
// aggregated report of all results. Each sub-agent runs independently.
func RunSubagents(ctx context.Context, agents []SubagentConfig, parentIgnore *IgnoreManager, ws Workspace, mcpManager *mcp.MCPManager) string {
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
			res := RunSubagent(ctx, cfg, parentIgnore, ws, mcpManager, agentBuf)
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

	// Preserve inherited scopes while collecting new local overrides.
	// Copy-on-merge never aliases the parent's backing array.
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
