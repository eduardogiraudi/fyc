package mcpauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"fuckyourcli/types"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type LoginOptions struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	CallbackPort int
	// OpenURL presents the authorization URL to the user; it must not log tokens.
	OpenURL func(string) error
}

type authMetadata struct {
	oauthex.AuthServerMeta
	ResponseISS bool `json:"authorization_response_iss_parameter_supported"`
}

// Login is deliberately separate from transport requests: AG-UI and background
// startup must never open a browser or block on a terminal confirmation.
func Login(ctx context.Context, cfg types.MCPServerConfig, secrets Store, opts LoginOptions) (retErr error) {
	defer func() {
		if ctx.Err() != nil {
			retErr = ctx.Err()
		}
	}()
	if opts.CallbackPort < 0 || opts.CallbackPort > 65535 {
		return fmt.Errorf("invalid OAuth callback port")
	}
	if secrets == nil || opts.OpenURL == nil {
		return fmt.Errorf("OAuth login requires a credential store and browser callback")
	}
	if cfg.Disabled || (cfg.Transport != "sse" && cfg.Transport != "streamableHttp") {
		return fmt.Errorf("OAuth login requires an enabled HTTP MCP server")
	}
	client, err := discoveryClient(cfg.Endpoint)
	if err != nil {
		return err
	}
	resource, meta, scopes, err := discover(ctx, cfg, client)
	if err != nil {
		return err
	}
	authorizeEndpoint, _ := url.Parse(meta.AuthorizationEndpoint)
	if _, err := endpointIPs(ctx, authorizeEndpoint.Hostname(), cfg.Endpoint); err != nil {
		return err
	}
	if len(meta.CodeChallengeMethodsSupported) > 0 && !slices.Contains(meta.CodeChallengeMethodsSupported, "S256") {
		return fmt.Errorf("OAuth server does not support PKCE S256")
	}
	previous, err := load(cfg, secrets)
	if err != nil {
		return err
	}
	if previous != nil && previous.Issuer != meta.Issuer {
		previous = nil
	}
	redirect := "http://127.0.0.1:0/callback"
	if previous != nil {
		redirect = previous.RedirectURL
	}
	if opts.CallbackPort != 0 {
		redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", opts.CallbackPort)
		if previous != nil && previous.RedirectURL != redirect {
			previous = nil
		}
	}
	callback, err := url.Parse(redirect)
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/callback" || callback.Port() == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" {
		return fmt.Errorf("invalid OAuth callback configuration")
	}
	listener, err := net.Listen("tcp4", callback.Host)
	if err != nil {
		return fmt.Errorf("OAuth callback port is unavailable")
	}
	defer listener.Close()
	callback.Host = listener.Addr().String()
	creds := &Credentials{Resource: resource.Resource, Issuer: meta.Issuer, AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, RedirectURL: callback.String(), AuthMethod: "none"}
	if len(opts.Scopes) > 0 {
		scopes = slices.Clone(opts.Scopes)
	}
	if slices.Contains(meta.ScopesSupported, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(slices.Clone(scopes), "offline_access")
	}
	creds.Scopes = scopes
	if opts.ClientID != "" {
		creds.ClientID, creds.ClientSecret = opts.ClientID, opts.ClientSecret
		if opts.ClientSecret != "" {
			creds.AuthMethod = "client_secret_basic"
		}
	} else if previous != nil {
		creds.ClientID, creds.ClientSecret, creds.AuthMethod = previous.ClientID, previous.ClientSecret, previous.AuthMethod
	}
	if creds.ClientID == "" {
		if meta.RegistrationEndpoint == "" {
			return fmt.Errorf("OAuth server has no dynamic registration endpoint; use fuckyourcli mcp login %q --client-id ID", cfg.Name)
		}
		registered, err := oauthex.RegisterClient(ctx, meta.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{creds.RedirectURL}, TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, ClientName: "fuckyourcli",
			Scope: strings.Join(creds.Scopes, " "),
		}, client)
		if err != nil {
			return fmt.Errorf("OAuth dynamic client registration failed")
		}
		if registered.ClientID == "" || !slices.Contains(registered.RedirectURIs, creds.RedirectURL) {
			return fmt.Errorf("OAuth registration returned an invalid client or callback")
		}
		creds.ClientID, creds.ClientSecret = registered.ClientID, registered.ClientSecret
		if registered.TokenEndpointAuthMethod != "" {
			creds.AuthMethod = registered.TokenEndpointAuthMethod
		}
	}
	if creds.AuthMethod != "none" && creds.AuthMethod != "client_secret_basic" && creds.AuthMethod != "client_secret_post" {
		return fmt.Errorf("unsupported OAuth token authentication method")
	}
	if creds.AuthMethod != "none" && creds.ClientSecret == "" {
		return fmt.Errorf("OAuth client requires a client secret")
	}
	if previous != nil && previous.ClientID == creds.ClientID {
		creds.Token = previous.Token
	}
	// Retain the registered client on cancellation, without destroying an old token.
	if err := saveLocked(ctx, cfg, secrets, creds); err != nil {
		return err
	}
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	oauthConfig := &oauth2.Config{
		ClientID: creds.ClientID, ClientSecret: creds.ClientSecret, RedirectURL: creds.RedirectURL, Scopes: creds.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: creds.AuthURL, TokenURL: creds.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
	}
	if creds.AuthMethod == "client_secret_basic" {
		oauthConfig.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	authURL := oauthConfig.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", creds.Resource))
	code, err := waitForCode(ctx, listener, callback, state, meta.Issuer, meta.ResponseISS, authURL, opts.OpenURL)
	if err != nil {
		return err
	}
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, client)
	token, err := oauthConfig.Exchange(tokenCtx, code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", creds.Resource))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("OAuth authorization-code exchange failed")
	}
	if token.AccessToken == "" || !strings.EqualFold(token.Type(), "Bearer") || !token.Valid() {
		return fmt.Errorf("OAuth server returned an invalid bearer token")
	}
	creds.Token = token
	return saveLocked(ctx, cfg, secrets, creds)
}

func saveLocked(ctx context.Context, cfg types.MCPServerConfig, secrets Store, creds *Credentials) error {
	lock, err := credentialLock(ctx, cfg)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	if err := save(cfg, secrets, creds); err != nil {
		return fmt.Errorf("cannot save MCP OAuth credentials")
	}
	return nil
}

func discover(ctx context.Context, cfg types.MCPServerConfig, client *http.Client) (*oauthex.ProtectedResourceMetadata, *authMetadata, []string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fuckyourcli","version":"1"}}}`
	method := http.MethodPost
	var payload io.Reader = strings.NewReader(body)
	if cfg.Transport == "sse" {
		method, payload = http.MethodGet, nil
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.Endpoint, payload)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range cfg.Headers {
		if !strings.EqualFold(k, "Authorization") {
			req.Header.Set(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("MCP OAuth discovery request failed")
	}
	challenges, parseErr := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	resp.Body.Close()
	if parseErr != nil {
		return nil, nil, nil, fmt.Errorf("invalid MCP authentication challenge")
	}
	metadataURL := ""
	var scopes []string
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" {
			metadataURL = challenge.Params["resource_metadata"]
			scopes = strings.Fields(challenge.Params["scope"])
			break
		}
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	var candidates []string
	if metadataURL != "" {
		candidates = []string{metadataURL}
	} else {
		base := endpoint.Scheme + "://" + endpoint.Host
		candidates = []string{base + "/.well-known/oauth-protected-resource" + endpoint.EscapedPath(), base + "/.well-known/oauth-protected-resource"}
	}
	var resource *oauthex.ProtectedResourceMetadata
	for _, candidate := range candidates {
		if err := allowedURL(candidate, cfg.Endpoint); err != nil {
			return nil, nil, nil, err
		}
		resource, err = oauthex.GetProtectedResourceMetadata(ctx, candidate, cfg.Endpoint, client)
		if err == nil {
			break
		}
	}
	if resource == nil || err != nil || len(resource.AuthorizationServers) == 0 {
		return nil, nil, nil, fmt.Errorf("MCP server did not provide valid OAuth protected-resource metadata")
	}
	issuer := resource.AuthorizationServers[0]
	if err := allowedURL(issuer, cfg.Endpoint); err != nil {
		return nil, nil, nil, err
	}
	meta, err := discoverIssuer(ctx, issuer, cfg.Endpoint, client)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(scopes) == 0 {
		scopes = slices.Clone(resource.ScopesSupported)
	}
	return resource, meta, scopes, nil
}

func discoverIssuer(ctx context.Context, issuer, resource string, client *http.Client) (*authMetadata, error) {
	u, _ := url.Parse(issuer)
	base := u.Scheme + "://" + u.Host
	issuerPath := strings.TrimRight(u.EscapedPath(), "/")
	candidates := []string{base + "/.well-known/oauth-authorization-server" + issuerPath, base + "/.well-known/openid-configuration" + issuerPath, strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"}
	for _, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, "GET", candidate, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("OAuth authorization-server discovery failed")
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			continue
		}
		var meta authMetadata
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil || meta.Issuer != issuer {
			return nil, fmt.Errorf("invalid OAuth issuer metadata")
		}
		for _, endpoint := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint} {
			if err := allowedURL(endpoint, resource); err != nil {
				return nil, err
			}
		}
		if meta.RegistrationEndpoint != "" {
			if err := allowedURL(meta.RegistrationEndpoint, resource); err != nil {
				return nil, err
			}
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("OAuth authorization-server metadata is unavailable")
}

func waitForCode(ctx context.Context, listener net.Listener, callback *url.URL, state, issuer string, requireISS bool, authURL string, openURL func(string) error) (string, error) {
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if req.Method != "GET" || req.URL.Path != callback.Path || req.Host != callback.Host {
			http.Error(w, "Invalid callback", http.StatusBadRequest)
			return
		}
		query := req.URL.Query()
		if len(query["state"]) != 1 || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		var res result
		if (requireISS && query.Get("iss") == "") || (query.Get("iss") != "" && query.Get("iss") != issuer) || len(query["iss"]) > 1 {
			res.err = fmt.Errorf("OAuth callback issuer mismatch")
		} else if query.Get("error") != "" {
			res.err = fmt.Errorf("OAuth authorization was denied or cancelled")
		} else if len(query["code"]) != 1 || query.Get("code") == "" {
			res.err = fmt.Errorf("OAuth callback did not contain an authorization code")
		} else {
			res.code = query.Get("code")
		}
		once.Do(func() { results <- res })
		if res.err != nil {
			http.Error(w, "Authentication failed; return to fuckyourcli.", http.StatusBadRequest)
		} else {
			fmt.Fprintln(w, "Authentication complete. Return to fuckyourcli.")
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 32 << 10}
	defer func() {
		// Let the browser receive its completion page before closing the callback.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Close()
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	if err := openURL(authURL); err != nil {
		return "", err
	}
	select {
	case res := <-results:
		return res.code, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return "", fmt.Errorf("OAuth callback server closed")
		}
		return "", fmt.Errorf("OAuth callback server failed")
	}
}
