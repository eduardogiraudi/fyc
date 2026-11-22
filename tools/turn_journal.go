package tools

import "sync"

// TurnJournal is the workspace snapshot the caller installs: after every tool
// that can touch the disk it records what changed, so /undo can put the files
// back. The interface lives here only so the dispatcher can reach it without
// importing the session; the journal itself belongs to the caller.
type TurnJournal interface {
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
	"read_mcp_resource": true, "list_mcp_resources": true, "list_mcp_resource_templates": true,
	"lsp_active_servers": true, "lsp_hover": true, "lsp_definition": true,
	"lsp_references": true, "lsp_diagnostics": true, "lsp_workspace_symbols": true,
	"git_status": true, "git_diff": true, "git_log": true, "git_show": true,
	"git_blame": true, "git_ls_files": true, "git_config": true,
}

// toolMayWrite reports whether the workspace may have changed after a call of
// this tool. Unknown names (MCP tools) are assumed to have written.
func toolMayWrite(name string) bool { return !readOnlyTools[name] }
