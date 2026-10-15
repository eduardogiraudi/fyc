package tools

import (
	"fuckyourcli/types"
)

// ── sub-agent tool policy ────────────────────────────────────────────────────
//
// Sub-agents run autonomously in parallel with a reduced, non-interactive
// toolset. Some tools must never be exposed to them:
//
//	spawn_subagents → sub-agents cannot spawn sub-sub-agents
//	computer        → concurrent desktop control is unsafe and screenshots
//	                  taken inside a sub-agent are not attached to the main
//	                  conversation
//	git_*           → repository-wide, irreversible-by-nature operations must
//	                  stay under the main agent's permission prompts
//
// This file is the single place where that policy lives: both the tool list
// handed to a sub-agent and the runtime dispatch are filtered through it.

// subagentAlwaysExcludedTools lists tool names that are never given to
// sub-agents regardless of what the spawning agent asks for.
var subagentAlwaysExcludedTools = map[string]bool{
	"spawn_subagents": true,
}

// SubagentToolExcluded reports whether a tool must be withheld from sub-agents.
func SubagentToolExcluded(name string) bool {
	if subagentAlwaysExcludedTools[name] {
		return true
	}
	if IsComputerTool(name) {
		return true
	}
	return IsGitTool(name)
}

// SubagentToolBlockReason returns the error message to feed back to a
// sub-agent that tried to call an excluded tool, or "" when the tool is
// allowed. It is the defense-in-depth counterpart of SubagentToolExcluded.
func SubagentToolBlockReason(name string) string {
	switch {
	case subagentAlwaysExcludedTools[name]:
		return "error: sub-agents cannot spawn sub-sub-agents"
	case IsComputerTool(name):
		return "error: computer control is not available inside sub-agents"
	case IsGitTool(name):
		return "error: git tools are not available inside sub-agents (git can only be driven by the main agent)"
	}
	return ""
}

// FilterToolsForSubagent returns the tool definitions a sub-agent may use.
// When allowed is empty every tool is offered, minus the excluded ones;
// otherwise only the requested names that are not excluded are offered.
func FilterToolsForSubagent(all []types.Tool, allowed []string) []types.Tool {
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	restricted := len(allowed) > 0

	out := make([]types.Tool, 0, len(all))
	for _, t := range all {
		name := t.Function.Name
		if SubagentToolExcluded(name) {
			continue
		}
		if restricted && !allowedSet[name] {
			continue
		}
		out = append(out, t)
	}
	return out
}
