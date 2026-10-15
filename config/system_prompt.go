package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fuckyourcli/store"
)

// DefaultSystemPrompt is the built-in base system prompt (the editable
// "persona" preamble). It reproduces the historical fuckyourcli preamble,
// but every dynamic value is expressed as an injectable ${VAR} placeholder so
// the user can freely reword the persona while keeping it dynamic.
//
// Only environment variables are substituted (no shell is ever invoked); the
// FYC_* names below are provided by the CLI itself, any other name falls back
// to the process environment, and an unknown variable resolves to "".
const DefaultSystemPrompt = `You are an agentic CLI assistant. You operate directly on the filesystem inside a restricted sandbox. You act, verify, and report — you do not explain unless asked.

Today: ${FYC_TODAY} | Working directory: ${FYC_CWD} (writes restricted here) | OS: ${FYC_OS} | ${FYC_SHELL}

User Preferences:
${FYC_PREFERENCES}`

// ShellNote returns the OS-specific sentence describing how the `sh` tool runs
// commands on this platform. It backs the ${FYC_SHELL} placeholder, so the base
// prompt stops claiming a Linux-only tmpfs sandbox when FYC_OS is windows
// (cmd.exe shell; PowerShell must be launched explicitly; the
// sandbox runs as a dedicated user; system TLS does not work).
func ShellNote(goos string) string {
	if goos == "windows" {
		return `sh runs through cmd.exe (no ;, printf, ls or uname — chain commands with &&); invoke PowerShell with powershell -NoProfile -Command "...". Commands run as a dedicated low-privilege user with access to the workspace and the toolchain cache, but not to your profile. System TLS (Schannel) is unavailable — use clients that bring their own TLS (Go, OpenSSL curl, Python).`
	}
	return "sh runs in a tmpfs sandbox — no system packages pre-installed."
}

// sysPromptVarRe matches ${VAR} placeholders. Variable names must start with a
// letter or underscore and may contain letters, digits and underscores.
var sysPromptVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// getSystemPromptPath returns the path of the user-editable system prompt file.
func getSystemPromptPath() string {
	dir := store.ConfigDir()
	if err := store.EnsureDir(dir, store.DirPerm); err != nil {
		store.Notice("config: cannot create %s: %v", dir, err)
	}
	return filepath.Join(dir, "SYSTEM_PROMPT.txt")
}

// SystemPromptTemplate returns the raw, unrendered system prompt template. When
// no user file exists (or it is empty/whitespace) the built-in default is
// returned, so behaviour is unchanged out of the box.
func SystemPromptTemplate() string {
	data, err := os.ReadFile(getSystemPromptPath())
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return DefaultSystemPrompt
	}
	return string(data)
}

// SystemPromptIsDefault reports whether the user has not customised the prompt.
func SystemPromptIsDefault() bool {
	data, err := os.ReadFile(getSystemPromptPath())
	return err != nil || strings.TrimSpace(string(data)) == ""
}

// SetSystemPrompt persists a custom system prompt template to disk.
func SetSystemPrompt(text string) error {
	if strings.TrimSpace(text) == "" {
		return ResetSystemPrompt()
	}
	return os.WriteFile(getSystemPromptPath(), []byte(text), 0600)
}

// ResetSystemPrompt removes the custom prompt, restoring the built-in default.
func ResetSystemPrompt() error {
	err := os.Remove(getSystemPromptPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SystemPromptPath exposes the template file location (used by the editor UI).
func SystemPromptPath() string {
	return getSystemPromptPath()
}

// RenderSystemPrompt substitutes every ${VAR} placeholder in tmpl. Values are
// looked up first in vars (the FYC_* built-ins) and then in the process
// environment. No shell is invoked; an unknown variable becomes an empty
// string. Substituted values are never rescanned.
func RenderSystemPrompt(tmpl string, vars map[string]string) string {
	return sysPromptVarRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := match[2 : len(match)-1]
		if v, ok := vars[name]; ok {
			return v
		}
		return os.Getenv(name)
	})
}

// SystemPromptVariableNames lists the built-in variables exposed to the
// template, in a stable order, for documentation purposes.
func SystemPromptVariableNames() []string {
	return []string{"FYC_TODAY", "FYC_CWD", "FYC_OS", "FYC_SHELL", "FYC_PREFERENCES"}
}
