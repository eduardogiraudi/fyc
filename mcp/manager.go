package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"fuckyourcli/mcpauth"
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
	secrets  mcpauth.Store
	// cancels ends the context a session's transport is bound to; nil until a
	// connection is established for that server.
	cancels map[string]context.CancelFunc
}

// NewMCPManager creates a new instance of MCPManager.
func NewMCPManager(version string, secrets ...mcpauth.Store) *MCPManager {
	m := &MCPManager{
		Sessions: make(map[string]*mcp.ClientSession),
		Clients:  make(map[string]*mcp.Client),
		cancels:  make(map[string]context.CancelFunc),
		version:  version,
	}
	if len(secrets) > 0 {
		m.secrets = secrets[0]
	}
	return m
}

// Initialize starts connections to the MCP servers defined in config.
// Supports transports: "stdio", "sse", "streamableHttp".
func (m *MCPManager) Initialize(ctx context.Context, configs []types.MCPServerConfig) error {
	m.mu.Lock()
	m.Configs = configs
	m.mu.Unlock()
	failures := make([]error, len(configs))
	seen := make(map[string]bool)
	var wg sync.WaitGroup
	for i, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		if seen[cfg.Name] {
			failures[i] = fmt.Errorf("duplicate MCP server name %s", cfg.Name)
			continue
		}
		seen[cfg.Name] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			transport, err := m.buildTransport(cfg)
			if err != nil {
				failures[i] = fmt.Errorf("MCP server %s: %w", cfg.Name, err)
				return
			}
			session, client, cancel, err := connectSession(ctx, transport, m.version, connectSetupTimeout)
			if err != nil {
				failures[i] = fmt.Errorf("failed to connect to MCP server %s: %w", cfg.Name, err)
				return
			}
			m.storeSession(cfg.Name, session, client, cancel)
		}()
	}
	wg.Wait()
	return errors.Join(failures...)
}

// connectSetupTimeout bounds the handshake only, never the session's lifetime.
const connectSetupTimeout = 30 * time.Second

// connectSession connects a client over transport and returns the session with
// the cancel func that ends its lifetime.
//
// The SDK ties the SSE hanging GET (and a stdio process) to the context handed
// to Connect, so cancelling that context after the handshake kills the session
// (streamable HTTP detaches it itself, SSE does not). The context is therefore
// detached from ctx and cancelled only by two watchers, both disarmed once the
// handshake completes: the caller's cancellation and the setup deadline.
func connectSession(ctx context.Context, transport mcp.Transport, version string, setupTimeout time.Duration) (*mcp.ClientSession, *mcp.Client, context.CancelFunc, error) {
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var mu sync.Mutex
	settingUp := true
	abortSetup := func() {
		mu.Lock()
		defer mu.Unlock()
		if settingUp {
			cancel()
		}
	}
	watchdog := time.AfterFunc(setupTimeout, abortSetup)
	stopParent := context.AfterFunc(ctx, abortSetup)
	client := mcp.NewClient(&mcp.Implementation{Name: "fuckyourcli", Version: version}, nil)
	session, err := client.Connect(lifetime, transport, nil)
	mu.Lock()
	settingUp = false
	mu.Unlock()
	watchdog.Stop()
	stopParent()

	switch {
	case ctx.Err() != nil:
		// The caller gave up while connecting: its error is the reason.
		if session != nil {
			session.Close()
		}
		cancel()
		return nil, nil, nil, ctx.Err()
	case lifetime.Err() != nil:
		// The watchdog fired: the handshake ran into the setup timeout.
		if session != nil {
			session.Close()
		}
		cancel()
		return nil, nil, nil, fmt.Errorf("connection setup timed out after %v", setupTimeout)
	case err != nil:
		cancel()
		return nil, nil, nil, err
	}
	return session, client, cancel, nil
}

// storeSession installs a new session for name, ending the previous one's
// transport context so a replaced connection cannot linger.
func (m *MCPManager) storeSession(name string, session *mcp.ClientSession, client *mcp.Client, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.cancels[name]; ok {
		old()
	}
	if m.cancels == nil {
		m.cancels = make(map[string]context.CancelFunc)
	}
	m.Sessions[name] = session
	m.Clients[name] = client
	m.cancels[name] = cancel
}

func (m *MCPManager) httpClient(cfg types.MCPServerConfig) *http.Client {
	base := httpClientWithHeaders(cfg.Headers)
	if m.secrets == nil {
		return base
	}
	return mcpauth.HTTPClient(base, cfg, m.secrets)
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

	sort.Strings(serverNames)
	results := make([][]types.Tool, len(serverNames))
	failures := make([]error, len(serverNames))
	var wg sync.WaitGroup
	for i, name := range serverNames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			listed, err := m.listToolsOnce(serverCtx, []string{name})
			if isEOFError(err) {
				if reconnectErr := m.reconnect(serverCtx, name); reconnectErr != nil {
					err = errors.Join(err, reconnectErr)
				} else {
					listed, err = m.listToolsOnce(serverCtx, []string{name})
				}
			}
			results[i], failures[i] = listed, err
		}()
	}
	wg.Wait()
	var allTools []types.Tool
	for _, listed := range results {
		allTools = append(allTools, listed...)
	}
	return allTools, errors.Join(failures...)
}

// listToolsOnce executes ListTools on all servers (without retry).
func (m *MCPManager) listToolsOnce(ctx context.Context, serverNames []string) ([]types.Tool, error) {
	var allTools []types.Tool
	for _, serverName := range serverNames {
		m.mu.RLock()
		session, ok := m.Sessions[serverName]
		m.mu.RUnlock()
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

	t, err := m.buildTransport(*cfg)
	if err != nil {
		return fmt.Errorf("build transport: %w", err)
	}

	session, client, cancel, err := connectSession(ctx, t, m.version, connectSetupTimeout)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	m.storeSession(serverName, session, client, cancel)

	return nil
}

// buildTransport builds the correct transport based on the config.
func buildTransport(cfg *types.MCPServerConfig) (mcp.Transport, error) {
	return (&MCPManager{}).buildTransport(*cfg)
}

func (m *MCPManager) buildTransport(cfg types.MCPServerConfig) (mcp.Transport, error) {
	transport := cfg.Transport
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Dir = cfg.WorkingDirectory
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
			HTTPClient: m.httpClient(cfg),
		}, nil
	case "streamableHttp":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("endpoint required for streamableHttp transport")
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: m.httpClient(cfg),
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
	for _, cancel := range m.cancels {
		cancel()
	}
	m.Sessions = make(map[string]*mcp.ClientSession)
	m.Clients = make(map[string]*mcp.Client)
	m.cancels = make(map[string]context.CancelFunc)
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
