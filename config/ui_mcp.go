package config

import (
	"fmt"
	"fuckyourcli/types"
	"fuckyourcli/ui"
	"strings"
)

// manageMCPServers shows the MCP server management menu.
func manageMCPServers() {
	idx := ui.SelectOption("Manage MCP Servers", []string{
		"Add new MCP Server",
		"Remove MCP Server",
		"List configured MCP Servers",
		"Enable/Disable MCP Servers",
		"Back",
	})

	switch idx {
	case 0:
		setupNewMCPServer()
	case 1:
		removeMCPServer()
	case 2:
		listMCPServers()
	case 3:
		toggleMCPServers()
	case 4, ui.SelectorCanceled:
		// "Back" or dismissed (q / Ctrl+C / Esc): nothing to do, return to the
		// configuration menu.
		return
	}
}

// removeMCPServer asks which server to remove and deletes it from the config.
func removeMCPServer() {
	if len(config.MCPServers) == 0 {
		fmt.Println(ui.InfoStyle.Render("No MCP servers configured."))
		return
	}
	names := mcpServerNames()
	idx := ui.SelectOption("Select server to remove", names)
	if idx < 0 {
		return // dismissed (q / Ctrl+C / Esc)
	}
	RemoveMCPServer(names[idx])
	Save()
	fmt.Println(ui.OkStyle.Render("Server removed."))
}

// listMCPServers prints the configured MCP servers.
func listMCPServers() {
	if len(config.MCPServers) == 0 {
		fmt.Println(ui.InfoStyle.Render("No MCP servers configured."))
		ui.InputText("Press Enter to continue...")
		return
	}
	fmt.Println(ui.TitleStyle.Render("Configured MCP Servers:"))
	for _, s := range config.MCPServers {
		transport := s.Transport
		if transport == "" {
			transport = "stdio"
		}
		if transport == "stdio" {
			fmt.Printf("- %s (%s): %s %s\n", s.Name, transport, s.Command, strings.Join(s.Args, " "))
		} else {
			fmt.Printf("- %s (%s): %s\n", s.Name, transport, s.Endpoint)
		}
	}
	ui.InputText("Press Enter to continue...")
}

// toggleMCPServers shows a multi-select to enable/disable MCP servers.
func toggleMCPServers() {
	if len(config.MCPServers) == 0 {
		fmt.Println(ui.InfoStyle.Render("No MCP servers configured."))
		return
	}

	labels := make([]string, len(config.MCPServers))
	states := make([]bool, len(config.MCPServers))
	for i, s := range config.MCPServers {
		transport := s.Transport
		if transport == "" || transport == "stdio" {
			transport = "stdio"
		}
		labels[i] = fmt.Sprintf("%s (%s)", s.Name, transport)
		states[i] = !s.Disabled
	}
	labels = append(labels, "← Done")
	states = append(states, true)

	newStates := ui.MultiSelectOption("Enable/Disable MCP Servers", labels, states)
	if len(newStates) == 0 {
		return
	}

	changed := false
	for i := range config.MCPServers {
		if config.MCPServers[i].Disabled == newStates[i] { // Disabled != newEnabled
			config.MCPServers[i].Disabled = !newStates[i]
			changed = true
		}
	}

	if changed {
		Save()
		fmt.Println(ui.OkStyle.Render("MCP server states updated."))
	} else {
		fmt.Println(ui.InfoStyle.Render("No changes made."))
	}
}

// setupNewMCPServer guides the user through the configuration of a new MCP server.
func setupNewMCPServer() {
	fmt.Println(ui.TitleStyle.Render("Setup New MCP Server"))

	name := ui.InputText("Enter server name (e.g., 'sqlite', 'github'):")
	if name == "" {
		return
	}

	transports := []string{"stdio", "sse", "streamableHttp"}
	transportIdx := ui.SelectOption("Select transport type", transports)
	if transportIdx < 0 {
		return // dismissed (q / Ctrl+C / Esc)
	}
	transport := transports[transportIdx]

	if transport == "sse" || transport == "streamableHttp" {
		setupHTTPMCPServer(name, transport)
	} else {
		setupStdioMCPServer(name)
	}
}

// setupHTTPMCPServer configures an MCP server with HTTP transport (SSE or streamableHttp).
func setupHTTPMCPServer(name, transport string) {
	endpoint := ui.InputText("Enter MCP server endpoint URL (e.g., http://localhost:3000/mcp):")
	if endpoint == "" {
		fmt.Println(ui.InfoStyle.Render("Endpoint cannot be empty."))
		return
	}

	headers := collectHTTPHeaders()

	config.MCPServers = append(config.MCPServers, types.MCPServerConfig{
		Name:      name,
		Transport: transport,
		Endpoint:  endpoint,
		Headers:   headers,
	})
	Save()
	fmt.Println(ui.OkStyle.Render("MCP Server added successfully!"))
}

// collectHTTPHeaders asks the user if they want to add authorization headers.
func collectHTTPHeaders() map[string]string {
	headers := make(map[string]string)
	choice := ui.SelectOption("Add authorization header?", []string{
		"No",
		"Yes, Bearer token",
		"Yes, custom header",
	})
	switch choice {
	case 1:
		token := ui.InputPassword("Enter Bearer token:")
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
	case 2:
		key := ui.InputText("Enter header name:")
		if key != "" {
			value := ui.InputText(fmt.Sprintf("Enter value for %s:", key))
			if value != "" {
				headers[key] = value
			}
		}
	}
	return headers
}

// setupStdioMCPServer configures an MCP server with stdio transport.
func setupStdioMCPServer(name string) {
	command := ui.InputText("Enter command (e.g., 'npx', 'python3'):")
	if command == "" {
		return
	}

	argsStr := ui.InputText("Enter arguments (separated by space):")
	args := strings.Fields(argsStr)

	env := make(map[string]string)
	if ui.SelectOption("Add environment variables?", []string{"No", "Yes"}) == 1 {
		env = collectEnvVars()
	}

	AddMCPServer(name, command, args, env)
	Save()
	fmt.Println(ui.OkStyle.Render("MCP Server added successfully!"))
}

// collectEnvVars collects key/value pairs of environment variables in a loop.
func collectEnvVars() map[string]string {
	env := make(map[string]string)
	for {
		key := ui.InputText("Enter env variable name (or leave empty to finish):")
		if key == "" {
			break
		}
		env[key] = ui.InputText(fmt.Sprintf("Enter value for %s:", key))
	}
	return env
}

// mcpServerNames returns the names of all configured MCP servers.
func mcpServerNames() []string {
	names := make([]string, len(config.MCPServers))
	for i, s := range config.MCPServers {
		names[i] = s.Name
	}
	return names
}
