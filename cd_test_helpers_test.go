package main

import (
	"context"

	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// newWorkspaceForTest builds the resolved workspace a session would hold.
func newWorkspaceForTest(primary string, extra []string) tools.Workspace {
	return tools.NewWorkspace(primary, extra)
}

// loadWorkspaceIgnoreForTest mirrors what setExtraRoots installs.
func loadWorkspaceIgnoreForTest(s *ChatSession) *tools.IgnoreManager {
	return tools.LoadWorkspaceIgnore(s.workspaceRoots())
}

// executeToolForTest runs one tool call through the real dispatcher.
func executeToolForTest(s *ChatSession, name string, args map[string]any) string {
	call := types.ToolCall{Function: types.ToolCallFunction{Name: name, Arguments: args}}
	return tools.ExecuteToolWithSpinner(context.Background(), call, s.GlobalIgnore, s.workspace(), nil).Text
}
