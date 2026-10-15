package config

import (
	"testing"
	"time"

	"fuckyourcli/providers"
)

// saveConfig snapshots the package-level config so a test can mutate it
// freely and restore it afterwards, keeping test order irrelevant.
func saveConfig(t *testing.T) {
	t.Helper()
	prev := config
	t.Cleanup(func() { config = prev })
}

func TestApplyCodexCredentialsKeepsPreviousRefreshToken(t *testing.T) {
	saveConfig(t)

	config.CodexAccessToken = "old-access"
	config.CodexRefreshToken = "old-refresh"
	config.CodexTokenExpiresAt = 1000

	// A refresh response without refresh_token must NOT wipe the stored one,
	// otherwise the (single-use) chain is broken and the session cannot be
	// renewed any more.
	applyCodexCredentials(&providers.CodexCredentials{
		AccessToken: "new-access",
		ExpiresAt:   2000,
	})

	if config.CodexAccessToken != "new-access" {
		t.Fatalf("access token not updated: %q", config.CodexAccessToken)
	}
	if config.CodexRefreshToken != "old-refresh" {
		t.Fatalf("refresh token was wiped: %q", config.CodexRefreshToken)
	}
	if config.CodexTokenExpiresAt != 2000 {
		t.Fatalf("expiry not updated: %d", config.CodexTokenExpiresAt)
	}

	// A response carrying a new refresh token rotates it.
	applyCodexCredentials(&providers.CodexCredentials{
		AccessToken:  "newer-access",
		RefreshToken: "newer-refresh",
		ExpiresAt:    3000,
	})
	if config.CodexRefreshToken != "newer-refresh" {
		t.Fatalf("refresh token not rotated: %q", config.CodexRefreshToken)
	}

	// A nil payload is a no-op, never a panic.
	applyCodexCredentials(nil)
	if config.CodexAccessToken != "newer-access" {
		t.Fatalf("nil credentials mutated the config: %q", config.CodexAccessToken)
	}
}

func TestCodexTokenStale(t *testing.T) {
	saveConfig(t)

	// Unknown expiry is never stale: without a recorded deadline the token is
	// used as-is and the provider's reactive 401 path is the safety net.
	config.CodexTokenExpiresAt = 0
	if codexTokenStale() {
		t.Fatal("unknown expiry must not be considered stale")
	}

	config.CodexTokenExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	if codexTokenStale() {
		t.Fatal("a token valid for an hour must not be stale")
	}

	config.CodexTokenExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if !codexTokenStale() {
		t.Fatal("an expired token must be stale")
	}

	// Inside the skew window counts as stale, so the refresh happens ahead of
	// the request that would need it.
	config.CodexTokenExpiresAt = time.Now().Add(codexRefreshSkew / 2).UnixMilli()
	if !codexTokenStale() {
		t.Fatal("a token inside the skew window must be stale")
	}
}

func TestCodexFreshAccessToken(t *testing.T) {
	saveConfig(t)

	config.CodexAccessToken = ""
	if _, err := CodexFreshAccessToken(); err == nil {
		t.Fatal("expected an error when no token is configured")
	}

	// A token that is not stale is returned untouched, with no network call.
	config.CodexAccessToken = "keep-me"
	config.CodexTokenExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	token, err := CodexFreshAccessToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "keep-me" {
		t.Fatalf("expected the stored token, got %q", token)
	}
}
