package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"fuckyourcli/types"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPManager manages the lifecycle of MCP servers and communication.
type MCPManager struct {
	Sessions map[string]*mcp.ClientSession
	Clients  map[string]*mcp.Client
	Configs  []types.MCPServerConfig // saved for reconnections
	version  string
	mu       sync.RWMutex
}

// NewMCPManager creates a new instance of MCPManager.
func NewMCPManager(version string) *MCPManager {
	return &MCPManager{
		Sessions: make(map[string]*mcp.ClientSession),
		Clients:  make(map[string]*mcp.Client),
		Configs:  nil,
		version:  version,
	}
}

// Initialize starts connections to the MCP servers defined in config.
// Supports transports: "stdio", "sse", "streamableHttp".
func (m *MCPManager) Initialize(ctx context.Context, configs []types.MCPServerConfig) error {
	// Save configs for reconnect
	m.Configs = configs

	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		transport := cfg.Transport
		if transport == "" {
			transport = "stdio" // default backward compatible
		}

		var t mcp.Transport

		client := mcp.NewClient(&mcp.Implementation{
			Name:    "fuckyourcli",
			Version: m.version,
		}, nil)

		switch transport {
		case "stdio":
			cmd := exec.Command(cfg.Command, cfg.Args...)
			if len(cfg.Env) > 0 {
				cmd.Env = os.Environ()
				for k, v := range cfg.Env {
					cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
				}
			}
			t = &mcp.CommandTransport{
				Command: cmd,
			}

		case "sse":
			if cfg.Endpoint == "" {
				return fmt.Errorf("MCP server %s: endpoint required for SSE transport", cfg.Name)
			}
			t = &mcp.SSEClientTransport{
				Endpoint:   cfg.Endpoint,
				HTTPClient: httpClientWithHeaders(cfg.Headers),
			}

		case "streamableHttp":
			if cfg.Endpoint == "" {
				return fmt.Errorf("MCP server %s: endpoint required for streamableHttp transport", cfg.Name)
			}
			t = &mcp.StreamableClientTransport{
				Endpoint:   cfg.Endpoint,
				HTTPClient: httpClientWithHeaders(cfg.Headers),
			}

		default:
			return fmt.Errorf("MCP server %s: unknown transport %q", cfg.Name, transport)
		}

		session, err := client.Connect(ctx, t, nil)
		if err != nil {
			return fmt.Errorf("failed to connect to MCP server %s: %w", cfg.Name, err)
		}

		m.mu.Lock()
		m.Sessions[cfg.Name] = session
		m.Clients[cfg.Name] = client
		m.mu.Unlock()
	}
	return nil
}

// httpClientWithHeaders creates an http.Client that adds custom headers to every request.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil // use default client
	}
	return &http.Client{
		Transport: &headerRoundTripper{
			headers: headers,
			next:    http.DefaultTransport,
		},
	}
}

// headerRoundTripper is an http.RoundTripper that adds fixed headers to every request.
type headerRoundTripper struct {
	headers map[string]string
	next    http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.next.RoundTrip(req)
}

// ListTools retrieves tools from all connected MCP servers.
func (m *MCPManager) ListTools(ctx context.Context) ([]types.Tool, error) {
	// First try without lock to collect server names
	m.mu.RLock()
	serverNames := make([]string, 0, len(m.Sessions))
	for name := range m.Sessions {
		serverNames = append(serverNames, name)
	}
	m.mu.RUnlock()

	allTools, err := m.listToolsOnce(ctx, serverNames)
	if err != nil && isEOFError(err) {
		// Try to reconnect all servers that failed
		for _, name := range serverNames {
			m.reconnect(ctx, name)
		}
		return m.listToolsOnce(ctx, serverNames)
	}
	return allTools, err
}

// listToolsOnce executes ListTools on all servers (without retry).
func (m *MCPManager) listToolsOnce(ctx context.Context, serverNames []string) ([]types.Tool, error) {
	var allTools []types.Tool
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, serverName := range serverNames {
		session, ok := m.Sessions[serverName]
		if !ok {
			continue // session just removed, skip
		}
		params := &mcp.ListToolsParams{}
		result, err := session.ListTools(ctx, params)
		if err != nil {
			// wrap with serverName for isEOFError
			return nil, fmt.Errorf("failed to list tools for server %s: %w", serverName, err)
		}

		for _, tool := range result.Tools {
			// Prefix the tool name with the server name to avoid collisions
			// Use "__" as a separator for OpenAI compatibility (pattern: ^[a-zA-Z0-9_-]+$)
			prefixedName := fmt.Sprintf("%s__%s", serverName, tool.Name)

			t := types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        prefixedName,
					Description: tool.Description,
				},
			}

			// Map InputSchema to ToolParameters
			if schemaMap, ok := tool.InputSchema.(map[string]any); ok {
				params := types.ToolParameters{
					Type:       "object",
					Properties: make(map[string]types.ToolProperty),
				}

				if props, ok := schemaMap["properties"].(map[string]any); ok {
					for k, v := range props {
						if propMap, ok := v.(map[string]any); ok {
							tp := types.ToolProperty{}
							if t, ok := propMap["type"].(string); ok {
								tp.Type = t
							}
							if d, ok := propMap["description"].(string); ok {
								tp.Description = d
							}
							params.Properties[k] = tp
						}
					}
				}

				if req, ok := schemaMap["required"].([]any); ok {
					for _, r := range req {
						if rs, ok := r.(string); ok {
							params.Required = append(params.Required, rs)
						}
					}
				}
				t.Function.Parameters = params
			}

			allTools = append(allTools, t)
		}
	}
	return allTools, nil
}

// isEOFError checks if the error indicates a "poisoned" MCP session.
func isEOFError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") || strings.Contains(msg, "client is closing")
}

// callToolOnce executes a single tool call (without retry).
func (m *MCPManager) callToolOnce(ctx context.Context, serverName, toolName string, arguments map[string]any) (string, error) {
	m.mu.RLock()
	session, ok := m.Sessions[serverName]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("MCP server %s not found", serverName)
	}
	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}
	result, err := session.CallTool(ctx, params)
	if err != nil {
		return "", fmt.Errorf("MCP server %s: %w", serverName, err)
	}
	return flattenContent(result.Content), nil
}

// CallTool executes a tool on a specific MCP server, with automatic retry on EOF.
func (m *MCPManager) CallTool(ctx context.Context, prefixedName string, arguments map[string]any) (string, error) {
	// Separate the server name from the tool name
	parts := strings.SplitN(prefixedName, "__", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid MCP tool name: %s", prefixedName)
	}
	serverName := parts[0]
	toolName := parts[1]

	result, err := m.callToolOnce(ctx, serverName, toolName, arguments)
	if err != nil && isEOFError(err) {
		// Poisoned session: reconnect and retry
		if reconnErr := m.reconnect(ctx, serverName); reconnErr != nil {
			return "", fmt.Errorf("MCP server %s call failed after reconnect error: %w (original: %v)", serverName, reconnErr, err)
		}
		return m.callToolOnce(ctx, serverName, toolName, arguments)
	}
	return result, err
}

// reconnect closes the current session and creates a new one for serverName.
func (m *MCPManager) reconnect(ctx context.Context, serverName string) error {
	m.mu.RLock()
	oldSession, hasSession := m.Sessions[serverName]
	client := m.Clients[serverName]
	// Look for the config for this server
	var cfg *types.MCPServerConfig
	for i := range m.Configs {
		if m.Configs[i].Name == serverName {
			cfg = &m.Configs[i]
			break
		}
	}
	m.mu.RUnlock()

	if cfg == nil {
		return fmt.Errorf("server %s not found in configs", serverName)
	}

	// Close old session
	if hasSession {
		oldSession.Close()
	}

	client = mcp.NewClient(&mcp.Implementation{
		Name:    "fuckyourcli",
		Version: m.version,
	}, nil)

	t, err := buildTransport(cfg)
	if err != nil {
		return fmt.Errorf("build transport: %w", err)
	}

	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	m.mu.Lock()
	m.Sessions[serverName] = session
	m.Clients[serverName] = client
	m.mu.Unlock()

	return nil
}

// buildTransport builds the correct transport based on the config.
func buildTransport(cfg *types.MCPServerConfig) (mcp.Transport, error) {
	transport := cfg.Transport
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if len(cfg.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range cfg.Env {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
			}
		}
		return &mcp.CommandTransport{Command: cmd}, nil
	case "sse":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("endpoint required for SSE transport")
		}
		return &mcp.SSEClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: httpClientWithHeaders(cfg.Headers),
		}, nil
	case "streamableHttp":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("endpoint required for streamableHttp transport")
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: httpClientWithHeaders(cfg.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", transport)
	}
}

// flattenContent flattens the contents of CallToolResult into a string.
func flattenContent(contents []mcp.Content) string {
	var output strings.Builder
	for _, content := range contents {
		switch c := content.(type) {
		case *mcp.TextContent:
			output.WriteString(c.Text)
		case *mcp.ImageContent:
			output.WriteString(fmt.Sprintf("[Image: %s]", c.MIMEType))
		case *mcp.EmbeddedResource:
			output.WriteString(fmt.Sprintf("[Embedded Resource: %s]", c.Resource.URI))
		default:
			data, _ := json.Marshal(content)
			output.Write(data)
		}
	}
	return output.String()
}

// ListResources retrieves all available static resources from the servers.
func (m *MCPManager) ListResources(ctx context.Context) (map[string][]mcp.Resource, error) {
	resources := make(map[string][]mcp.Resource)
	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, session := range m.Sessions {
		res, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
		if err != nil {
			continue // Some servers might not support resources
		}

		// Convert []*mcp.Resource to []mcp.Resource
		var list []mcp.Resource
		for _, r := range res.Resources {
			if r != nil {
				list = append(list, *r)
			}
		}
		resources[name] = list
	}
	return resources, nil
}

// ListResourceTemplates retrieves dynamic resource templates from the servers.
func (m *MCPManager) ListResourceTemplates(ctx context.Context) (map[string][]mcp.ResourceTemplate, error) {
	templates := make(map[string][]mcp.ResourceTemplate)
	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, session := range m.Sessions {
		res, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
		if err != nil {
			continue
		}

		// Convert []*mcp.ResourceTemplate to []mcp.ResourceTemplate
		var list []mcp.ResourceTemplate
		for _, t := range res.ResourceTemplates {
			if t != nil {
				list = append(list, *t)
			}
		}
		templates[name] = list
	}
	return templates, nil
}

// ReadResource reads the content of a specific resource.
func (m *MCPManager) ReadResource(ctx context.Context, serverName string, uri string) (string, error) {
	m.mu.RLock()
	session, ok := m.Sessions[serverName]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("MCP server %s not found", serverName)
	}

	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: uri,
	})
	if err != nil {
		return "", err
	}

	var output strings.Builder
	for _, content := range result.Contents {
		if content.Text != "" {
			output.WriteString(content.Text)
		} else if len(content.Blob) > 0 {
			output.WriteString(fmt.Sprintf("[Binary data, MIME: %s]", content.MIMEType))
		}
	}

	return output.String(), nil
}

// Shutdown closes all active MCP sessions.
func (m *MCPManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.Sessions {
		session.Close()
	}
	m.Sessions = make(map[string]*mcp.ClientSession)
}

// IsMCPTool checks if a tool belongs to an MCP server (based on the prefix).
func (m *MCPManager) IsMCPTool(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for serverName := range m.Sessions {
		if strings.HasPrefix(name, serverName+"__") {
			return true
		}
	}
	return false
}

// HasSessions reports whether any MCP server session is currently connected.
func (m *MCPManager) HasSessions() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.Sessions) > 0
}
