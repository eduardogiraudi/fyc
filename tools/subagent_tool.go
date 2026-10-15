package tools

import (
	"context"
	"fmt"
	"fuckyourcli/mcp"
)

// executeSpawnSubagentsTool handles the "spawn_subagents" case.
//
// IMPORTANT: this case does executeMu.Unlock() at the start and
// defer executeMu.Lock() at the end. See the original in tools.go.
func executeSpawnSubagentsTool(ctx context.Context, args map[string]any, globalIgnore *IgnoreManager, restrictedRoot string, mcpManager *mcp.MCPManager) string {
	executeMu.Unlock()
	defer executeMu.Lock()
	rawAgents, ok := args["agents"].([]interface{})
	if !ok || len(rawAgents) == 0 {
		return "error: agents argument must be a non-empty array"
	}

	var agents []SubagentConfig
	for i, raw := range rawAgents {
		agent, ok := raw.(map[string]interface{})
		if !ok {
			return fmt.Sprintf("error: agent %d is not a valid object", i)
		}
		id, _ := agent["id"].(string)
		subdir, _ := agent["subdirectory"].(string)
		instructions, _ := agent["instructions"].(string)
		if id == "" || subdir == "" || instructions == "" {
			return fmt.Sprintf("error: agent %d: id, subdirectory and instructions are required", i)
		}

		allowedTools := extractStringList(agent, "tools")
		maxIter := 10
		if maxIterRaw, ok := agent["max_iterations"].(float64); ok {
			maxIter = int(maxIterRaw)
			if maxIter < 1 {
				maxIter = 1
			}
			if maxIter > 50 {
				maxIter = 50
			}
		}

		agents = append(agents, SubagentConfig{
			ID:            id,
			Subdirectory:  subdir,
			Instructions:  instructions,
			AllowedTools:  allowedTools,
			MaxIterations: maxIter,
		})
	}

	result := RunSubagents(ctx, agents, globalIgnore, restrictedRoot, mcpManager)
	return result
}

// SubagentConfig, RunSubagents, RunSubagent, SubagentBufferProvider,
// globalSubagentTools, SetGlobalSubagentTools are all defined in subagent.go.
