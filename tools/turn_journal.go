package tools

import (
	"path/filepath"
	"strings"
	"sync"
)

// TurnJournal is the workspace snapshot the caller installs: after every tool
// that can touch the disk it records what changed, so /undo can put the files
// back. The interface lives here only so the dispatcher can reach it without
// importing the session; the journal itself belongs to the caller.
type TurnJournal interface {
	// Prepare stores the pre-image of the files a call declares it may write,
	// before the call runs. It is what makes the journal copy-on-write: the
	// whole workspace no longer has to be copied at the start of a turn.
	Prepare(paths []string, ig *IgnoreManager)
	// PrepareTree stores the pre-image of the whole workspace, for a tool that
	// cannot declare its targets (sh, a git rewrite, an MCP server).
	PrepareTree(root string, ig *IgnoreManager)
	// Collect attributes the changes since the last call to the current turn.
	Collect(root string, ig *IgnoreManager)
}

var (
	turnJournalMu sync.RWMutex
	turnJournal   TurnJournal
)

// SetTurnJournal installs (or clears, with nil) the journal the dispatcher feeds.
func SetTurnJournal(j TurnJournal) {
	turnJournalMu.Lock()
	turnJournal = j
	turnJournalMu.Unlock()
}

func currentTurnJournal() TurnJournal {
	turnJournalMu.RLock()
	defer turnJournalMu.RUnlock()
	return turnJournal
}

// readOnlyTools never change the workspace, so the dispatcher does not diff it
// after them. Everything else is diffed, MCP tools included: a server may write.
var readOnlyTools = map[string]bool{
	"read": true, "ls": true, "grep": true, "get_anchors": true, "read_media": true,
	"web_search": true, "context_snapshot": true, "skills_discovery": true,
	"read_skill": true, "read_user_preferences": true, "questionary": true,
	"computer": true, "sniff": true, "kill": true,
	// spawn_subagents writes through the sub-agent's own tool calls (which the
	// journal already sees), and the preferences live outside the workspace.
	"spawn_subagents": true, "update_user_preferences": true,
	"read_mcp_resource": true, "list_mcp_resources": true, "list_mcp_resource_templates": true,
	"lsp_active_servers": true, "lsp_hover": true, "lsp_definition": true,
	"lsp_references": true, "lsp_diagnostics": true, "lsp_workspace_symbols": true,
	"git_status": true, "git_diff": true, "git_log": true, "git_show": true,
	"git_blame": true, "git_ls_files": true, "git_config": true,
}

// toolMayWrite reports whether the workspace may have changed after a call of
// this tool. Unknown names (MCP tools) are assumed to have written.
func toolMayWrite(name string) bool { return !readOnlyTools[name] }

// pathArgs names the arguments that hold the files a writing tool is about to
// touch. A tool listed here is prepared file by file; everything else that may
// write gets the workspace as its pre-image (see PrepareTree).
var pathArgs = map[string][]string{
	"write":           {"path"},
	"replace_anchors": {"path"},
	"delete":          {"path"},
	"make_dir":        {"path"},
	"rename":          {"path"},
	"copy":            {"source", "destination"},
	"move":            {"source", "destination"},
}

// toolTargetPaths resolves the files a call declares it may write, as absolute
// paths. declared is false for a tool that names no targets: its pre-image has
// to be the whole workspace.
func toolTargetPaths(name string, args map[string]any, ws Workspace, ig *IgnoreManager) (paths []string, declared bool) {
	keys, declared := pathArgs[name]
	if !declared {
		return nil, false
	}
	for _, key := range keys {
		value, _ := args[key].(string)
		if strings.TrimSpace(value) == "" {
			continue
		}
		if root, rel, err := ws.resolve(value, ig); err == nil {
			paths = append(paths, filepath.Join(root, filepath.FromSlash(rel)))
		}
	}
	// rename also overwrites a sibling inside the same root.
	if name == "rename" {
		if p, _ := args["path"].(string); strings.TrimSpace(p) != "" {
			if root, _, err := ws.resolve(p, ig); err == nil {
				if dest, _ := args["new_name"].(string); strings.TrimSpace(dest) != "" {
					paths = append(paths, filepath.Join(root, filepath.FromSlash(filepath.Clean(dest))))
				}
			}
		}
	}
	return paths, true
}
