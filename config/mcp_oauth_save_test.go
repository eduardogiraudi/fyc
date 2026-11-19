package config

import (
	"testing"
	"time"

	"fuckyourcli/mcpauth"
	"fuckyourcli/store"
	"fuckyourcli/types"
)

func TestConfigSaveWaitsForOAuthRotation(t *testing.T) {
	withConfigDir(t)
	config = defaults()
	config.GeminiAPIKey = "provider-secret"
	key := mcpauth.Key(types.MCPServerConfig{Name: "tuist--tuist", Endpoint: "https://example.com/mcp"})
	secrets := MCPOAuthStore{}
	if err := secrets.Save(key, "old-record"); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Lock(AuthFilePath() + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- SaveE()
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("config save bypassed the OAuth auth-store lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	auth, corrupt := loadAuthStore()
	if corrupt {
		t.Fatal("auth store is corrupt")
	}
	auth[key] = "rotated-record"
	if err := saveAuthStore(auth); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("config save did not resume after OAuth rotation")
	}
	value, err := secrets.Load(key)
	if err != nil || value != "rotated-record" {
		t.Fatalf("config save dropped OAuth rotation: %q, %v", value, err)
	}
}
