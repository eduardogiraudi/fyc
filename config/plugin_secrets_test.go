package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPluginCredentials(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	t.Setenv("FYC_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))
	t.Setenv("PLUGIN_TEST_TOKEN", "environment")
	t.Setenv("PLUGIN_TEST_MISSING", "")
	got := MissingPluginVariables([]string{"${PLUGIN_ROOT}", "${PLUGIN_DATA}", "${PLUGIN_TEST_TOKEN}", "${PLUGIN_TEST_MISSING}", "${PLUGIN_TEST_MISSING}", "${OPTIONAL:-default}"})
	if !reflect.DeepEqual(got, []string{"PLUGIN_TEST_MISSING"}) {
		t.Fatalf("missing variables: %v", got)
	}
	if _, err := ResolvePluginValue("Bearer ${PLUGIN_TEST_MISSING}"); err == nil {
		t.Fatal("missing token silently became empty")
	}
	if err := SavePluginSecret("PLUGIN_TEST_TOKEN", "stored"); err != nil {
		t.Fatal(err)
	}
	value, err := ResolvePluginValue("Bearer ${PLUGIN_TEST_TOKEN}")
	if err != nil || value != "Bearer stored" {
		t.Fatalf("auth precedence: %q %v", value, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("FYC_CONFIG_DIR"), "config.json")); !os.IsNotExist(err) {
		t.Fatal("plugin secrets wrote config.json")
	}
	if err := os.WriteFile(AuthFilePath(), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SavePluginSecret("NEW_TOKEN", "secret"); err == nil {
		t.Fatal("overwrote corrupt auth store")
	}
	data, err := os.ReadFile(AuthFilePath())
	if err != nil || string(data) != "corrupt" {
		t.Fatal("corrupt auth store was changed")
	}
}
