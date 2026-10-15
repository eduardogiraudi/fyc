//go:build !windows

package tools

import "runtime"

// On Linux and macOS the sandbox is external (bwrap / sandbox-exec): there is
// nothing to plant on the host filesystem, so revoke/reset have no work to do.

// sandboxStatus describes the external sandbox in use.
func sandboxStatus(root string) SandboxInfo {
	info := SandboxInfo{Root: root, Network: true}
	switch runtime.GOOS {
	case "linux":
		info.Platform = "linux (bwrap)"
		info.Note = "bwrap: private mount namespace; the host filesystem is untouched"
	case "darwin":
		info.Platform = "darwin (sandbox-exec)"
		info.Note = "sandbox-exec: generated TinyScheme profile, deny-by-default"
	default:
		info.Platform = runtime.GOOS
		info.Note = "no sandbox on this platform: only sensitive env vars are filtered"
		return info
	}
	info.Enabled = true
	info.Profile = info.Platform
	rw, ro := standardAllowedDirs(root)
	info.Allow = rw
	info.AllowRO = ro
	info.Deny = append(sensitivePaths(homeDir()), workspaceSensitivePaths(root)...)
	return info
}

func sandboxACLRevoke(root string) (int, error) { return 0, nil }

func sandboxReset(root string) (int, error) { return 0, nil }
