//go:build windows

package tools

import (
	"os"
	"path/filepath"
)

// standardAllowedDirs returns the Windows equivalent of the POSIX RW/RO lists.
//
// RO covers %SystemRoot% and %ProgramFiles%: those trees are readable as-is
// through the system ACEs, so the sandbox plants no ACE there and leaves the
// read side to the allow-list. RW covers the workspace plus the user toolchain
// caches (GOCACHE, pnpm store, uv, cargo…) so go/uv/pnpm can write.
// for "ALL APPLICATION PACKAGES", so Phase 2 must NOT plant redundant ACEs
// there. RW covers the workspace plus the user toolchain
// caches (GOCACHE, pnpm store, uv, cargo…) so go/uv/pnpm can write.
func standardAllowedDirs(restrictedRoot string) (rw []string, ro []string) {
	home, _ := os.UserHomeDir()
	local := os.Getenv("LOCALAPPDATA") // == os.UserCacheDir() on Windows
	roaming := os.Getenv("APPDATA")
	windir := os.Getenv("SystemRoot") // usually C:\Windows
	progFiles := os.Getenv("ProgramFiles")
	progFilesX86 := os.Getenv("ProgramFiles(x86)")

	join := func(base string, elems ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, elems...)...)
	}

	rw = []string{
		restrictedRoot,
		os.TempDir(),
		local,
		join(local, "go-build"), // GOCACHE
		join(home, "go", "pkg"),
		join(home, "go", "bin"),
		join(local, "uv"), // uv toolchain/cache under %LOCALAPPDATA%
		join(home, ".cargo"),
		join(home, ".rustup"),
		join(roaming, "npm"),
		join(local, "pnpm"),
		join(local, "pnpm-store"),
		join(home, ".nuget"),
		join(home, ".gradle"),
		join(home, ".m2"),
		join(home, ".cache"),
		join(home, ".agents", "skills"),
	}

	ro = []string{
		windir,
		progFiles,
		progFilesX86,
		join(progFiles, "Go"),
	}

	return nonEmpty(rw), nonEmpty(ro)
}

// nonEmpty drops empty entries (an env var such as %ProgramFiles(x86)% may be
// unset), keeping the RO/RW lists free of relative paths.
func nonEmpty(paths []string) []string {
	out := paths[:0]
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
