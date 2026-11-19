package mcpauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fuckyourcli/types"

	"golang.org/x/oauth2"
)

type memoryStore struct {
	mu     sync.Mutex
	values map[string]string
	fail   bool
}

func (s *memoryStore) Load(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("secret detail")
	}
	return s.values[key], nil
}
func (s *memoryStore) Save(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("secret detail")
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}
func (s *memoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

type oauthFixture struct {
	cfg           types.MCPServerConfig
	store         *memoryStore
	challenge     string
	mu            sync.Mutex
	refreshes     atomic.Int32
	registrations atomic.Int32
	failRefresh   atomic.Bool
}

func newFixture(t *testing.T) *oauthFixture {
	t.Helper()
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	f := &oauthFixture{store: &memoryStore{}}
	root := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/mcp":
			if req.Header.Get("Authorization") != "Bearer access" && req.Header.Get("Authorization") != "Bearer renewed" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="mcp:read"`, root))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		case "/.well-known/oauth-protected-resource":
			json.NewEncoder(w).Encode(map[string]any{"resource": root + "/mcp", "authorization_servers": []string{root}, "scopes_supported": []string{"mcp:read"}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": root, "authorization_endpoint": root + "/authorize", "token_endpoint": root + "/token", "registration_endpoint": root + "/register", "scopes_supported": []string{"mcp:read", "offline_access"}, "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
		case "/register":
			f.registrations.Add(1)
			var doc map[string]any
			if err := json.NewDecoder(req.Body).Decode(&doc); err != nil {
				t.Error(err)
				return
			}
			doc["client_id"] = "test-client"
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(doc)
		case "/token":
			if err := req.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			if req.Form.Get("resource") != root+"/mcp" || req.Form.Get("client_id") != "test-client" {
				t.Error("missing token resource or client ID")
			}
			if req.Form.Get("grant_type") == "refresh_token" {
				f.refreshes.Add(1)
				if f.failRefresh.Load() {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":"invalid_grant","error_description":"VERY_SECRET"}`)
					return
				}
				if req.Form.Get("refresh_token") != "refresh" {
					t.Error("unexpected refresh token")
				}
				fmt.Fprint(w, `{"access_token":"renewed","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`)
			} else {
				f.mu.Lock()
				challenge := f.challenge
				f.mu.Unlock()
				sum := sha256.Sum256([]byte(req.Form.Get("code_verifier")))
				if req.Form.Get("code") != "authorization-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
					t.Error("invalid code or PKCE verifier")
				}
				fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	root = server.URL
	f.cfg = types.MCPServerConfig{Name: "tuist--tuist", Transport: "streamableHttp", Endpoint: root + "/mcp"}
	t.Cleanup(server.Close)
	return f
}
func (f *oauthFixture) login(t *testing.T, options ...LoginOptions) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := LoginOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	opts.OpenURL = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.cfg.Endpoint || q.Get("scope") != "mcp:read offline_access" {
			return fmt.Errorf("invalid authorization URL")
		}
		f.mu.Lock()
		f.challenge = q.Get("code_challenge")
		f.mu.Unlock()
		callback, _ := url.Parse(q.Get("redirect_uri"))
		params := url.Values{"state": {"wrong-state"}, "code": {"authorization-code"}, "iss": {u.Scheme + "://" + u.Host}}
		callback.RawQuery = params.Encode()
		resp, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			return fmt.Errorf("accepted wrong state")
		}
		params.Set("state", q.Get("state"))
		callback.RawQuery = params.Encode()
		resp, err = http.Get(callback.String())
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("callback failed")
		}
		return nil
	}
	err := Login(ctx, f.cfg, f.store, opts)
	if err != nil {
		t.Fatal(err)
	}
}
func TestLoginRenewalRotationAndLogout(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	status, err := Status(f.cfg, f.store)
	if err != nil || status != "authenticated" {
		t.Fatalf("status %s: %v", status, err)
	}
	creds, err := load(f.cfg, f.store)
	if err != nil {
		t.Fatal(err)
	}
	creds.Token.Expiry = time.Now().Add(-time.Hour)
	if err := save(f.cfg, f.store, creds); err != nil {
		t.Fatal(err)
	}
	client := HTTPClient(nil, f.cfg, f.store)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(f.cfg.Endpoint)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Error(resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	creds, _ = load(f.cfg, f.store)
	if f.refreshes.Load() != 1 || creds.Token.RefreshToken != "rotated" {
		t.Fatal("refresh was duplicated or rotation was not stored")
	}
	// A new login reuses the persisted dynamic client and callback port.
	f.login(t)
	if f.registrations.Load() != 1 {
		t.Fatal("registered a new client on repeated login")
	}
	if err := Logout(f.cfg, f.store); err != nil {
		t.Fatal(err)
	}
	status, _ = Status(f.cfg, f.store)
	if status != "not authenticated" {
		t.Fatal(status)
	}
	_, err = client.Get(f.cfg.Endpoint)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "fuckyourcli mcp login") {
		t.Fatalf("missing login hint: %v", err)
	}
}
func TestUnauthorizedRefreshAndSecretSafeErrors(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	creds, _ := load(f.cfg, f.store)
	creds.Token.AccessToken = "rejected"
	if err := save(f.cfg, f.store, creds); err != nil {
		t.Fatal(err)
	}
	client := HTTPClient(nil, f.cfg, f.store)
	resp, err := client.Get(f.cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if f.refreshes.Load() != 1 {
		t.Fatal("401 did not trigger renewal")
	}
	creds, _ = load(f.cfg, f.store)
	creds.Token.Expiry = time.Now().Add(-time.Hour)
	if err := save(f.cfg, f.store, creds); err != nil {
		t.Fatal(err)
	}
	f.failRefresh.Store(true)
	_, err = client.Get(f.cfg.Endpoint)
	if !errors.Is(err, ErrLoginRequired) || strings.Contains(err.Error(), "VERY_SECRET") {
		t.Fatalf("unsafe token failure: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func TestExplicitAuthorizationAndOriginIsolation(t *testing.T) {
	cfg := types.MCPServerConfig{Name: "manual", Endpoint: "https://example.com/mcp", Headers: map[string]string{"aUthoRization": "Bearer manual"}}
	calls := 0
	client := HTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: req}, nil
	})}, cfg, &memoryStore{fail: true})
	resp, err := client.Get(cfg.Endpoint)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("manual authorization should bypass OAuth: %v", err)
	}
	resp.Body.Close()
	_, err = client.Get("https://evil.example/mcp")
	if err == nil || calls != 1 {
		t.Fatal("credentials leaked to another origin")
	}
	// Redirects cannot move credentials, even when they return to the MCP origin later.
	cfg.Headers = nil
	client = HTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/mcp"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}, cfg, &memoryStore{})
	if _, err := client.Get(cfg.Endpoint); err == nil {
		t.Fatal("accepted cross-origin redirect")
	}
}
func TestCallbackIssuerAndCancellation(t *testing.T) {
	for _, mode := range []string{"issuer", "cancel", "denied"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			callback, _ := url.Parse("http://" + listener.Addr().String() + "/callback")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = waitForCode(ctx, listener, callback, "state", "https://issuer.example", true, "https://issuer.example/authorize", func(string) error {
				if mode == "cancel" {
					cancel()
					return nil
				}
				q := url.Values{"state": {"state"}, "code": {"code"}, "iss": {"https://evil.example"}}
				if mode == "denied" {
					q.Set("iss", "https://issuer.example")
					q.Set("error", "access_denied")
				}
				callback.RawQuery = q.Encode()
				resp, err := http.Get(callback.String())
				if err != nil {
					return err
				}
				resp.Body.Close()
				return nil
			})
			if err == nil {
				t.Fatal("accepted invalid callback")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
func TestDiscoveryURLSecurity(t *testing.T) {
	for _, raw := range []string{"http://example.com/token", "https://127.0.0.1/token", "https://10.0.0.1/token", "https://100.100.100.200/token", "https://user:secret@example.com/token", "file:///token", "https://example.com/token#fragment"} {
		if err := allowedURL(raw, "https://example.com/mcp"); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := allowedURL("http://127.0.0.1:123/token", "http://127.0.0.1:456/mcp"); err == nil {
		t.Fatal("accepted another loopback origin")
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "::ffff:192.168.1.1", "64:ff9b::a00:1", "2002:0a00:0001::"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted private IP %s", raw)
		}
	}
	client, err := discoveryClient("http://127.0.0.1:123/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(nil, nil); err == nil {
		t.Fatal("discovery redirects were allowed")
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("rejected public IP")
	}
}
func TestCredentialBindingAndMissingRenewal(t *testing.T) {
	f := newFixture(t)
	creds := &Credentials{Resource: "https://wrong.example/mcp", Token: &oauth2.Token{AccessToken: "secret"}}
	if err := save(f.cfg, f.store, creds); err != nil {
		t.Fatal(err)
	}
	if _, err := load(f.cfg, f.store); err == nil {
		t.Fatal("accepted credentials for a different resource")
	}
	creds.Resource = f.cfg.Endpoint
	creds.Token.Expiry = time.Now().Add(-time.Hour)
	if err := save(f.cfg, f.store, creds); err != nil {
		t.Fatal(err)
	}
	_, err := HTTPClient(nil, f.cfg, f.store).Get(f.cfg.Endpoint)
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("missing renewal did not request login: %v", err)
	}
}
