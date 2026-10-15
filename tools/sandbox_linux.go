//go:build linux

package tools

import (
	"context"
	"os"
	"os/exec"
)

// ── Self-re-exec pattern constants ──────────────────────────────────────────
//
// re-execute the binary because RestrictPaths affects the entire process.
// Pattern: parent launches itself with _FYC_SANDBOX_CHILD=1 + config JSON.

// MaybeRunAsSandboxChild must be called FIRST in main().
// If we are the normal process, it's a no-op.

// buildSandboxCmd is the entry point for the Linux platform.
func buildSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, error) {
	return buildLinuxSandboxCmd(ctx, cfg)
}

// buildLinuxSandboxCmd builds the cmd that launches ITSELF as a sandboxed child.
func buildLinuxSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, error) {
	args := []string{"--die-with-parent"}

	// Read-only dirs
	for _, p := range cfg.AllowedRO {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				args = append(args, "--ro-bind", p, p)
			}
		}
	}
	// Read-write dirs
	for _, p := range cfg.AllowedRW {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				args = append(args, "--bind", p, p)
			}
		}
	}
	// Block sensitive paths that fall inside RW dirs
	home, _ := os.UserHomeDir()
	for _, p := range sensitivePaths(home) {
		args = append(args, "--tmpfs", p) // mount empty tmpfs on top
	}
	for _, p := range workspaceSensitivePaths(cfg.RestrictedRoot) {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--tmpfs", p)
		}
	}
	args = append(args, "--dev", "/dev") // ← NOT --dev-bind
	args = append(args, "--proc", "/proc")
	args = append(args, "--", "sh", "-c", cfg.CmdStr)

	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Env = append(filteredEnv(cfg.DenyEnv), cfg.SecretEnvPairs...)
	cmd.Dir = cfg.RestrictedRoot

	return sandboxResult{Cmd: cmd, Cleanup: func() {}}, nil
}

// MaybeRunAsSandboxChild is a no-op on Linux: we use bwrap directly.
func MaybeRunAsSandboxChild() {}

// MaybeRunAsSandboxRunner is a no-op on Linux (Windows-only dedicated-user mode).
func MaybeRunAsSandboxRunner() {}
