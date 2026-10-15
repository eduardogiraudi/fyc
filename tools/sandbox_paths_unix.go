//go:build !windows

package tools

import "os"

// standardAllowedDirs builds standard RW/RO lists from home/cache/tmp.
func standardAllowedDirs(restrictedRoot string) (rw []string, ro []string) {
	home, _ := os.UserHomeDir()
	cacheDir, _ := os.UserCacheDir()
	tmpDir := os.TempDir()

	rw = []string{
		restrictedRoot,
		tmpDir,
		cacheDir,
		// common tool caches
		home + "/.cache",
		home + "/go/pkg",
		home + "/go/bin",
		home + "/.local/share/uv",
		home + "/.local/share/pnpm",
		home + "/.local/lib",
		home + "/.local/bin",
		home + "/.local/share",
		// package manager stores
		home + "/.npm",
		home + "/.pnpm-store",
		home + "/.yarn",
		home + "/.config/pnpm",
		home + "/.local/share/pnpm/store",
		home + "/.cargo",
		home + "/.rustup",
		home + "/.m2",
		home + "/.gradle",
		home + "/.rbenv",
		home + "/.bundle",
		home + "/.gem",
		home + "/.asdf",
		home + "/.sdkman",
		home + "/.nvm",
		home + "/.pyenv",
		// specific config dirs (granular — NOT the whole .config)
		home + "/.config/npm",
		home + "/.config/pip",
		home + "/.config/pycodestyle",
		home + "/.config/nvim",
		home + "/.config/git",
		home + "/.agents/skills",
		// misc
		home + "/.yarnrc",
		home + "/.yarnrc.yml",
		// device/shm (linux only, ignored on darwin if absent)
		"/dev/shm",
	}

	ro = []string{
		"/usr",
		"/bin",
		"/lib",
		"/lib64",
		"/etc",
		"/dev",
		"/proc",
		"/run", // systemd-resolved socket, /run/nscd/socket — required by getaddrinfo
		"/var", // /var/cache/nscd, /var/run → symlink to /run on Ubuntu
		// macOS paths
		"/System",
		"/Library",
		"/private",
	}

	return rw, ro
}
