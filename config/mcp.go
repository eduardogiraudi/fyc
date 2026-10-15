package config

import (
	"fuckyourcli/types"
)

// AddMCPServer adds a new MCP server
func AddMCPServer(name, command string, args []string, env map[string]string) {
	server := types.MCPServerConfig{
		Name:    name,
		Command: command,
		Args:    args,
		Env:     env,
	}
	config.MCPServers = append(config.MCPServers, server)
}

// RemoveMCPServer removes an MCP server by name
func RemoveMCPServer(name string) {
	var newServers []types.MCPServerConfig
	for _, s := range config.MCPServers {
		if s.Name != name {
			newServers = append(newServers, s)
		}
	}
	config.MCPServers = newServers
}
