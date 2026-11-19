package config

import (
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/mcpauth"
	"fuckyourcli/types"
)

func TestMCPOAuthStoreIsolationAndCorruption(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	t.Setenv("FYC_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))
	secrets := MCPOAuthStore{}
	key := mcpauth.Key(types.MCPServerConfig{Name: "tuist--tuist", Endpoint: "https://example.com/mcp"})
	if err := secrets.Save(key, `{"access_token":"secret"}`); err != nil {
		t.Fatal(err)
	}
	if err := SavePluginSecret("UNRELATED_SECRET", "preserved"); err != nil {
		t.Fatal(err)
	}
	value, err := secrets.Load(key)
	if err != nil || value != `{"access_token":"secret"}` {
		t.Fatalf("OAuth store: %q %v", value, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("FYC_CONFIG_DIR"), "config.json")); !os.IsNotExist(err) {
		t.Fatal("OAuth credentials wrote config.json")
	}
	if err := secrets.Delete(key); err != nil {
		t.Fatal(err)
	}
	value, err = secrets.Load(key)
	if err != nil || value != "" {
		t.Fatalf("logout: %q %v", value, err)
	}
	auth, corrupt := loadAuthStore()
	if corrupt || auth["UNRELATED_SECRET"] != "preserved" {
		t.Fatal("logout destroyed unrelated credentials")
	}
	if err := os.WriteFile(AuthFilePath(), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Load(key); err == nil {
		t.Fatal("loaded corrupt auth store")
	}
	if err := secrets.Save(key, "new"); err == nil {
		t.Fatal("overwrote corrupt auth store")
	}
	if err := secrets.Delete(key); err == nil {
		t.Fatal("deleted corrupt auth store")
	}
	data, err := os.ReadFile(AuthFilePath())
	if err != nil || string(data) != "corrupt" {
		t.Fatal("corrupt auth store changed")
	}
}
