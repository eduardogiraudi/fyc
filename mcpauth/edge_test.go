package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fuckyourcli/types"
)

func TestSSELoginAndPreregisteredCallback(t *testing.T) {
	f := newFixture(t)
	f.cfg.Transport = "sse"
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	callbackPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	f.login(t, LoginOptions{ClientID: "test-client", CallbackPort: callbackPort})
	if f.registrations.Load() != 0 {
		t.Fatal("preregistered client triggered dynamic registration")
	}
	creds, err := load(f.cfg, f.store)
	if err != nil || creds.RedirectURL != "http://127.0.0.1:"+port+"/callback" {
		t.Fatalf("callback port was ignored: %v", err)
	}
}

func TestCancelledLoginRetainsRegistration(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Login(ctx, f.cfg, f.store, LoginOptions{OpenURL: func(string) error {
		cancel()
		return nil
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("login cancellation: %v", err)
	}
	creds, err := load(f.cfg, f.store)
	if err != nil || creds == nil || creds.ClientID == "" || creds.Token != nil {
		t.Fatalf("registration was lost or cancellation saved a token: %v", err)
	}
	f.login(t)
	if f.registrations.Load() != 1 {
		t.Fatal("cancelled login discarded the registered client")
	}
}

func TestLogoutDuringUnauthorizedRequest(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	client := HTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer access" {
			t.Error("stored bearer was not attached")
		}
		if err := f.store.Delete(Key(f.cfg)); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}, f.cfg, f.store)
	if _, err := client.Get(f.cfg.Endpoint); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("logout raced with 401: %v", err)
	}
}

func TestUntrustedDiscoveryDoesNotOpenBrowser(t *testing.T) {
	for _, mode := range []string{"resource", "issuer", "pkce", "token", "challenge", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("FYC_CONFIG_DIR", t.TempDir())
			root := ""
			var leaked atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/mcp":
					metadataURL := root + "/metadata"
					if mode == "challenge" {
						metadataURL = "http://169.254.169.254/credentials"
					}
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, metadataURL))
					w.WriteHeader(http.StatusUnauthorized)
				case "/metadata":
					if mode == "redirect" {
						http.Redirect(w, req, "/leak", http.StatusFound)
						return
					}
					resource := root + "/mcp"
					if mode == "resource" {
						resource = root + "/another-resource"
					}
					json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{root}})
				case "/.well-known/oauth-authorization-server":
					issuer, tokenURL := root, root+"/token"
					methods := []string{"S256"}
					if mode == "issuer" {
						issuer = root + "/another-issuer"
					}
					if mode == "pkce" {
						methods = []string{"plain"}
					}
					if mode == "token" {
						tokenURL = "https://10.0.0.1/token"
					}
					json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": root + "/authorize", "token_endpoint": tokenURL, "code_challenge_methods_supported": methods})
				case "/leak":
					leaked.Store(true)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			root = server.URL
			cfg := types.MCPServerConfig{Name: "unsafe", Transport: "streamableHttp", Endpoint: root + "/mcp"}
			opened := false
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := Login(ctx, cfg, &memoryStore{}, LoginOptions{ClientID: "client", OpenURL: func(string) error {
				opened = true
				return errors.New("unexpected browser")
			}})
			if err == nil || opened || leaked.Load() {
				t.Fatalf("unsafe OAuth discovery accepted: %v", err)
			}
		})
	}
}
