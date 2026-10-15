//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// MaybeRunAsSandboxChild is a no-op on Darwin: we use external sandbox-exec,
// not the self-re-exec pattern.
func MaybeRunAsSandboxChild() {}

// MaybeRunAsSandboxRunner is a no-op on Darwin (Windows-only dedicated-user mode).
func MaybeRunAsSandboxRunner() {}

// buildSandboxCmd is the entry point for the Darwin platform.
func buildSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, error) {
	return buildDarwinSandboxCmd(ctx, cfg)
}

// buildDarwinSandboxCmd generates a TinyScheme profile (.sb) and runs
// sandbox-exec -f <profile> sh -c <cmd>.
func buildDarwinSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, error) {
	profile := buildSBProfile(cfg)

	// Write the profile to a temp file.
	f, err := os.CreateTemp("", "fyc-sandbox-*.sb")
	if err != nil {
		return sandboxResult{}, fmt.Errorf("sandbox: cannot create profile: %w", err)
	}
	profilePath := f.Name()
	if _, err := f.WriteString(profile); err != nil {
		f.Close()
		os.Remove(profilePath)
		return sandboxResult{}, fmt.Errorf("sandbox: cannot write profile: %w", err)
	}
	f.Close()

	cleanup := func() { os.Remove(profilePath) }

	args := []string{"-f", profilePath, "--", "sh", "-c", cfg.CmdStr}
	cmd := exec.CommandContext(ctx, "sandbox-exec", args...)
	cmd.Env = append(filteredEnv(cfg.DenyEnv), cfg.SecretEnvPairs...)
	cmd.Dir = cfg.RestrictedRoot

	return sandboxResult{Cmd: cmd, Cleanup: cleanup}, nil
}

// buildSBProfile generates the sandbox-exec profile in TinyScheme format.
// Strategy: deny everything by default, then explicit allow-list.
func buildSBProfile(cfg sandboxConfig) string {
	var sb strings.Builder

	sb.WriteString("(version 1)\n")
	sb.WriteString("(deny default)\n\n")

	// ── Always-required process operations ──────────────────────────────────
	sb.WriteString("; processo\n")
	sb.WriteString("(allow process-exec process-fork)\n")
	sb.WriteString("(allow signal (target self))\n")
	sb.WriteString("(allow process-info-pidinfo process-info-setcontrol)\n\n")

	// ── System ──────────────────────────────────────────────────────────────
	sb.WriteString("; sistema\n")
	sb.WriteString("(allow sysctl-read)\n")
	sb.WriteString("(allow mach-lookup)\n") // macOS IPC, required by almost everything
	sb.WriteString("(allow mach-register)\n")
	sb.WriteString("(allow iokit-open)\n") // device drivers (terminal, etc.)
	sb.WriteString("(allow system-socket)\n")
	sb.WriteString("(allow network-outbound)\n")
	sb.WriteString("(allow network-inbound)\n\n")

	// ── Path read-only (system) ──────────────────────────────────────────────
	sb.WriteString("; read-only system paths\n")
	sb.WriteString("(allow file-read* (literal \"/\"))\n")
	// Essential macOS paths
	systemRO := []string{
		"/usr",
		"/bin",
		"/sbin",
		"/System",
		"/Library/Frameworks",
		"/Library/Preferences",
		"/private/etc",
		"/private/tmp",
		"/private/var/folders", // macOS TMPDIR often resolves here
		"/dev",
		"/AppleInternal",
	}
	for _, p := range systemRO {
		sb.WriteString(fmt.Sprintf("(allow file-read* (subpath %q))\n", p))
	}
	// Append RO paths from config (may overlap)
	for _, p := range cfg.AllowedRO {
		if p != "" {
			sb.WriteString(fmt.Sprintf("(allow file-read* (subpath %q))\n", p))
		}
	}
	sb.WriteString("\n")

	// ── Path read-write ──────────────────────────────────────────────────────
	sb.WriteString("; read-write paths\n")
	for _, p := range cfg.AllowedRW {
		if p != "" {
			sb.WriteString(fmt.Sprintf("(allow file-read* file-write* (subpath %q))\n", p))
		}
	}
	sb.WriteString("\n")

	// ── Explicit sensitive path blocks ──────────────────────────────────────
	// Already blocked by (deny default), but explicit for clarity
	// and to prevent partial matches from opening them.
	sb.WriteString("; sensitive paths — explicit deny (redundant but explicit)\n")
	home, _ := os.UserHomeDir()
	for _, p := range sensitivePaths(home) {
		sb.WriteString(fmt.Sprintf("(deny file-read* file-write* (subpath %q))\n", p))
	}
	sb.WriteString("\n")
	for _, p := range workspaceSensitivePaths(cfg.RestrictedRoot) {
		sb.WriteString(fmt.Sprintf("(deny file-read* file-write* (subpath %q))\n", p))
	}
	sb.WriteString("\n")

	return sb.String()
}
