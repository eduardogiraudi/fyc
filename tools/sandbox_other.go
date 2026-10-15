//go:build !linux && !darwin && !windows

package tools

import (
	"context"
	"os/exec"
)

// MaybeRunAsSandboxChild is a no-op on unsupported platforms.
func MaybeRunAsSandboxChild() {}

// MaybeRunAsSandboxRunner is a no-op on unsupported platforms.
func MaybeRunAsSandboxRunner() {}

// buildSandboxCmd fallback without sandbox: only filters sensitive env vars.
func buildSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.CmdStr)
	cmd.Env = append(filteredEnv(cfg.DenyEnv), cfg.SecretEnvPairs...)
	cmd.Dir = cfg.RestrictedRoot
	return sandboxResult{Cmd: cmd, Cleanup: func() {}}, nil
}
