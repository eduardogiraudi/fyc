package config

import (
	"errors"
	"fmt"
	"fuckyourcli/providers"
	"fuckyourcli/ui"
	"sync"
	"time"
)

// errAuthCanceled is returned when the user dismisses a Codex authentication
// prompt (q / Ctrl+C / Esc): the flow aborts instead of falling through into a
// fresh OAuth round.
var errAuthCanceled = errors.New("authentication cancelled")

// ensureCodexAuth ensures that config contains a valid access token for Codex.
// It first tries to reuse/renew the existing token; if it fails,
// it performs the full OAuth flow.
func ensureCodexAuth() error {
	reuse, forceReauth, canceled := tryReuseCodexSession()
	if canceled {
		return errAuthCanceled
	}
	if reuse {
		return nil
	}
	if !forceReauth {
		refreshed, err := tryRefreshCodexToken()
		if refreshed {
			return err // nil if ok, error if refresh failed but user wants to retry
		}
		if errors.Is(err, errAuthCanceled) {
			return errAuthCanceled
		}
	}
	return doFullCodexAuth()
}

// tryReuseCodexSession checks if an unexpired token exists and asks
// the user if they want to reuse it. Returns (true, _, false) when the token is
// reused, (false, true, false) when the user explicitly asked for a full
// re-authentication, and (_, _, true) when the prompt was dismissed
// (q / Ctrl+C / Esc).
func tryReuseCodexSession() (reuse bool, forceReauth bool, canceled bool) {
	if config.CodexAccessToken == "" || config.CodexTokenExpiresAt <= time.Now().UnixMilli() {
		return false, false, false
	}
	choice := ui.SelectOption(
		"Codex is already authenticated. Reuse existing session?",
		[]string{"Yes, reuse", "No, re-authenticate"},
	)
	if choice < 0 {
		return false, false, true
	}
	if choice != 0 {
		fmt.Println(ui.InfoStyle.Render("Starting fresh authentication as requested..."))
		return false, true, false
	}
	// Best-effort preemptive refresh: if it fails, we still use the existing token.
	tryRefreshCodexToken()
	return true, false, false
}

// tryRefreshCodexToken attempts to renew the token using the refresh_token.
// Returns (true, nil) if successful, (false, nil) if there is no refresh token,
// (false, err) if the refresh failed.
func tryRefreshCodexToken() (ok bool, err error) {
	if config.CodexRefreshToken == "" {
		return false, nil
	}

	// Ask only if the token has expired AND there was a valid access token previously.
	// If there is no access token, attempt the refresh silently (original behavior).
	if config.CodexAccessToken != "" && config.CodexTokenExpiresAt <= time.Now().UnixMilli() {
		choice := ui.SelectOption(
			"Token expired. What would you like to do?",
			[]string{"Try refresh", "Full re-authentication"},
		)
		if choice < 0 {
			return false, errAuthCanceled
		}
		if choice != 0 {
			return false, nil
		}
	}

	fmt.Println(ui.InfoStyle.Render("Attempting token refresh..."))
	codexRefreshMu.Lock()
	_, err = codexRefreshLocked()
	codexRefreshMu.Unlock()
	if err != nil {
		fmt.Println(ui.InfoStyle.Render("Token refresh failed: " + err.Error()))
		return false, err
	}

	fmt.Println(ui.OkStyle.Render("Token refreshed successfully!"))
	return true, nil
}

// doFullCodexAuth performs the full OAuth flow.
func doFullCodexAuth() error {
	fmt.Println(ui.TitleStyle.Render("OpenAI Codex Authentication"))
	fmt.Println(ui.InfoStyle.Render("You need to authenticate with your OpenAI account."))

	creds, err := providers.CodexAuthenticate()
	if err != nil {
		return err
	}

	applyCodexCredentials(creds)
	Save()
	return nil
}

// codexRefreshSkew is how long before the recorded expiry a Codex token is
// treated as stale, so the refresh happens slightly ahead of the request that
// would need it.
const codexRefreshSkew = 60 * time.Second

// codexRefreshMu serializes Codex token refreshes. Refresh tokens are
// single-use: two concurrent refreshes (the main agent plus parallel
// sub-agents) would invalidate each other and lose the session.
var codexRefreshMu sync.Mutex

// applyCodexCredentials stores freshly minted credentials in the in-memory
// config. A response that omits refresh_token or expires_in must NOT wipe the
// stored values: the previous ones are kept so the session stays renewable.
func applyCodexCredentials(creds *providers.CodexCredentials) {
	if creds == nil {
		return
	}
	if creds.AccessToken != "" {
		config.CodexAccessToken = creds.AccessToken
	}
	if creds.RefreshToken != "" {
		config.CodexRefreshToken = creds.RefreshToken
	}
	if creds.ExpiresAt > 0 {
		config.CodexTokenExpiresAt = creds.ExpiresAt
	}
}

// codexTokenStale reports whether the recorded expiry is close enough that a
// refresh should be attempted. An unknown expiry (0) is never considered
// stale: the provider's reactive 401 path is the safety net for those.
func codexTokenStale() bool {
	if config.CodexTokenExpiresAt <= 0 {
		return false
	}
	return config.CodexTokenExpiresAt <= time.Now().Add(codexRefreshSkew).UnixMilli()
}

// codexRefreshLocked performs the actual refresh and persists it. Callers must
// hold codexRefreshMu.
func codexRefreshLocked() (string, error) {
	if config.CodexRefreshToken == "" {
		return "", fmt.Errorf("no Codex refresh token available")
	}
	creds, err := providers.CodexRefreshToken(config.CodexRefreshToken)
	if err != nil {
		return "", err
	}
	if creds.AccessToken == "" {
		return "", fmt.Errorf("Codex token refresh returned an empty access token")
	}
	applyCodexCredentials(creds)
	Save()
	return config.CodexAccessToken, nil
}

// CodexFreshAccessToken returns the Codex access token a request should use,
// proactively refreshing and persisting it when it is expired or within the
// skew window. It never prompts and is safe for concurrent use.
//
// It fails only when no token is configured at all. When a refresh is attempted
// but fails, the existing (possibly stale) token is returned with a nil error:
// the provider's reactive 401 path then surfaces the real problem, instead of
// the session dying on a clock guess that may be wrong.
func CodexFreshAccessToken() (string, error) {
	codexRefreshMu.Lock()
	defer codexRefreshMu.Unlock()

	if config.CodexAccessToken == "" {
		return "", fmt.Errorf("Codex not authenticated")
	}
	if !codexTokenStale() || config.CodexRefreshToken == "" {
		return config.CodexAccessToken, nil
	}
	if token, err := codexRefreshLocked(); err == nil {
		return token, nil
	}
	return config.CodexAccessToken, nil
}

// CodexForceRefresh refreshes the Codex token regardless of the recorded
// expiry and persists the new credentials. It is used after a 401, when the
// server has already rejected the current token. Errors are returned so the
// caller can report them alongside the original failure.
func CodexForceRefresh() (string, error) {
	codexRefreshMu.Lock()
	defer codexRefreshMu.Unlock()
	return codexRefreshLocked()
}
