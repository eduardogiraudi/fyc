package tools

import (
	"context"
	"fmt"
	"fuckyourcli/config"
	"fuckyourcli/mcp"
	"fuckyourcli/types"
	"strings"
	"sync"
)

// ExecuteTool dispatches a tool call by name, handling MCP tools, permission prompts,
// questionaryMu serializes the questionary tool across concurrent sub-agents
// to prevent overlapping stdin reads.
var questionaryMu sync.Mutex
var executeMu sync.Mutex
var persistentMu sync.Mutex

type persistentProcess struct {
	Result  *sandboxResult
	Proc    sandboxProcess
	Cancel  context.CancelFunc
	Pid     int
	Command string
	Buf     *tailBuffer
	Redact  func(string) string

	// Exit state. The waiter goroutine sets it while sniff/kill read it from
	// other goroutines, so it is guarded by this mutex (the byte buffer keeps
	// its own). Never touch exited/exitErr without holding mu.
	mu      sync.Mutex
	exited  bool
	exitErr string
}

var persistentProcesses = make(map[int]*persistentProcess)

// ExecuteToolWithSpinner dispatches a tool call and returns the result, running
// the configured pre/post tool hooks around the actual execution.
func ExecuteToolWithSpinner(ctx context.Context, call types.ToolCall, globalIgnore *IgnoreManager, restrictedRoot string, mcpManager *mcp.MCPManager) types.ToolResult {
	executeMu.Lock()
	defer executeMu.Unlock()

	// Let a blocked interaction (AG-UI HITL) release the global execution mutex
	// while the client decides, so other threads keep making progress.
	ctx = withExecuteMuRelease(ctx, &executeMu)

	hooks := config.Get().Hooks
	if out, blocked := RunPreToolHook(hooks.PreTool, call, restrictedRoot); blocked {
		msg := "error: blocked by pre-tool hook"
		if strings.TrimSpace(out) != "" {
			msg += ":\n" + out
		}
		return textResult(msg)
	}

	res := dispatchToolCall(ctx, call, globalIgnore, restrictedRoot, mcpManager)

	// Post-tool hook: side effects only; its output is deliberately discarded
	// (not added to the model context, not written to the CLI output).
	RunPostToolHook(hooks.PostTool, call, res, restrictedRoot)
	return res
}

// dispatchToolCall runs the actual tool implementation for a call.
func dispatchToolCall(ctx context.Context, call types.ToolCall, globalIgnore *IgnoreManager, restrictedRoot string, mcpManager *mcp.MCPManager) types.ToolResult {
	name := call.Function.Name
	args := call.Function.Arguments

	if handled, result := executeMCPPrefixCheck(ctx, name, args, mcpManager); handled {
		return textResult(result)
	}

	switch name {
	case "kill":
		return textResult(executeKillTool(args))
	case "sniff":
		return textResult(executeSniffTool(args))
	case "skills_discovery":
		return textResult(executeSkillsDiscoveryTool(restrictedRoot))

	case "read_skill":
		return textResult(executeReadSkillTool(args, restrictedRoot))

	case "web_search":
		return textResult(executeWebSearchTool(args))

	case "ls":
		return textResult(executeLSTool(args, restrictedRoot, globalIgnore))
	case "grep":
		return textResult(executeGrepTool(args, restrictedRoot, globalIgnore))
	case "read":
		return textResult(executeReadTool(args, restrictedRoot, globalIgnore))
	case "get_anchors":
		return textResult(executeGetAnchorsTool(args, restrictedRoot, globalIgnore))

	case "replace_anchors":
		return textResult(executeReplaceAnchorsTool(ctx, args, restrictedRoot, globalIgnore))
	case "write":
		return textResult(executeWriteTool(ctx, args, restrictedRoot, globalIgnore))

	case "delete":
		return textResult(executeDeleteTool(ctx, args, restrictedRoot, globalIgnore))
	case "update_user_preferences":
		return textResult(executeUpdateUserPreferencesTool(ctx, args))
	case "read_user_preferences":
		return textResult(executeReadUserPreferencesTool())
	case "sh":
		return textResult(executeShTool(ctx, args, restrictedRoot, globalIgnore))
	case "make_dir":
		return textResult(executeMakeDirTool(ctx, args, restrictedRoot, globalIgnore))

	case "context_snapshot":
		return textResult(executeContextSnapshotTool(args, restrictedRoot, globalIgnore))
	case "list_mcp_resources":
		return textResult(executeListMCPResources(mcpManager))

	case "list_mcp_resource_templates":
		return textResult(executeListMCPResourceTemplates(mcpManager))

	case "copy":
		return textResult(executeCopyTool(ctx, args, restrictedRoot, globalIgnore))

	case "rename":
		return textResult(executeRenameTool(ctx, args, restrictedRoot, globalIgnore))

	case "move":
		return textResult(executeMoveTool(ctx, args, restrictedRoot, globalIgnore))

	case "read_mcp_resource":
		return textResult(executeReadMCPResource(args, mcpManager))
	case "lsp_active_servers":
		return textResult(executeLSPActiveServers(restrictedRoot, globalIgnore))

	case "lsp_hover":
		return textResult(executeLSPHover(args, restrictedRoot, globalIgnore))

	case "lsp_definition":
		return textResult(executeLSPDefinition(args, restrictedRoot, globalIgnore))

	case "lsp_references":
		return textResult(executeLSPReferences(args, restrictedRoot, globalIgnore))

	case "lsp_diagnostics":
		return textResult(executeLSPDiagnostics(args, restrictedRoot, globalIgnore))

	case "lsp_workspace_symbols":
		return textResult(executeLSPWorkspaceSymbols(args, restrictedRoot, globalIgnore))

	case "questionary":
		return textResult(executeQuestionaryTool(ctx, args))
	case "spawn_subagents":
		return textResult(executeSpawnSubagentsTool(ctx, args, globalIgnore, restrictedRoot, mcpManager))
	case "computer":
		if reason := ComputerUseBlockReason(); reason != "" {
			return textResult("error: computer use is disabled: " + reason + " (switch to Anthropic, Gemini 3 or a Responses-compatible provider)")
		}
		return executeComputerTool(ctx, args)

	case "read_media":
		if reason := ToolImageBlockReason(); reason != "" {
			return textResult("error: read_media is unavailable: " + reason)
		}
		return executeReadMediaTool(args, restrictedRoot, globalIgnore)

	// ── git tools (read-only by default, see config/git.go) ──────────────
	case "git_status":
		return textResult(executeGitStatus(args, restrictedRoot))
	case "git_diff":
		return textResult(executeGitDiff(args, restrictedRoot))
	case "git_log":
		return textResult(executeGitLog(args, restrictedRoot))
	case "git_show":
		return textResult(executeGitShow(args, restrictedRoot))
	case "git_blame":
		return textResult(executeGitBlame(args, restrictedRoot))
	case "git_ls_files":
		return textResult(executeGitLsFiles(args, restrictedRoot))
	case "git_config":
		return textResult(executeGitConfig(args, restrictedRoot))
	case "git_add":
		return textResult(executeGitAdd(ctx, args, restrictedRoot))
	case "git_commit":
		return textResult(executeGitCommit(ctx, args, restrictedRoot))
	case "git_switch":
		return textResult(executeGitSwitch(ctx, args, restrictedRoot))
	case "git_restore":
		return textResult(executeGitRestore(ctx, args, restrictedRoot))
	case "git_stash":
		return textResult(executeGitStash(ctx, args, restrictedRoot))
	case "git_init":
		return textResult(executeGitInit(ctx, args, restrictedRoot))
	case "git_branch":
		return textResult(executeGitBranch(ctx, args, restrictedRoot))
	case "git_remote":
		return textResult(executeGitRemote(ctx, args, restrictedRoot))
	case "git_fetch":
		return textResult(executeGitFetch(ctx, args, restrictedRoot))
	case "git_pull":
		return textResult(executeGitPull(ctx, args, restrictedRoot))
	case "git_push":
		return textResult(executeGitPush(ctx, args, restrictedRoot))
	case "git_clone":
		return textResult(executeGitClone(ctx, args, restrictedRoot))
	case "git_reset":
		return textResult(executeGitReset(ctx, args, restrictedRoot))
	case "git_merge":
		return textResult(executeGitMerge(ctx, args, restrictedRoot))
	case "git_rebase":
		return textResult(executeGitRebase(ctx, args, restrictedRoot))
	case "git_cherry_pick":
		return textResult(executeGitCherryPick(ctx, args, restrictedRoot))
	case "git_revert":
		return textResult(executeGitRevert(ctx, args, restrictedRoot))
	case "git_tag":
		return textResult(executeGitTag(ctx, args, restrictedRoot))
	default:
		return textResult(fmt.Sprintf("error: unknown tool %s", name))
	} // End of switch
}

func extractStringList(m map[string]interface{}, key string) []string {
	raw, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// CloseLSPManagers shuts down all lazily-started language servers.
func CloseLSPManagers() {
	closeAllLSPManagers()
}
