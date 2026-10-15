package tools

import (
	"os"
	"os/exec"
	"strings"
)

// sandboxConfig holds all parameters needed to build a sandboxed command.
type sandboxConfig struct {
	RestrictedRoot string   // process cwd, always r+w
	AllowedRW      []string // read-write paths
	AllowedRO      []string // read-only paths (system dirs)
	DenyEnv        []string // env var names to strip from child process
	SecretEnvPairs []string // "KEY=VALUE" injected into child env (passed but redacted from output)
	CmdStr         string   // shell command to execute
}

// sandboxResult wraps cmd + cleanup (e.g., removing profile file on Darwin).
type sandboxResult struct {
	Cmd     *exec.Cmd
	Cleanup func()
	// spawn, when non-nil, starts the sandboxed command itself and returns the
	// running process, bypassing the default os/exec path in startSandboxed.
	// The Windows dedicated-user runner sets it: the
	// runner is launched with CreateProcessWithLogonW, which os/exec cannot
	// express.
	spawn func() (sandboxProcess, error)
}

func workspaceSensitivePaths(restrictedRoot string) []string {
	return []string{
		restrictedRoot + "/.git",
	}
}

// filteredEnv returns os.Environ() with keys in denyKeys removed. Names are
// compared through normalizeEnvKey, so the match is case-insensitive on Windows
// (where PATH == Path) and exact elsewhere.
func filteredEnv(denyKeys []string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		blocked := false
		for _, k := range denyKeys {
			if normalizeEnvKey(key) == normalizeEnvKey(k) {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, e)
		}
	}
	return out
}

// sensitivePaths returns paths that must be explicitly blocked.
func sensitivePaths(home string) []string {
	return []string{
		home + "/.ssh",
		home + "/.aws",
		home + "/.gnupg",
		home + "/.netrc",
		home + "/.git-credentials",
		home + "/.config/gcloud",
		home + "/.config/gh",
		home + "/.docker",
	}
}

// homeDir returns the user's home directory (empty when it cannot be
// determined). Platform-neutral: used by the sandbox diagnostics on every OS.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// ── sandbox diagnostics (/sandbox, `fyc sandbox`) ────
// The Windows implementations live in sandbox_acl_windows.go;
// sandbox_status_other.go covers the other platforms. These wrappers keep the
// callers (slash.go, main.go) free of build tags.

// SandboxInfo describes the sandbox currently in use.
type SandboxInfo struct {
	Platform string   // "windows (restricted-token)", "linux (bwrap)", …
	Enabled  bool     // is the sandbox active for `sh`?
	Network  bool     // is outbound network allowed inside the sandbox?
	Profile  string   // sandbox profile / container name
	Root     string   // workspace root the sandbox is anchored to
	SID      string   // SID the sandbox grants on RW paths (Windows)
	Note     string   // extra explanation (disabled sandbox, missing profile…)
	Err      string   // non-fatal problem (e.g. profile not created yet)
	Allow    []string // paths the sandbox may read and write
	AllowRO  []string // paths the sandbox may only read
	Deny     []string // paths deliberately unreachable from the sandbox
	ACL      []string // one human-readable line per path (ACE state)
}

// SandboxStatus reports the sandbox state for the given workspace root.
func SandboxStatus(root string) SandboxInfo { return sandboxStatus(root) }

// SandboxACLRevoke removes every ACE the sandbox planted on host paths
// (Windows); it is a no-op elsewhere. Returns the number of objects cleaned.
func SandboxACLRevoke(root string) (int, error) { return sandboxACLRevoke(root) }

// SandboxReset revokes the ACEs and clears any legacy AppContainer ACEs (Windows).
func SandboxReset(root string) (int, error) { return sandboxReset(root) }
