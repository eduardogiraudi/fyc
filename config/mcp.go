package config

import (
	"fuckyourcli/plugins"
	"fuckyourcli/store"
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

// SessionMCPServers merges approved plugin servers without persisting them in config.json.
func SessionMCPServers(workspace string) []types.MCPServerConfig {
	servers := append([]types.MCPServerConfig(nil), Get().MCPServers...)
	seen := map[string]bool{}
	for _, s := range servers {
		seen[s.Name] = true
	}
	for _, installed := range plugins.Load(workspace) {
		list, err := installed.Package.MCPServers(installed.Entry.Path, installed.DataDir(), ResolvePluginValue)
		if err != nil {
			store.Notice("plugins: %s MCP disabled: %v", installed.Entry.Name, err)
			continue
		}
		if len(list) > 0 {
			if err := store.EnsureDir(installed.DataDir(), store.DirPerm); err != nil {
				store.Notice("plugins: %v", err)
				continue
			}
		}
		for _, s := range list {
			if seen[s.Name] {
				store.Notice("plugins: MCP name collision %s; keeping explicit config", s.Name)
				continue
			}
			seen[s.Name] = true
			servers = append(servers, s)
		}
	}
	return servers
}
