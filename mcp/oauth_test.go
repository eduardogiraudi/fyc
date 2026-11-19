package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fuckyourcli/mcpauth"
	"fuckyourcli/types"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type testOAuthStore struct {
	mu    sync.Mutex
	value string
}

func (s *testOAuthStore) Load(string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, nil
}
func (s *testOAuthStore) Save(_ string, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}
func (s *testOAuthStore) Delete(string) error {
	return s.Save("", "")
}

func TestOAuthRenewalAndAuthenticatedReconnect(t *testing.T) {
	for _, transport := range []string{"sse", "streamableHttp"} {
		t.Run(transport, func(t *testing.T) {
			t.Setenv("FYC_CONFIG_DIR", t.TempDir())
			ctx := managerTestContext(t)
			server := echoServer()
			getServer := func(*http.Request) *sdk.Server { return server }
			var handler http.Handler = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{JSONResponse: true})
			if transport == "sse" {
				handler = sdk.NewSSEHandler(getServer, nil)
			}
			var refreshes atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/token" {
					if err := req.ParseForm(); err != nil {
						t.Error(err)
					}
					if req.Form.Get("grant_type") != "refresh_token" || req.Form.Get("refresh_token") != "refresh" {
						t.Error("invalid refresh grant")
					}
					refreshes.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"access_token":"renewed","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`)
					return
				}
				if req.Header.Get("Authorization") != "Bearer renewed" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				handler.ServeHTTP(w, req)
			}))
			defer httpServer.Close()
			cfg := types.MCPServerConfig{Name: "authenticated", Transport: transport, Endpoint: httpServer.URL + "/mcp"}
			data, err := json.Marshal(mcpauth.Credentials{
				Resource: cfg.Endpoint, Issuer: httpServer.URL, ClientID: "client", AuthMethod: "none",
				TokenURL: httpServer.URL + "/token",
				Token:    &oauth2.Token{AccessToken: "expired", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)},
			})
			if err != nil {
				t.Fatal(err)
			}
			secrets := &testOAuthStore{value: string(data)}
			manager := NewMCPManager("test", secrets)
			defer manager.Shutdown(context.Background())
			if err := manager.Initialize(ctx, []types.MCPServerConfig{cfg}); err != nil {
				t.Fatal(err)
			}
			if err := manager.Sessions[cfg.Name].Close(); err != nil {
				t.Fatal(err)
			}
			listed, err := manager.ListTools(ctx)
			if err != nil || len(listed) != 1 || refreshes.Load() != 1 {
				t.Fatalf("authenticated reconnect: tools=%d, refreshes=%d, err=%v", len(listed), refreshes.Load(), err)
			}
			result, err := manager.CallTool(ctx, "authenticated__echo__path", map[string]any{"path": "verified"})
			if err != nil || result != "verified" {
				t.Fatalf("authenticated tool: %s, %v", result, err)
			}
		})
	}
}
