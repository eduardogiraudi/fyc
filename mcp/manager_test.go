package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fuckyourcli/types"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func managerTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func managerForTest(t *testing.T) *MCPManager {
	t.Helper()
	manager := NewMCPManager("test-version")
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	return manager
}

func connectMemoryServer(t *testing.T, ctx context.Context, manager *MCPManager, name string, server *sdk.Server) *sdk.ServerSession {
	t.Helper()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.Sessions[name] = clientSession
	manager.Clients[name] = client
	return serverSession
}

func echoServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "test-server", Version: "test"}, nil)
	server.AddTool(&sdk.Tool{
		Name:        "echo__path",
		Description: "Echo a path",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path to echo"},
			},
			"required": []string{"path"},
		},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprint(args["path"])}}}, nil
	})
	return server
}

func serveHTTPServer(t *testing.T, server *sdk.Server, transport string) string {
	t.Helper()
	getServer := func(*http.Request) *sdk.Server { return server }
	var handler http.Handler
	if transport == "sse" {
		handler = sdk.NewSSEHandler(getServer, nil)
	} else {
		handler = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{JSONResponse: true})
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

func requireErrorContains(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("error = %v, want it to contain %q", err, text)
	}
}

func TestNewMCPManagerAndEmptyOperations(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	if manager.version != "test-version" || manager.Sessions == nil || manager.Clients == nil || manager.Configs != nil {
		t.Fatalf("unexpected initial state: %+v", manager)
	}
	if manager.HasSessions() || manager.IsMCPTool("server__echo") {
		t.Fatal("empty manager reports an active session/tool")
	}
	tools, err := manager.ListTools(ctx)
	if err != nil || len(tools) != 0 {
		t.Fatalf("ListTools = %v, %v", tools, err)
	}
	resources, err := manager.ListResources(ctx)
	if err != nil || resources == nil || len(resources) != 0 {
		t.Fatalf("ListResources = %v, %v", resources, err)
	}
	templates, err := manager.ListResourceTemplates(ctx)
	if err != nil || templates == nil || len(templates) != 0 {
		t.Fatalf("ListResourceTemplates = %v, %v", templates, err)
	}
	for _, tc := range []struct{ name, message string }{
		{"echo", "invalid MCP tool name: echo"},
		{"missing__echo", "MCP server missing not found"},
	} {
		result, err := manager.CallTool(ctx, tc.name, nil)
		requireErrorContains(t, err, tc.message)
		if result != "" {
			t.Fatalf("failed call returned %q", result)
		}
	}
	_, err = manager.ReadResource(ctx, "missing", "test:///doc")
	requireErrorContains(t, err, "MCP server missing not found")
	manager.Shutdown(ctx)
	manager.Shutdown(ctx)
	if manager.Sessions == nil || manager.HasSessions() {
		t.Fatal("shutdown must leave an initialized, empty session map")
	}
}

func TestBuildTransport(t *testing.T) {
	t.Setenv("FYC_MCP_TEST_INHERITED", "inherited")
	t.Setenv("FYC_MCP_TEST_OVERRIDE", "old")
	for _, transport := range []string{"", "stdio"} {
		t.Run("stdio_"+transport, func(t *testing.T) {
			cfg := types.MCPServerConfig{Transport: transport, Command: "test-command", Args: []string{"one", "two"}, Env: map[string]string{"FYC_MCP_TEST_OVERRIDE": "new"}}
			built, err := buildTransport(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			cmdTransport, ok := built.(*sdk.CommandTransport)
			if !ok {
				t.Fatalf("transport type = %T", built)
			}
			if !reflect.DeepEqual(cmdTransport.Command.Args, []string{"test-command", "one", "two"}) {
				t.Fatalf("command args = %v", cmdTransport.Command.Args)
			}
			env := strings.Join(cmdTransport.Command.Environ(), "\n")
			if !strings.Contains(env, "FYC_MCP_TEST_INHERITED=inherited") || !strings.Contains(env, "FYC_MCP_TEST_OVERRIDE=new") || strings.Contains(env, "FYC_MCP_TEST_OVERRIDE=old") {
				t.Fatalf("inheritance/override failed: %q", env)
			}
		})
	}
	built, err := buildTransport(&types.MCPServerConfig{Command: "test-command"})
	if err != nil || built.(*sdk.CommandTransport).Command.Env != nil {
		t.Fatalf("stdio without custom env = %v, %v", built, err)
	}
	for _, transport := range []string{"sse", "streamableHttp"} {
		t.Run(transport, func(t *testing.T) {
			cfg := types.MCPServerConfig{Transport: transport, Endpoint: "https://example.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer test"}}
			built, err := buildTransport(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			var endpoint string
			var client *http.Client
			switch tr := built.(type) {
			case *sdk.SSEClientTransport:
				if transport != "sse" {
					t.Fatalf("unexpected transport %T", built)
				}
				endpoint, client = tr.Endpoint, tr.HTTPClient
			case *sdk.StreamableClientTransport:
				if transport != "streamableHttp" {
					t.Fatalf("unexpected transport %T", built)
				}
				endpoint, client = tr.Endpoint, tr.HTTPClient
			default:
				t.Fatalf("unexpected transport %T", built)
			}
			if endpoint != cfg.Endpoint || client == nil {
				t.Fatalf("endpoint/client = %q, %v", endpoint, client)
			}
			roundTripper, ok := client.Transport.(*headerRoundTripper)
			if !ok || !reflect.DeepEqual(roundTripper.headers, cfg.Headers) {
				t.Fatalf("headers transport = %#v", client.Transport)
			}
		})
	}
}

func TestTransportValidationAndDisabledServers(t *testing.T) {
	for _, tc := range []struct {
		transport string
		message   string
	}{
		{"sse", "endpoint required for SSE transport"},
		{"streamableHttp", "endpoint required for streamableHttp transport"},
		{"invalid", `unknown transport "invalid"`},
	} {
		t.Run(tc.transport, func(t *testing.T) {
			ctx := managerTestContext(t)
			manager := managerForTest(t)
			cfg := types.MCPServerConfig{Name: "broken", Transport: tc.transport}
			_, err := buildTransport(&cfg)
			requireErrorContains(t, err, tc.message)
			err = manager.Initialize(ctx, []types.MCPServerConfig{cfg})
			requireErrorContains(t, err, "MCP server broken: "+tc.message)
			if manager.HasSessions() {
				t.Fatal("invalid config created a session")
			}
			cfg.Disabled = true
			if err := manager.Initialize(ctx, []types.MCPServerConfig{cfg}); err != nil {
				t.Fatalf("disabled server was not skipped: %v", err)
			}
			if manager.HasSessions() || !reflect.DeepEqual(manager.Configs, []types.MCPServerConfig{cfg}) {
				t.Fatal("disabled config was not saved/skipped")
			}
		})
	}
}

type managerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f managerRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHTTPClientWithHeaders(t *testing.T) {
	if httpClientWithHeaders(nil) != nil || httpClientWithHeaders(map[string]string{}) != nil {
		t.Fatal("no headers should use the default client")
	}
	client := httpClientWithHeaders(map[string]string{"Authorization": "Bearer test", "X-Mcp-Test": "custom"})
	roundTripper := client.Transport.(*headerRoundTripper)
	response := &http.Response{StatusCode: http.StatusAccepted}
	sentinel := errors.New("transport failed")
	for _, nextErr := range []error{nil, sentinel} {
		var called bool
		roundTripper.next = managerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			if req.Header.Get("Authorization") != "Bearer test" || req.Header.Get("X-Mcp-Test") != "custom" || req.Header.Get("Accept") != "application/json" {
				t.Fatalf("request headers = %v", req.Header)
			}
			return response, nextErr
		})
		req := httptest.NewRequest(http.MethodPost, "http://example.invalid/mcp", nil)
		req.Header.Set("Authorization", "old")
		req.Header.Set("Accept", "application/json")
		got, err := roundTripper.RoundTrip(req)
		if !called || got != response || err != nextErr {
			t.Fatalf("delegation = %v, %v, called %v", got, err, called)
		}
	}
}

func TestIsEOFError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"eof", io.EOF, true},
		{"wrapped_eof", fmt.Errorf("list tools: %w", io.EOF), true},
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"closing", errors.New("MCP server test: client is closing"), true},
		{"other", errors.New("permission denied"), false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEOFError(tc.err); got != tc.want {
				t.Fatalf("isEOFError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFlattenContent(t *testing.T) {
	audio := &sdk.AudioContent{MIMEType: "audio/wav", Data: []byte{1, 2}}
	audioJSON, err := json.Marshal(audio)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		contents []sdk.Content
		want     string
	}{
		{"empty", nil, ""},
		{"text", []sdk.Content{&sdk.TextContent{Text: "one"}, &sdk.TextContent{Text: "two"}}, "onetwo"},
		{"mixed", []sdk.Content{
			&sdk.TextContent{Text: "before"},
			&sdk.ImageContent{MIMEType: "image/png", Data: []byte{1}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "test:///doc", Text: "document"}},
			&sdk.TextContent{Text: "after"},
		}, "before[Image: image/png][Embedded Resource: test:///doc]after"},
		{"fallback", []sdk.Content{audio}, string(audioJSON)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := flattenContent(tc.contents); got != tc.want {
				t.Fatalf("flattenContent = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListToolsMapsSchemaAndNamespacesServers(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	for _, name := range []string{"alpha", "alpha2"} {
		connectMemoryServer(t, ctx, manager, name, echoServer())
	}
	listed, err := manager.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d tools, want 2", len(listed))
	}
	byName := make(map[string]types.Tool)
	for _, tool := range listed {
		byName[tool.Function.Name] = tool
	}
	for _, name := range []string{"alpha", "alpha2"} {
		want := types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        name + "__echo__path",
				Description: "Echo a path",
				Parameters: types.ToolParameters{
					Type:       "object",
					Properties: map[string]types.ToolProperty{"path": {Type: "string", Description: "Path to echo"}},
					Required:   []string{"path"},
				},
			},
		}
		if !reflect.DeepEqual(byName[want.Function.Name], want) {
			t.Fatalf("tool = %+v, want %+v", byName[want.Function.Name], want)
		}
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"alpha__echo__path", true},
		{"alpha2__echo__path", true},
		{"alpha_echo__path", false},
		{"alph__echo__path", false},
		{"missing__echo", false},
		{"echo__path", false},
	} {
		if got := manager.IsMCPTool(tc.name); got != tc.want {
			t.Errorf("IsMCPTool(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	listed, err = manager.listToolsOnce(ctx, []string{"removed", "alpha"})
	if err != nil || len(listed) != 1 || listed[0].Function.Name != "alpha__echo__path" {
		t.Fatalf("list with removed session = %v, %v", listed, err)
	}
}

func TestCallToolForwardsArgumentsAndFlattensResult(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "test"}, nil)
	received := make(chan map[string]any, 1)
	server.AddTool(&sdk.Tool{Name: "nested__tool", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		received <- args
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: "done"},
			&sdk.ImageContent{MIMEType: "image/png", Data: []byte{1}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "test:///doc", Text: "doc"}},
		}}, nil
	})
	connectMemoryServer(t, ctx, manager, "server", server)
	args := map[string]any{"path": "/tmp/doc", "enabled": true, "count": float64(3), "nested": map[string]any{"key": "value"}}
	result, err := manager.CallTool(ctx, "server__nested__tool", args)
	if err != nil || result != "done[Image: image/png][Embedded Resource: test:///doc]" {
		t.Fatalf("CallTool = %q, %v", result, err)
	}
	select {
	case got := <-received:
		if !reflect.DeepEqual(got, args) {
			t.Fatalf("arguments = %v, want %v", got, args)
		}
	default:
		t.Fatal("handler did not receive arguments")
	}
}

func TestProtocolErrorsDoNotRetry(t *testing.T) {
	for _, method := range []string{"tools/list", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			ctx := managerTestContext(t)
			manager := managerForTest(t)
			server := echoServer()
			var calls atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, gotMethod string, req sdk.Request) (sdk.Result, error) {
					if gotMethod == method {
						calls.Add(1)
						return nil, errors.New("permission denied")
					}
					return next(ctx, gotMethod, req)
				}
			})
			connectMemoryServer(t, ctx, manager, "server", server)
			before := manager.Sessions["server"]
			var err error
			if method == "tools/list" {
				_, err = manager.ListTools(ctx)
				requireErrorContains(t, err, "failed to list tools for server server:")
			} else {
				_, err = manager.CallTool(ctx, "server__echo__path", nil)
				requireErrorContains(t, err, "MCP server server:")
			}
			requireErrorContains(t, err, "permission denied")
			if calls.Load() != 1 || manager.Sessions["server"] != before {
				t.Fatalf("non-EOF error was retried: calls=%d", calls.Load())
			}
		})
	}
}

func TestResourcesAndTemplates(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	resource := sdk.Resource{Name: "document", URI: "test:///doc", MIMEType: "text/plain", Description: "A document"}
	template := sdk.ResourceTemplate{Name: "dynamic", URITemplate: "test:///dynamic/{id}", MIMEType: "text/plain"}
	server := echoServer()
	server.AddResource(&resource, func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{
			{URI: req.Params.URI, Text: "first"},
			{URI: req.Params.URI, Text: "second"},
			{URI: req.Params.URI, MIMEType: "application/octet-stream", Blob: []byte{1, 2}},
			{URI: req.Params.URI, Text: "text wins", Blob: []byte{3}},
			{URI: req.Params.URI},
		}}, nil
	})
	server.AddResourceTemplate(&template, func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: req.Params.URI}}}, nil
	})
	// Inject null entries to exercise defensive conversion of SDK pointer slices.
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			switch method {
			case "resources/list":
				return &sdk.ListResourcesResult{Resources: []*sdk.Resource{nil, &resource}}, nil
			case "resources/templates/list":
				return &sdk.ListResourceTemplatesResult{ResourceTemplates: []*sdk.ResourceTemplate{nil, &template}}, nil
			default:
				return next(ctx, method, req)
			}
		}
	})
	connectMemoryServer(t, ctx, manager, "good", server)
	unsupported := echoServer()
	unsupported.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "resources/list" || method == "resources/templates/list" {
				return nil, errors.New("not supported")
			}
			return next(ctx, method, req)
		}
	})
	connectMemoryServer(t, ctx, manager, "unsupported", unsupported)
	resources, err := manager.ListResources(ctx)
	if err != nil || !reflect.DeepEqual(resources, map[string][]sdk.Resource{"good": {resource}}) {
		t.Fatalf("ListResources = %+v, %v", resources, err)
	}
	templates, err := manager.ListResourceTemplates(ctx)
	if err != nil || !reflect.DeepEqual(templates, map[string][]sdk.ResourceTemplate{"good": {template}}) {
		t.Fatalf("ListResourceTemplates = %+v, %v", templates, err)
	}
	result, err := manager.ReadResource(ctx, "good", resource.URI)
	if err != nil || result != "firstsecond[Binary data, MIME: application/octet-stream]text wins" {
		t.Fatalf("ReadResource = %q, %v", result, err)
	}
	result, err = manager.ReadResource(ctx, "good", "test:///dynamic/42")
	if err != nil || result != "test:///dynamic/42" {
		t.Fatalf("ReadResource template = %q, %v", result, err)
	}
	_, err = manager.ReadResource(ctx, "good", "test:///missing")
	if err == nil {
		t.Fatal("missing resource did not return an error")
	}
}

func TestInitializeHTTPTransportsAndShutdown(t *testing.T) {
	for _, transport := range []string{"sse", "streamableHttp"} {
		t.Run(transport, func(t *testing.T) {
			ctx := managerTestContext(t)
			server := echoServer()
			getServer := func(*http.Request) *sdk.Server { return server }
			var handler http.Handler
			if transport == "sse" {
				handler = sdk.NewSSEHandler(getServer, nil)
			} else {
				handler = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{JSONResponse: true})
			}
			var requests atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				if req.Header.Get("Authorization") != "Bearer test" || req.Header.Get("X-Mcp-Test") != "custom" {
					t.Errorf("missing custom headers: %v", req.Header)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				handler.ServeHTTP(w, req)
			}))
			t.Cleanup(httpServer.Close)
			manager := managerForTest(t)
			configs := []types.MCPServerConfig{
				{Name: "disabled", Transport: "invalid", Disabled: true},
				{Name: "http", Transport: transport, Endpoint: httpServer.URL, Headers: map[string]string{"Authorization": "Bearer test", "X-Mcp-Test": "custom"}},
			}
			if err := manager.Initialize(ctx, configs); err != nil {
				t.Fatal(err)
			}
			if !manager.HasSessions() || len(manager.Sessions) != 1 || manager.Clients["http"] == nil || !reflect.DeepEqual(manager.Configs, configs) {
				t.Fatal("initialized manager has incorrect state")
			}
			for session := range server.Sessions() {
				info := session.InitializeParams().ClientInfo
				if info.Name != "fuckyourcli" || info.Version != "test-version" {
					t.Fatalf("client info = %+v", info)
				}
			}
			listed, err := manager.ListTools(ctx)
			if err != nil || len(listed) != 1 || listed[0].Function.Name != "http__echo__path" {
				t.Fatalf("ListTools = %v, %v", listed, err)
			}
			result, err := manager.CallTool(ctx, "http__echo__path", map[string]any{"path": "http-path"})
			if err != nil || result != "http-path" {
				t.Fatalf("CallTool = %q, %v", result, err)
			}
			before := manager.Sessions["http"]
			manager.Shutdown(ctx)
			if manager.HasSessions() || manager.IsMCPTool("http__echo__path") || requests.Load() < 3 {
				t.Fatal("shutdown failed or HTTP transport was not used")
			}
			if err := before.Ping(ctx, &sdk.PingParams{}); err == nil {
				t.Fatal("shutdown did not close the session")
			}
			manager.Shutdown(ctx)
		})
	}
}

func stdioTestConfig(t *testing.T, transport string) types.MCPServerConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return types.MCPServerConfig{
		Name: "stdio", Transport: transport, Command: executable,
		Args: []string{"-test.run=^TestMCPStdioHelperProcess$", "--", "test-argument"},
		Env:  map[string]string{"FYC_MCP_TEST_HELPER": "1", "FYC_MCP_TEST_VALUE": "configured"},
	}
}

// Reuse the test executable so stdio tests need no shell, external server, or build step.
func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("FYC_MCP_TEST_HELPER") != "1" {
		return
	}
	server := echoServer()
	server.AddTool(&sdk.Tool{Name: "environment", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: os.Getenv("FYC_MCP_TEST_VALUE") + ":" + os.Getenv("FYC_MCP_TEST_INHERITED") + ":" + os.Args[len(os.Args)-1]}}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestInitializeStdioAndReconnectClosedSessions(t *testing.T) {
	t.Setenv("FYC_MCP_TEST_INHERITED", "inherited")
	for _, transport := range []string{"", "stdio"} {
		t.Run("transport_"+transport, func(t *testing.T) {
			ctx := managerTestContext(t)
			manager := managerForTest(t)
			cfg := stdioTestConfig(t, transport)
			if err := manager.Initialize(ctx, []types.MCPServerConfig{cfg}); err != nil {
				t.Fatal(err)
			}
			result, err := manager.CallTool(ctx, "stdio__environment", nil)
			if err != nil || result != "configured:inherited:test-argument" {
				t.Fatalf("stdio env/args = %q, %v", result, err)
			}
			for _, operation := range []string{"list", "call"} {
				before, beforeClient := manager.Sessions["stdio"], manager.Clients["stdio"]
				if err := before.Close(); err != nil {
					t.Fatal(err)
				}
				if operation == "list" {
					listed, err := manager.ListTools(ctx)
					if err != nil || len(listed) != 2 {
						t.Fatalf("ListTools after close = %v, %v", listed, err)
					}
				} else {
					result, err := manager.CallTool(ctx, "stdio__echo__path", map[string]any{"path": "reconnected"})
					if err != nil || result != "reconnected" {
						t.Fatalf("CallTool after close = %q, %v", result, err)
					}
				}
				if manager.Sessions["stdio"] == before || manager.Clients["stdio"] == beforeClient {
					t.Fatal("reconnect did not replace session/client")
				}
				result, err = manager.CallTool(ctx, "stdio__environment", nil)
				if err != nil || result != "configured:inherited:test-argument" {
					t.Fatalf("reconnect lost env/args = %q, %v", result, err)
				}
			}
		})
	}
}

func TestInitializeConnectionFailures(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	cfg := types.MCPServerConfig{Name: "missing", Command: t.TempDir() + "/nonexistent-server"}
	err := manager.Initialize(ctx, []types.MCPServerConfig{cfg})
	requireErrorContains(t, err, "failed to connect to MCP server missing:")
	if manager.HasSessions() || len(manager.Clients) != 0 {
		t.Fatal("failed connect registered a session/client")
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(httpServer.Close)
	for _, transport := range []string{"sse", "streamableHttp"} {
		cfg := types.MCPServerConfig{Name: "unauthorized", Transport: transport, Endpoint: httpServer.URL}
		err := manager.Initialize(ctx, []types.MCPServerConfig{cfg})
		requireErrorContains(t, err, "failed to connect to MCP server unauthorized:")
		if manager.HasSessions() {
			t.Fatal("failed HTTP connect registered a session")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	cfg = types.MCPServerConfig{Name: "canceled", Transport: "streamableHttp", Endpoint: httpServer.URL}
	err = manager.Initialize(canceled, []types.MCPServerConfig{cfg})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialization = %v", err)
	}
}

func TestReconnectErrors(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	err := manager.reconnect(ctx, "missing")
	requireErrorContains(t, err, "server missing not found in configs")
	manager.Configs = []types.MCPServerConfig{{Name: "broken", Transport: "invalid"}}
	err = manager.reconnect(ctx, "broken")
	requireErrorContains(t, err, `build transport: unknown transport "invalid"`)
	manager.Configs = []types.MCPServerConfig{{Name: "broken", Command: t.TempDir() + "/nonexistent-server"}}
	err = manager.reconnect(ctx, "broken")
	requireErrorContains(t, err, "connect:")
	if manager.HasSessions() {
		t.Fatal("failed reconnect registered a session")
	}
	connectMemoryServer(t, ctx, manager, "unconfigured", echoServer())
	if err := manager.Sessions["unconfigured"].Close(); err != nil {
		t.Fatal(err)
	}
	_, err = manager.CallTool(ctx, "unconfigured__echo__path", nil)
	requireErrorContains(t, err, "call failed after reconnect error:")
	requireErrorContains(t, err, "server unconfigured not found in configs")
	requireErrorContains(t, err, "original:")
	_, err = manager.ListTools(ctx)
	if err == nil || !isEOFError(err) {
		t.Fatalf("ListTools with failed reconnect = %v", err)
	}
}

func TestEOFRetryIsLimitedToOne(t *testing.T) {
	for _, method := range []string{"tools/list", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			ctx := managerTestContext(t)
			server := echoServer()
			var initializations, calls atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, gotMethod string, req sdk.Request) (sdk.Result, error) {
					if gotMethod == "initialize" {
						initializations.Add(1)
					}
					if gotMethod == method {
						calls.Add(1)
						return nil, errors.New("forced EOF")
					}
					return next(ctx, gotMethod, req)
				}
			})
			endpoint := serveHTTPServer(t, server, "streamableHttp")
			manager := managerForTest(t)
			if err := manager.Initialize(ctx, []types.MCPServerConfig{{Name: "server", Transport: "streamableHttp", Endpoint: endpoint}}); err != nil {
				t.Fatal(err)
			}
			var err error
			if method == "tools/list" {
				_, err = manager.ListTools(ctx)
			} else {
				_, err = manager.CallTool(ctx, "server__echo__path", nil)
			}
			requireErrorContains(t, err, "forced EOF")
			if calls.Load() != 2 || initializations.Load() != 2 {
				t.Fatalf("retry counts: calls=%d, initializations=%d; want 2 each", calls.Load(), initializations.Load())
			}
		})
	}
}

func TestConcurrentManagerOperations(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	connectMemoryServer(t, ctx, manager, "server", echoServer())
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Go(func() {
			for iteration := 0; iteration < 10; iteration++ {
				if !manager.HasSessions() || !manager.IsMCPTool("server__echo__path") {
					t.Error("active session/tool disappeared")
					return
				}
				listed, err := manager.ListTools(ctx)
				if err != nil || len(listed) != 1 {
					t.Errorf("concurrent ListTools = %v, %v", listed, err)
					return
				}
				result, err := manager.CallTool(ctx, "server__echo__path", map[string]any{"path": "concurrent"})
				if err != nil || result != "concurrent" {
					t.Errorf("concurrent CallTool = %q, %v", result, err)
					return
				}
			}
		})
	}
	wg.Wait()
	manager.Shutdown(ctx)
	if manager.HasSessions() {
		t.Fatal("shutdown after concurrent calls left a session")
	}
}
