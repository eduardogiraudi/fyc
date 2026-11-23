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
func ExecuteToolWithSpinner(ctx context.Context, call types.ToolCall, globalIgnore *IgnoreManager, ws Workspace, mcpManager *mcp.MCPManager) types.ToolResult {
	executeMu.Lock()
	defer executeMu.Unlock()

	// Let a blocked interaction (AG-UI HITL) release the global execution mutex
	// while the client decides, so other threads keep making progress.
	ctx = withExecuteMuRelease(ctx, &executeMu)

	hooks := config.Get().Hooks
	if out, blocked := RunPreToolHook(hooks.PreTool, call, ws.Primary); blocked {
		msg := "error: blocked by pre-tool hook"
		if strings.TrimSpace(out) != "" {
			msg += ":\n" + out
		}
		return textResult(msg)
	}

	res := dispatchToolCall(ctx, call, globalIgnore, ws, mcpManager)

	// Record what the call changed before anything else runs: the undo journal
	// diffs the workspace, which is what makes `sh` and the git tools revertible
	// exactly like a patch.
	if journal := currentTurnJournal(); journal != nil && toolMayWrite(call.Function.Name) {
		journal.Collect(ws.Primary, globalIgnore)
	}

	// Post-tool hook: side effects only; its output is deliberately discarded
	// (not added to the model context, not written to the CLI output).
	RunPostToolHook(hooks.PostTool, call, res, ws.Primary)
	return res
}

// dispatchToolCall runs the actual tool implementation for a call.
//
// Tools that take a single path keep their single-root implementation: the path
// is resolved against the workspace here and handed over as a name inside the
// owning root, so an absolute path under any allowed root (/cd) works everywhere
// without a second code path per tool. Tools that take two paths or no path at
// all (copy/move, git, LSP listing, skills) get the whole workspace.
func dispatchToolCall(ctx context.Context, call types.ToolCall, globalIgnore *IgnoreManager, ws Workspace, mcpManager *mcp.MCPManager) types.ToolResult {
	name := call.Function.Name
	args := call.Function.Arguments

	if handled, result := executeMCPPrefixCheck(ctx, name, args, mcpManager); handled {
		return textResult(result)
	}

	// withPath resolves args[key] and runs a tool inside the owning root.
	withPath := func(key string, run func(map[string]any, string) string) types.ToolResult {
		bound, root, err := bindPath(args, key, ws, globalIgnore)
		if err != nil {
			return textResult(err.Error())
		}
		return textResult(run(bound, root))
	}
	// withPathResult is withPath for a tool that returns a structured result.
	withPathResult := func(key string, run func(map[string]any, string) types.ToolResult) types.ToolResult {
		bound, root, err := bindPath(args, key, ws, globalIgnore)
		if err != nil {
			return textResult(err.Error())
		}
		return run(bound, root)
	}

	switch name {
	case "kill":
		return textResult(executeKillTool(args))
	case "sniff":
		return textResult(executeSniffTool(args))
	case "skills_discovery":
		return textResult(executeSkillsDiscoveryTool(ws))

	case "read_skill":
		return textResult(executeReadSkillTool(args, ws))

	case "web_search":
		return textResult(executeWebSearchTool(ctx, args))

	case "ls":
		return withPath("path", func(a map[string]any, root string) string {
			return executeLSTool(a, root, globalIgnore)
		})
	case "grep":
		return withPath("path", func(a map[string]any, root string) string {
			return executeGrepTool(a, root, globalIgnore)
		})
	case "read":
		return withPath("path", func(a map[string]any, root string) string {
			return executeReadTool(a, root, globalIgnore)
		})
	case "get_anchors":
		return withPath("path", func(a map[string]any, root string) string {
			return executeGetAnchorsTool(a, root, globalIgnore)
		})

	case "replace_anchors":
		bound, root, err := bindPath(args, "path", ws, globalIgnore)
		if err != nil {
			return textResult(err.Error())
		}
		return executeReplaceAnchorsTool(ctx, bound, root, globalIgnore)
	case "write":
		return withPath("path", func(a map[string]any, root string) string {
			return executeWriteTool(ctx, a, root, globalIgnore)
		})

	case "delete":
		return withPath("path", func(a map[string]any, root string) string {
			return executeDeleteTool(ctx, a, root, globalIgnore)
		})
	case "update_user_preferences":
		return textResult(executeUpdateUserPreferencesTool(ctx, args))
	case "read_user_preferences":
		return textResult(executeReadUserPreferencesTool())
	case "sh":
		return textResult(executeShTool(ctx, args, ws, globalIgnore))
	case "make_dir":
		return withPath("path", func(a map[string]any, root string) string {
			return executeMakeDirTool(ctx, a, root, globalIgnore)
		})

	case "context_snapshot":
		return withPath("path", func(a map[string]any, root string) string {
			return executeContextSnapshotTool(a, root, globalIgnore)
		})
	case "list_mcp_resources":
		return textResult(executeListMCPResources(mcpManager))

	case "list_mcp_resource_templates":
		return textResult(executeListMCPResourceTemplates(mcpManager))

	case "copy":
		return textResult(executeCopyTool(ctx, args, ws, globalIgnore))

	case "rename":
		return withPath("path", func(a map[string]any, root string) string {
			return executeRenameTool(ctx, a, root, globalIgnore)
		})

	case "move":
		return textResult(executeMoveTool(ctx, args, ws, globalIgnore))

	case "read_mcp_resource":
		return textResult(executeReadMCPResource(args, mcpManager))
	case "lsp_active_servers":
		return textResult(executeLSPActiveServersIn(ws, globalIgnore))

	case "lsp_hover":
		return withPath("file", func(a map[string]any, root string) string {
			return executeLSPHover(a, root, globalIgnore)
		})

	case "lsp_definition":
		return withPath("file", func(a map[string]any, root string) string {
			return executeLSPDefinition(a, root, globalIgnore)
		})

	case "lsp_references":
		return withPath("file", func(a map[string]any, root string) string {
			return executeLSPReferences(a, root, globalIgnore)
		})

	case "lsp_diagnostics":
		return withPath("file", func(a map[string]any, root string) string {
			return executeLSPDiagnostics(a, root, globalIgnore)
		})

	case "lsp_workspace_symbols":
		return textResult(executeLSPWorkspaceSymbolsIn(args, ws, globalIgnore))

	case "questionary":
		return textResult(executeQuestionaryTool(ctx, args))
	case "spawn_subagents":
		return textResult(executeSpawnSubagentsTool(ctx, args, globalIgnore, ws, mcpManager))
	case "computer":
		if reason := ComputerUseBlockReason(); reason != "" {
			return textResult("error: computer use is disabled: " + reason + " (switch to Anthropic, Gemini 3 or a Responses-compatible provider)")
		}
		return executeComputerTool(ctx, args)

	case "read_media":
		if reason := ToolImageBlockReason(); reason != "" {
			return textResult("error: read_media is unavailable: " + reason)
		}
		return withPathResult("path", func(a map[string]any, root string) types.ToolResult {
			return executeReadMediaTool(a, root, globalIgnore)
		})

	// ── git tools (read-only by default, see config/git.go) ──────────────
	// The read-only ones answer for every allowed root that is a repository;
	// a mutating call runs in the root that owns its explicit path, or in the
	// primary root when it names none.
	case "git_status":
		return textResult(gitReadOnWorkspace(ws, args, executeGitStatus))
	case "git_diff":
		return textResult(gitReadOnWorkspace(ws, args, executeGitDiff))
	case "git_log":
		return textResult(gitReadOnWorkspace(ws, args, executeGitLog))
	case "git_show":
		return textResult(gitReadOnWorkspace(ws, args, executeGitShow))
	case "git_blame":
		return textResult(gitReadOnWorkspace(ws, args, executeGitBlame))
	case "git_ls_files":
		return textResult(gitReadOnWorkspace(ws, args, executeGitLsFiles))
	case "git_config":
		return textResult(gitReadOnWorkspace(ws, args, executeGitConfig))
	case "git_add":
		return textResult(executeGitAdd(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_commit":
		return textResult(executeGitCommit(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_switch":
		return textResult(executeGitSwitch(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_restore":
		return textResult(executeGitRestore(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_stash":
		return textResult(executeGitStash(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_init":
		return textResult(executeGitInit(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_branch":
		return textResult(executeGitBranch(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_remote":
		return textResult(executeGitRemote(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_fetch":
		return textResult(executeGitFetch(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_pull":
		return textResult(executeGitPull(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_push":
		return textResult(executeGitPush(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_clone":
		return textResult(executeGitClone(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_reset":
		return textResult(executeGitReset(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_merge":
		return textResult(executeGitMerge(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_rebase":
		return textResult(executeGitRebase(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_cherry_pick":
		return textResult(executeGitCherryPick(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_revert":
		return textResult(executeGitRevert(ctx, args, gitWorkspaceRoot(args, ws)))
	case "git_tag":
		return textResult(executeGitTag(ctx, args, gitWorkspaceRoot(args, ws)))
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
