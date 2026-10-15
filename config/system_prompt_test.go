package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderSystemPromptBuiltins(t *testing.T) {
	vars := map[string]string{
		"FYC_TODAY":       "Wednesday, September 16, 2026",
		"FYC_CWD":         "/home/eduardo/fyc",
		"FYC_OS":          "linux",
		"FYC_PREFERENCES": "concise",
	}
	got := RenderSystemPrompt("Today: ${FYC_TODAY} | cwd: ${FYC_CWD} | os: ${FYC_OS} | pref: ${FYC_PREFERENCES}", vars)
	want := "Today: Wednesday, September 16, 2026 | cwd: /home/eduardo/fyc | os: linux | pref: concise"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderSystemPromptEnvFallback(t *testing.T) {
	t.Setenv("FYC_TEST_ENV_VAR", "from-env")
	got := RenderSystemPrompt("value=${FYC_TEST_ENV_VAR}", map[string]string{})
	if got != "value=from-env" {
		t.Fatalf("got %q want %q", got, "value=from-env")
	}
}

func TestRenderSystemPromptMissingIsEmpty(t *testing.T) {
	got := RenderSystemPrompt("a=${FYC_DEFINITELY_UNSET_VAR}b", map[string]string{"FYC_OTHER": "x"})
	if got != "a=b" {
		t.Fatalf("missing variable should resolve to empty string, got %q", got)
	}
}

func TestRenderSystemPromptNoRecursion(t *testing.T) {
	vars := map[string]string{"FYC_TODAY": "literal ${FYC_OS}", "FYC_OS": "linux"}
	got := RenderSystemPrompt("${FYC_TODAY}", vars)
	if got != "literal ${FYC_OS}" {
		t.Fatalf("substituted values must not be rescanned, got %q", got)
	}
}

func TestRenderSystemPromptLeavesLiteralDollar(t *testing.T) {
	got := RenderSystemPrompt("cost is $5 and $VAR stays", map[string]string{})
	if got != "cost is $5 and $VAR stays" {
		t.Fatalf("got %q", got)
	}
}

func TestSystemPromptTemplateDefaultsWhenNoFile(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	if got := SystemPromptTemplate(); got != DefaultSystemPrompt {
		t.Fatalf("expected default template, got %q", got)
	}
	if !SystemPromptIsDefault() {
		t.Fatal("expected SystemPromptIsDefault to be true")
	}
}

func TestSetAndResetSystemPrompt(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	custom := "You are a graphic designer. Date: ${FYC_TODAY}"
	if err := SetSystemPrompt(custom); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}
	if SystemPromptIsDefault() {
		t.Fatal("expected a custom prompt after SetSystemPrompt")
	}
	if got := SystemPromptTemplate(); got != custom {
		t.Fatalf("got %q want %q", got, custom)
	}
	if err := ResetSystemPrompt(); err != nil {
		t.Fatalf("ResetSystemPrompt: %v", err)
	}
	if got := SystemPromptTemplate(); !strings.Contains(got, "agentic CLI assistant") {
		t.Fatalf("expected the built-in default after reset, got %q", got)
	}
	if got := SystemPromptTemplate(); !strings.Contains(got, "agentic CLI assistant") {
		t.Fatalf("expected the built-in default after reset, got %q", got)
	}
}

func TestDefaultSystemPromptRendersLikeHistoricalPrompt(t *testing.T) {
	vars := map[string]string{
		"FYC_TODAY":       "Wednesday, September 16, 2026",
		"FYC_CWD":         "/work",
		"FYC_OS":          "linux",
		"FYC_SHELL":       "sh runs in a tmpfs sandbox — no system packages pre-installed.",
		"FYC_PREFERENCES": "pref-line",
	}
	got := RenderSystemPrompt(DefaultSystemPrompt, vars)
	want := "You are an agentic CLI assistant. You operate directly on the filesystem inside a restricted sandbox. You act, verify, and report — you do not explain unless asked.\n\n" +
		"Today: Wednesday, September 16, 2026 | Working directory: /work (writes restricted here) | OS: linux | sh runs in a tmpfs sandbox — no system packages pre-installed.\n\n" +
		"User Preferences:\npref-line"
	if got != want {
		t.Fatalf("default template drifted from the historical prompt:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestShellNoteIsOSSpecific locks the ${FYC_SHELL} value: Unix keeps the
// historical tmpfs sentence, Windows describes cmd.exe, PowerShell and Schannel.
func TestShellNoteIsOSSpecific(t *testing.T) {
	if note := ShellNote("linux"); !strings.Contains(note, "tmpfs sandbox") {
		t.Fatalf("non-Windows shell note should describe the tmpfs sandbox, got %q", note)
	}
	win := ShellNote("windows")
	for _, want := range []string{"cmd.exe", "powershell -NoProfile -Command", "Schannel"} {
		if !strings.Contains(win, want) {
			t.Fatalf("Windows shell note should mention %q, got %q", want, win)
		}
	}
}

func TestEditorDisplayNameUsesEnvCommand(t *testing.T) {
	t.Setenv("FYC_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "/usr/bin/my-editor --wait")
	if got := editorDisplayName(); got != "my-editor" {
		t.Fatalf("got %q want %q", got, "my-editor")
	}
}

func TestEditorDisplayNameFYCEDitorWins(t *testing.T) {
	t.Setenv("FYC_EDITOR", "fyc-edit")
	t.Setenv("VISUAL", "visual-edit")
	t.Setenv("EDITOR", "editor-edit")
	if got := editorDisplayName(); got != "fyc-edit" {
		t.Fatalf("got %q want %q", got, "fyc-edit")
	}
}

func TestEditorDisplayNameDetectsInstalledEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LookPath semantics differ on Windows")
	}
	t.Setenv("FYC_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nano"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := editorDisplayName(); got != "nano" {
		t.Fatalf("got %q want %q", got, "nano")
	}
}

func TestEditorDisplayNameFallsBackToDollarEditor(t *testing.T) {
	t.Setenv("FYC_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir())
	if got := editorDisplayName(); got != "$EDITOR" {
		t.Fatalf("got %q want %q", got, "$EDITOR")
	}
}

func TestSystemPromptAndPreferencesUseConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	t.Setenv("FYC_CONFIG_DIR", dir)
	want := filepath.Join(dir, "SYSTEM_PROMPT.txt")
	if got := SystemPromptPath(); got != want {
		t.Fatalf("SystemPromptPath() = %q, want %q", got, want)
	}
	pref := filepath.Join(filepath.Dir(want), "PREFERENCES.txt")
	if err := os.WriteFile(pref, []byte("concise\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadPreferences(); got != "concise" {
		t.Fatalf("loadPreferences() = %q, want concise", got)
	}
}
