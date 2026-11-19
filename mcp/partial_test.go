package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fuckyourcli/types"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyOAuthStore struct{}

func (emptyOAuthStore) Load(string) (string, error) { return "", nil }
func (emptyOAuthStore) Save(string, string) error   { return nil }
func (emptyOAuthStore) Delete(string) error         { return nil }

func TestInitializeKeepsHealthyServersOnOAuthFailure(t *testing.T) {
	for _, transport := range []string{"sse", "streamableHttp"} {
		t.Run(transport, func(t *testing.T) {
			ctx := managerTestContext(t)
			server := echoServer()
			getServer := func(*http.Request) *sdk.Server { return server }
			var handler http.Handler = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{JSONResponse: true})
			if transport == "sse" {
				handler = sdk.NewSSEHandler(getServer, nil)
			}
			healthy := httptest.NewServer(handler)
			defer healthy.Close()
			unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer unauthorized.Close()
			manager := NewMCPManager("test", emptyOAuthStore{})
			defer manager.Shutdown(context.Background())
			err := manager.Initialize(ctx, []types.MCPServerConfig{
				{Name: "tuist--tuist", Transport: transport, Endpoint: unauthorized.URL},
				{Name: "healthy", Transport: transport, Endpoint: healthy.URL},
				{Name: "invalid", Transport: "invalid"},
			})
			if err == nil || !strings.Contains(err.Error(), "needs authentication") || !strings.Contains(err.Error(), "mcp login") {
				t.Fatalf("expected OAuth login hint: %v", err)
			}
			listed, err := manager.ListTools(ctx)
			if err != nil || len(listed) != 1 || listed[0].Function.Name != "healthy__echo__path" {
				t.Fatalf("healthy tools lost: %v, %v", listed, err)
			}
			if err := manager.Sessions["healthy"].Close(); err != nil {
				t.Fatal(err)
			}
			listed, err = manager.ListTools(ctx)
			if err != nil || len(listed) != 1 {
				t.Fatalf("OAuth-enabled reconnect: %v, %v", listed, err)
			}
		})
	}
}

func TestListToolsRetainsOtherServersOnFailure(t *testing.T) {
	ctx := managerTestContext(t)
	manager := managerForTest(t)
	broken := echoServer()
	broken.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/list" {
				return nil, errors.New("tool catalog unavailable")
			}
			return next(ctx, method, req)
		}
	})
	connectMemoryServer(t, ctx, manager, "broken", broken)
	connectMemoryServer(t, ctx, manager, "healthy", echoServer())
	listed, err := manager.ListTools(ctx)
	if err == nil || !strings.Contains(err.Error(), "tool catalog unavailable") || len(listed) != 1 || listed[0].Function.Name != "healthy__echo__path" {
		t.Fatalf("partial tool catalog: %v, %v", listed, err)
	}
}
