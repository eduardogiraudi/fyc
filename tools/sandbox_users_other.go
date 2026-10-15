//go:build !windows

package tools

import "runtime"

// The dedicated-user sandbox (Fase D1) is Windows-only: it
// creates a local account and plants read-ACL/deny ACEs on the host profile. On
// Linux/macOS the sandbox is external (bwrap / sandbox-exec) and needs none of
// this, so the setup is inert and reports Supported=false.

func sandboxSetup(opts SandboxSetupOptions) (SandboxSetupResult, error) {
	return SandboxSetupResult{
		Platform:  runtime.GOOS,
		Supported: false,
		Notes:     []string{"the dedicated-user sandbox is Windows-only; this platform uses " + sandboxImplName()},
	}, nil
}

func sandboxUsersStatus(workspace string) SandboxUsersStatus {
	return SandboxUsersStatus{Platform: runtime.GOOS, Supported: false}
}

func sandboxUsersReset() (SandboxUsersResetResult, error) {
	return SandboxUsersResetResult{Platform: runtime.GOOS, Supported: false}, nil
}

func sandboxImplName() string {
	switch runtime.GOOS {
	case "linux":
		return "bwrap"
	case "darwin":
		return "sandbox-exec"
	default:
		return "no sandbox"
	}
}
