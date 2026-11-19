// Package mcpauth provides explicit MCP OAuth login and non-interactive renewal.
package mcpauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/types"

	"golang.org/x/oauth2"
)

// Store keeps opaque OAuth records in the application's secret store.
type Store interface {
	Load(string) (string, error)
	Save(string, string) error
	Delete(string) error
}

var ErrLoginRequired = errors.New("MCP OAuth login required")

type LoginRequiredError struct{ Server string }

func (e *LoginRequiredError) Error() string {
	return fmt.Sprintf("MCP server %q needs authentication; run: fuckyourcli mcp login %q", e.Server, e.Server)
}
func (e *LoginRequiredError) Unwrap() error { return ErrLoginRequired }

type Credentials struct {
	Resource     string        `json:"resource"`
	Issuer       string        `json:"issuer"`
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret,omitempty"`
	AuthMethod   string        `json:"auth_method"`
	RedirectURL  string        `json:"redirect_url"`
	AuthURL      string        `json:"auth_url"`
	TokenURL     string        `json:"token_url"`
	Scopes       []string      `json:"scopes,omitempty"`
	Token        *oauth2.Token `json:"token,omitempty"`
}

func Key(cfg types.MCPServerConfig) string {
	sum := sha256.Sum256([]byte(cfg.Name + "\x00" + cfg.Endpoint))
	return "FYC_MCP_OAUTH_" + hex.EncodeToString(sum[:])
}

func load(cfg types.MCPServerConfig, secrets Store) (*Credentials, error) {
	if secrets == nil {
		return nil, nil
	}
	value, err := secrets.Load(Key(cfg))
	if err != nil {
		return nil, fmt.Errorf("cannot read MCP OAuth credentials")
	}
	if value == "" {
		return nil, nil
	}
	var creds Credentials
	if err := json.Unmarshal([]byte(value), &creds); err != nil || creds.Resource != cfg.Endpoint {
		return nil, fmt.Errorf("invalid MCP OAuth credential record")
	}
	return &creds, nil
}

func save(cfg types.MCPServerConfig, secrets Store, creds *Credentials) error {
	b, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return secrets.Save(Key(cfg), string(b))
}

func credentialLock(ctx context.Context, cfg types.MCPServerConfig) (*store.FileLock, error) {
	path := filepath.Join(store.ConfigDir(), "mcp-oauth", Key(cfg)+".lock")
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := store.TryLock(path)
		if !errors.Is(err, store.ErrLocked) {
			return lock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

func Status(cfg types.MCPServerConfig, secrets Store) (string, error) {
	creds, err := load(cfg, secrets)
	if err != nil {
		return "unreadable credentials", err
	}
	if creds == nil || creds.Token == nil || creds.Token.AccessToken == "" {
		return "not authenticated", nil
	}
	if creds.Token.Valid() {
		return "authenticated", nil
	}
	if creds.Token.RefreshToken != "" {
		return "expired (renewable)", nil
	}
	return "expired (login required)", nil
}

func Logout(cfg types.MCPServerConfig, secrets Store) error {
	lock, err := credentialLock(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return secrets.Delete(Key(cfg))
}

// HTTPClient never prompts. An authentication failure points at the login command.
func HTTPClient(base *http.Client, cfg types.MCPServerConfig, secrets Store) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	explicit := false
	for name := range cfg.Headers {
		if strings.EqualFold(name, "Authorization") {
			explicit = true
		}
	}
	client.Transport = &oauthTransport{cfg: cfg, secrets: secrets, next: next, explicit: explicit}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || len(via) >= 5 || !sameOrigin(req.URL, via[0].URL) {
			return fmt.Errorf("MCP redirect to another origin is refused")
		}
		return nil
	}
	return &client
}

type oauthTransport struct {
	cfg      types.MCPServerConfig
	secrets  Store
	next     http.RoundTripper
	explicit bool
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	handedOff := false
	defer func() {
		if !handedOff && req.Body != nil {
			req.Body.Close()
		}
	}()
	endpoint, err := url.Parse(t.cfg.Endpoint)
	if err != nil || !sameOrigin(endpoint, req.URL) {
		return nil, fmt.Errorf("MCP credentials cannot be sent to another origin")
	}
	if t.explicit {
		handedOff = true
		return t.next.RoundTrip(req)
	}
	token, err := tokenForRequest(req.Context(), t.cfg, t.secrets, "")
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	if token != nil {
		token.SetAuthHeader(clone)
	}
	handedOff = true
	resp, err := t.next.RoundTrip(clone)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	if token == nil || (req.Body != nil && req.GetBody == nil) {
		resp.Body.Close()
		return nil, &LoginRequiredError{t.cfg.Name}
	}
	resp.Body.Close()
	fresh, err := tokenForRequest(req.Context(), t.cfg, t.secrets, token.AccessToken)
	if err != nil {
		return nil, err
	}
	clone = req.Clone(req.Context())
	if req.GetBody != nil {
		clone.Body, err = req.GetBody()
		if err != nil {
			return nil, err
		}
	}
	fresh.SetAuthHeader(clone)
	resp, err = t.next.RoundTrip(clone)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, &LoginRequiredError{t.cfg.Name}
	}
	return resp, err
}

func tokenForRequest(ctx context.Context, cfg types.MCPServerConfig, secrets Store, rejected string) (*oauth2.Token, error) {
	creds, err := load(cfg, secrets)
	if err != nil {
		return nil, err
	}
	if creds == nil || creds.Token == nil {
		if rejected != "" {
			return nil, &LoginRequiredError{cfg.Name}
		}
		return nil, nil
	}
	if tokenUsable(creds.Token, rejected) {
		return creds.Token, nil
	}
	lock, err := credentialLock(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	// Re-read after the inter-process lock: refresh tokens may rotate once.
	creds, err = load(cfg, secrets)
	if err != nil {
		return nil, err
	}
	if creds == nil || creds.Token == nil {
		return nil, &LoginRequiredError{cfg.Name}
	}
	if tokenUsable(creds.Token, rejected) {
		return creds.Token, nil
	}
	if creds.Token.RefreshToken == "" {
		return nil, &LoginRequiredError{cfg.Name}
	}
	token, err := refresh(ctx, cfg, creds)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("MCP OAuth renewal failed: %w", err)
	}
	creds.Token = token
	if err := save(cfg, secrets, creds); err != nil {
		return nil, fmt.Errorf("cannot save renewed MCP OAuth credentials")
	}
	return token, nil
}

func tokenUsable(token *oauth2.Token, rejected string) bool {
	if !token.Valid() || (rejected != "" && token.AccessToken == rejected) {
		return false
	}
	return token.Expiry.IsZero() || token.Expiry.After(time.Now().Add(30*time.Second))
}

func refresh(ctx context.Context, cfg types.MCPServerConfig, creds *Credentials) (*oauth2.Token, error) {
	client, err := discoveryClient(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if err := allowedURL(creds.TokenURL, cfg.Endpoint); err != nil {
		return nil, err
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {creds.Token.RefreshToken}, "resource": {creds.Resource}}
	if creds.AuthMethod != "client_secret_basic" {
		values.Set("client_id", creds.ClientID)
	}
	if creds.AuthMethod == "client_secret_post" {
		values.Set("client_secret", creds.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", creds.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if creds.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(creds.ClientID), url.QueryEscape(creds.ClientSecret))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token endpoint is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned HTTP %d: %w", resp.StatusCode, &LoginRequiredError{cfg.Name})
	}
	var doc struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		TokenType    string      `json:"token_type"`
		ExpiresIn    json.Number `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil || doc.AccessToken == "" || (doc.TokenType != "" && !strings.EqualFold(doc.TokenType, "Bearer")) {
		return nil, fmt.Errorf("invalid OAuth token response")
	}
	if doc.RefreshToken == "" {
		doc.RefreshToken = creds.Token.RefreshToken
	}
	token := &oauth2.Token{AccessToken: doc.AccessToken, RefreshToken: doc.RefreshToken, TokenType: "Bearer"}
	if doc.ExpiresIn != "" {
		seconds, err := doc.ExpiresIn.Int64()
		if err != nil || seconds <= 0 || seconds > 10*365*24*3600 {
			return nil, fmt.Errorf("invalid OAuth token expiry")
		}
		token.Expiry = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return token, nil
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
