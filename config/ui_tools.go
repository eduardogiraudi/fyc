package config

import (
	"fuckyourcli/ui"
)

// defaultToolNames is the canonical list of manageable tools.
// Adding a new tool here is the only modification needed.
var defaultToolNames = []string{
	"web_search", "context_snapshot",
	"ls", "grep", "read", "get_anchors", "replace_anchors",
	"write", "delete", "make_dir", "sh", "kill", "sniff",
	"list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource",
	"skills_discovery", "read_skill", "read_user_preferences", "update_user_preferences",
	"questionary", "copy", "rename", "move",
	"lsp_active_servers", "lsp_hover", "lsp_definition", "lsp_references",
	"lsp_diagnostics", "lsp_workspace_symbols",
	"spawn_subagents", "computer", "read_media",
}

// toolsDisabledByDefault contains tools that start disabled
// when there is no saved state yet.
var toolsDisabledByDefault = map[string]bool{
	"spawn_subagents": true,
	"computer":        true,
	"read_media":      true,
}

// init registers git tools (config/git.go) without duplicating lists: each
// git tool that can mutate the repository starts disabled, only read-only
// ones start enabled.
func init() {
	defaultToolNames = append(defaultToolNames, GitToolNames()...)
	for _, name := range GitDisabledByDefaultNames() {
		toolsDisabledByDefault[name] = true
	}
}

// ToggleTools shows a multi-select to enable/disable tools.
// If toolNames is nil or empty, it uses the default list.
// Returns true if the configuration has changed and a UI message to be shown via sink.
func ToggleTools(toolNames []string) (bool, string) {
	if len(toolNames) == 0 {
		toolNames = defaultToolNames
	}

	labels, states := buildToolSelections(toolNames)
	labels = append(labels, "← Done")
	states = append(states, true)

	newStates := ui.MultiSelectOption("Enable/Disable Tools", labels, states)
	if len(newStates) == 0 {
		return false, ""
	}

	newByName := buildNewToolStateMap(toolNames, newStates)
	applyToolDependencies(newByName)
	changed := persistToolStates(newByName)
	if changed {
		return true, ui.OkStyle.Render("Tool states updated.")
	}
	return false, ui.InfoStyle.Render("No changes made.")
}

// buildToolSelections builds labels and initial states for the multi-select.
func buildToolSelections(toolNames []string) (labels []string, states []bool) {
	saved := GetToolStates()
	labels = make([]string, 0, len(toolNames))
	states = make([]bool, 0, len(toolNames))
	for _, name := range toolNames {
		enabled, ok := saved[name]
		if !ok {
			enabled = !toolsDisabledByDefault[name]
		}
		labels = append(labels, name)
		states = append(states, enabled)
	}
	return
}

// buildNewToolStateMap maps name -> new state after user selection.
func buildNewToolStateMap(toolNames []string, newStates []bool) map[string]bool {
	m := make(map[string]bool, len(toolNames))
	for i, name := range toolNames {
		m[name] = newStates[i]
	}
	return m
}

// applyToolDependencies applies dependencies between tools.
// Currently: disabling get_anchors forces the disabling of replace_anchors.
func applyToolDependencies(m map[string]bool) {
	if enabled, ok := m["get_anchors"]; ok && !enabled {
		m["replace_anchors"] = false
	}
}

// persistToolStates saves the updated states in config and returns true if
// something has changed.
func persistToolStates(newByName map[string]bool) bool {
	if config.ToolStates == nil {
		config.ToolStates = make(map[string]bool)
	}
	changed := false
	for name, enabled := range newByName {
		if config.ToolStates[name] != enabled {
			config.ToolStates[name] = enabled
			changed = true
		}
	}
	if changed {
		Save()
	}
	return changed
}
