package tools

import (
	"context"
	"fmt"
	"fuckyourcli/mcp"
	"strings"
)

// executeMCPPrefixCheck handles the MCP tool prefix check (first block in
// ExecuteToolWithSpinner).
// Returns (handled, resultString). If handled is true, the result is final.
func executeMCPPrefixCheck(ctx context.Context, name string, args map[string]any, mcpManager *mcp.MCPManager) (bool, string) {
	if mcpManager == nil || !mcpManager.IsMCPTool(name) {
		return false, ""
	}

	permRes, suggestion := askPermission(ctx,
		fmt.Sprintf("MCP tool: %s\nArgs: %v", name, args),
		[]string{"Allow", "Deny"},
	)
	if permRes == PermissionDeny {
		return true, "error: MCP tool call denied"
	}
	if permRes == PermissionDenyWithSuggestion {
		msg := "error: MCP tool call denied"
		if suggestion != "" {
			msg += ": " + suggestion
		}
		return true, msg
	}

	res, err := mcpManager.CallTool(ctx, name, args)
	if err != nil {
		return true, fmt.Sprintf("MCP error: %v", err)
	}
	return true, res
}

// executeListMCPResources handles the "list_mcp_resources" case.
func executeListMCPResources(mcpManager *mcp.MCPManager) string {
	if mcpManager == nil {
		return "error: MCP manager not initialized"
	}
	res, err := mcpManager.ListResources(context.Background())
	if err != nil {
		return fmt.Sprintf("error listing MCP resources: %v", err)
	}
	var sb strings.Builder
	for server, resources := range res {
		sb.WriteString(fmt.Sprintf("Server: %s\n", server))
		for _, r := range resources {
			sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", r.Name, r.URI, r.Description))
		}
		sb.WriteString("\n")
	}
	out := sb.String()
	if out == "" {
		return "No MCP resources found"
	}
	return out
}

// executeListMCPResourceTemplates handles the "list_mcp_resource_templates" case.
func executeListMCPResourceTemplates(mcpManager *mcp.MCPManager) string {
	if mcpManager == nil {
		return "error: MCP manager not initialized"
	}
	templates, err := mcpManager.ListResourceTemplates(context.Background())
	if err != nil {
		return fmt.Sprintf("error listing MCP resource templates: %v", err)
	}
	var sb strings.Builder
	for server, tps := range templates {
		sb.WriteString(fmt.Sprintf("Server: %s\n", server))
		for _, t := range tps {
			sb.WriteString(fmt.Sprintf("- %s (Template: %s): %s\n", t.Name, t.URITemplate, t.Description))
		}
		sb.WriteString("\n")
	}
	out := sb.String()
	if out == "" {
		return "No MCP resource templates found"
	}
	return out
}

// executeReadMCPResource handles the "read_mcp_resource" case.
func executeReadMCPResource(args map[string]any, mcpManager *mcp.MCPManager) string {
	if mcpManager == nil {
		return "error: MCP manager not initialized"
	}
	server, _ := args["server"].(string)
	uri, _ := args["uri"].(string)
	if server == "" || uri == "" {
		return "error: server and uri arguments required"
	}
	content, err := mcpManager.ReadResource(context.Background(), server, uri)
	if err != nil {
		return fmt.Sprintf("error reading MCP resource: %v", err)
	}
	return content
}
