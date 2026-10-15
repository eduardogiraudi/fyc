package config

import "strings"

// The LSP discovery overrides live in Config.LSP (see types.go) and are
// consumed by lsp.Discover through tools.LSPOptions. These setters keep the
// stored values tidy: blanks are dropped so a stray entry never turns into the
// current directory, and map keys are normalised.

// SetLSPSearchPaths replaces the configured search directories.
func SetLSPSearchPaths(paths []string) {
	config.LSP.SearchPaths = cleanStrings(paths)
}

// SetLSPDisabled replaces the list of canonical server names to skip.
func SetLSPDisabled(names []string) {
	config.LSP.Disabled = cleanStrings(names)
}

// SetLSPBinary pins a server (canonical name) to an explicit executable.
// Empty values are ignored; use ClearLSPBinary to remove a pin.
func SetLSPBinary(name, path string) {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if name == "" || path == "" {
		return
	}
	if config.LSP.Binaries == nil {
		config.LSP.Binaries = map[string]string{}
	}
	config.LSP.Binaries[name] = path
}

// ClearLSPBinary removes a per-server executable override.
func ClearLSPBinary(name string) {
	if config.LSP.Binaries == nil {
		return
	}
	delete(config.LSP.Binaries, strings.TrimSpace(name))
	if len(config.LSP.Binaries) == 0 {
		config.LSP.Binaries = nil
	}
}

// cleanStrings trims every entry and drops the empty ones, preserving order.
func cleanStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
