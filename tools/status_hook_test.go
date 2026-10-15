package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/config"
)

// withStatusHook resets the status-bar hook configuration after the test.
func withStatusHook(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		config.SetStatusBarHook(config.StatusBarHook{})
	})
}

func failingHook() config.StatusBarHook {
	return config.StatusBarHook{
		Enabled: true,
		Command: hookCmd("exit 1", "exit /b 1"),
	}
}

func TestStatusHookRunnerReturnsFirstLine(t *testing.T) {
	withStatusHook(t)
	config.SetStatusBarHook(config.StatusBarHook{
		Enabled: true,
		Command: hookCmd("printf 'credits: 42\\nsecond line\\n'", "echo credits: 42"),
	})

	line, show := (&StatusHookRunner{}).Refresh(t.TempDir())
	if !show || line != "credits: 42" {
		t.Fatalf("got line=%q show=%v", line, show)
	}
}

func TestStatusHookRunnerKeepsLastUsefulValueOnError(t *testing.T) {
	withStatusHook(t)
	config.SetStatusBarHook(config.StatusBarHook{
		Enabled: true,
		Command: hookCmd("echo credits: 42", "echo credits: 42"),
	})
	r := &StatusHookRunner{}
	if line, show := r.Refresh(t.TempDir()); !show || line != "credits: 42" {
		t.Fatalf("initial refresh: line=%q show=%v", line, show)
	}

	// A failing command must not overwrite the remembered value.
	config.SetStatusBarHook(failingHook())
	if line, show := r.Refresh(t.TempDir()); !show || line != "credits: 42" {
		t.Fatalf("after failure: line=%q show=%v (last value must be kept)", line, show)
	}
}

func TestStatusHookRunnerNoValueOnFirstFailure(t *testing.T) {
	withStatusHook(t)
	config.SetStatusBarHook(failingHook())

	if line, show := (&StatusHookRunner{}).Refresh(t.TempDir()); show || line != "" {
		t.Fatalf("first failure must render nothing, got line=%q show=%v", line, show)
	}
}

func TestStatusHookRunnerDisabledClearsValue(t *testing.T) {
	withStatusHook(t)
	config.SetStatusBarHook(config.StatusBarHook{
		Enabled: true,
		Command: hookCmd("echo credits: 42", "echo credits: 42"),
	})
	r := &StatusHookRunner{}
	if _, show := r.Refresh(t.TempDir()); !show {
		t.Fatalf("expected a value")
	}

	config.SetStatusBarHook(config.StatusBarHook{Enabled: false})
	if line, show := r.Refresh(t.TempDir()); show || line != "" {
		t.Fatalf("disabled hook must clear the line, got line=%q show=%v", line, show)
	}
}

func TestStatusHookRunnerResetsOnProviderChange(t *testing.T) {
	withStatusHook(t)
	t.Cleanup(func() {
		config.RemoveOpenAICompatibleProvider("ProviderA")
		config.RemoveOpenAICompatibleProvider("ProviderB")
	})

	config.SetStatusBarHook(config.StatusBarHook{Enabled: true, Command: "echo shared"})
	config.AddOpenAICompatibleProvider("ProviderA", "http://a", "ka", "model-a")
	config.AddOpenAICompatibleProvider("ProviderB", "http://b", "kb", "model-b")

	config.SwitchToOpenAIProvider("ProviderA", "model-a", false)
	r := &StatusHookRunner{}
	if line, _ := r.Refresh(t.TempDir()); line != "shared" {
		t.Fatalf("ProviderA: got %q", line)
	}

	// ProviderB has no working hook: the ProviderA value must not leak.
	config.SetStatusBarHookForProvider("ProviderB", failingHook())
	config.SwitchToOpenAIProvider("ProviderB", "model-b", false)
	if line, show := r.Refresh(t.TempDir()); show || line != "" {
		t.Fatalf("provider switch must drop the stale value, got line=%q show=%v", line, show)
	}
}

func TestStatusHookExportsSecretsOnlyWhenOptedIn(t *testing.T) {
	withStatusHook(t)
	t.Cleanup(func() { config.RemoveOpenAICompatibleProvider("SecretProvider") })

	config.AddOpenAICompatibleProvider("SecretProvider", "https://api.example", "sk-test-123", "model-s")
	config.SwitchToOpenAIProvider("SecretProvider", "model-s", false)

	writeKey := hookCmd(
		`printf '%s' "$FYC_API_KEY" > key.txt; printf '%s' "$FYC_PROVIDER_KEY" > pkey.txt; echo ok`,
		// cmd.exe leaves an undefined %VAR% literally, so an unset key would be
		// written verbatim and look like a leak; PowerShell expands $env: to an
		// empty string instead, matching the sh semantics below.
		psEncoded(`[IO.File]::WriteAllText('key.txt', "$env:FYC_API_KEY"); [IO.File]::WriteAllText('pkey.txt', "$env:FYC_PROVIDER_KEY"); Write-Output 'ok'`),
	)

	// Opt-out (default): the secret must not reach the command.
	dir := t.TempDir()
	config.SetStatusBarHook(config.StatusBarHook{Enabled: true, Command: writeKey})
	if _, show := (&StatusHookRunner{}).Refresh(dir); !show {
		t.Fatalf("hook produced no output")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "key.txt")); strings.TrimSpace(string(data)) != "" {
		t.Fatalf("API key leaked without opt-in: %q", data)
	}

	// Opt-in: the secret is exported through FYC_API_KEY.
	dir2 := t.TempDir()
	config.SetStatusBarHook(config.StatusBarHook{Enabled: true, Command: writeKey, IncludeSecrets: true})
	if _, ok := (&StatusHookRunner{}).Refresh(dir2); !ok {
		t.Fatalf("hook produced no output")
	}
	if data, _ := os.ReadFile(filepath.Join(dir2, "key.txt")); strings.TrimSpace(string(data)) != "sk-test-123" {
		t.Fatalf("expected FYC_API_KEY to carry the secret, got %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir2, "pkey.txt")); strings.TrimSpace(string(data)) != "SecretProvider" {
		t.Fatalf("expected FYC_PROVIDER_KEY=SecretProvider, got %q", data)
	}
}
