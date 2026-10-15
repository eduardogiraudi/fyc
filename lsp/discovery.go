// Package discovery finds LSP executables already installed on the system.
// It checks (in order): system PATH, Neovim Mason, VS Code extensions, Zed.
package lsp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LSPInfo describes a discovered language server.
type LSPInfo struct {
	Name     string   // canonical name, e.g. "rust-analyzer"
	Language string   // e.g. "rust"
	Binary   string   // absolute path to executable
	Args     []string // extra args needed to start in LSP mode
	Source   string   // where it was found: "config", "path", "mason", "vscode", "zed"
	InitOpts any      // optional initializationOptions for this LSP
}

// candidate maps a well-known binary name to language metadata.
type candidate struct {
	name     string
	language string
	args     []string // extra args to pass to start in --stdio mode if needed
	zedNames []string // alternative subdirectory names used by Zed (if different from binary)
}

// knownLSPs is the list of LSPs we know how to find and start.
// The binary name is the key used for PATH/Mason lookup.
var knownLSPs = []struct {
	binary string
	candidate
}{
	{"clangd", candidate{"clangd", "c/c++", nil, nil}},
	{"rust-analyzer", candidate{"rust-analyzer", "rust", nil, nil}},
	{"gopls", candidate{"gopls", "go", nil, nil}},
	{"pyright-langserver", candidate{"pyright", "python", []string{"--stdio"}, []string{"basedpyright", "pyright"}}},
	{"pylsp", candidate{"pylsp", "python", nil, nil}},
	{"ruff", candidate{"ruff", "python", []string{"server"}, nil}},
	{"typescript-language-server", candidate{"tsserver", "typescript/javascript", []string{"--stdio"}, nil}},
	{"vtsls", candidate{"vtsls", "typescript/javascript", []string{"--stdio"}, nil}},
	{"vscode-html-language-server", candidate{"html-ls", "html", []string{"--stdio"}, nil}},
	{"vscode-css-language-server", candidate{"css-ls", "css", []string{"--stdio"}, nil}},
	{"vscode-json-language-server", candidate{"json-ls", "json", []string{"--stdio"}, []string{"json-language-server"}}},
	{"yaml-language-server", candidate{"yaml-ls", "yaml", []string{"--stdio"}, nil}},
	{"lua-language-server", candidate{"lua-ls", "lua", nil, nil}},
	{"bash-language-server", candidate{"bash-ls", "bash", []string{"start"}, nil}},
	{"eslint", candidate{"eslint-ls", "javascript/typescript", []string{"--stdio"}, nil}},
	{"tailwindcss-language-server", candidate{"tailwindcss-ls", "css/html", []string{"--stdio"}, nil}},
	{"solargraph", candidate{"solargraph", "ruby", []string{"stdio"}, nil}},
	{"kotlin-language-server", candidate{"kotlin-ls", "kotlin", nil, nil}},
	{"jdtls", candidate{"jdtls", "java", nil, nil}},
	{"intelephense", candidate{"intelephense", "php", []string{"--stdio"}, nil}},
	{"zls", candidate{"zls", "zig", nil, nil}},
	{"marksman", candidate{"marksman", "markdown", []string{"server"}, nil}},
	{"taplo", candidate{"taplo", "toml", []string{"lsp", "stdio"}, nil}},
	{"nil", candidate{"nil", "nix", nil, nil}},
	{"terraform-ls", candidate{"terraform-ls", "terraform", []string{"serve"}, nil}},
	{"elixir-ls", candidate{"elixir-ls", "elixir", nil, nil}},
	{"erlang_ls", candidate{"erlang-ls", "erlang", nil, nil}},
	{"hls", candidate{"hls", "haskell", []string{"--lsp"}, nil}},
	{"ocamllsp", candidate{"ocamllsp", "ocaml", nil, nil}},
	{"svelte-language-server", candidate{"svelte-ls", "svelte", []string{"--stdio"}, nil}},
	{"vue-language-server", candidate{"vue-ls", "vue", []string{"--stdio"}, nil}},
	{"sqls", candidate{"sqls", "sql", nil, nil}},
	{"docker-langserver", candidate{"docker-ls", "dockerfile", []string{"--stdio"}, nil}},
}

// Options customises LSP discovery. The zero value reproduces the built-in
// behaviour (system PATH, then the editor caches).
type Options struct {
	// SearchPaths are extra directories probed for a server's binary. They are
	// checked before PATH, so a directory listed here wins over the system
	// install: the user's explicit choice always takes precedence.
	SearchPaths []string
	// Binaries pins a server (by canonical name, e.g. "gopls") to an explicit
	// executable. It overrides every other source.
	Binaries map[string]string
	// Disabled lists canonical server names that must never be started.
	Disabled []string
}

// Discover finds all available LSPs on the system, applying the user options.
// Resolution order: Binaries → SearchPaths → PATH → Mason → VS Code → Zed, so
// anything the user configured wins over the auto-detected sources.
func Discover(opts Options) []LSPInfo {
	home, _ := os.UserHomeDir()
	var found []LSPInfo
	seen := map[string]bool{} // deduplicate by canonical name
	disabled := nameSet(opts.Disabled)
	searchDirs := expandHomePaths(opts.SearchPaths)

	for _, k := range knownLSPs {
		if seen[k.name] || disabled[k.name] {
			continue
		}

		// 0. Explicit override from the configuration (highest priority).
		if p, ok := opts.Binaries[k.name]; ok {
			// resolveExec also probes the Windows launcher extensions (.cmd/
			// .exe/.bat), so a pin written without one still resolves.
			if p = resolveExec(expandHome(p)); p != "" {
				found = append(found, LSPInfo{
					Name: k.name, Language: k.language,
					Binary: p, Args: k.args, Source: "config",
				})
				seen[k.name] = true
				continue
			}
		}

		// 1. User-configured search directories (before PATH).
		if p := probeDirs(searchDirs, k.binary); p != "" {
			found = append(found, LSPInfo{
				Name: k.name, Language: k.language,
				Binary: p, Args: k.args, Source: "config",
			})
			seen[k.name] = true
			continue
		}

		// 2. System PATH
		if p, err := exec.LookPath(k.binary); err == nil {
			found = append(found, LSPInfo{
				Name: k.name, Language: k.language,
				Binary: p, Args: k.args, Source: "path",
			})
			seen[k.name] = true
			continue
		}

		// 3. Neovim Mason
		if p := masonPath(home, k.binary); p != "" {
			found = append(found, LSPInfo{
				Name: k.name, Language: k.language,
				Binary: p, Args: k.args, Source: "mason",
			})
			seen[k.name] = true
			continue
		}

		// 4. VS Code extensions
		if p := vscodePath(home, k.binary); p != "" {
			found = append(found, LSPInfo{
				Name: k.name, Language: k.language,
				Binary: p, Args: k.args, Source: "vscode",
			})
			seen[k.name] = true
			continue
		}

		// 5. Zed
		if p := zedPath(home, k.binary, k.zedNames); p != "" {
			found = append(found, LSPInfo{
				Name: k.name, Language: k.language,
				Binary: p, Args: k.args, Source: "zed",
			})
			seen[k.name] = true
		}
	}

	return found
}

// ---------- option helpers ----------

// nameSet turns a list of canonical names into a lookup set, ignoring blanks.
func nameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	return set
}

// expandHomePaths trims each configured directory and expands a leading "~",
// dropping the empty entries so a stray value never turns into the CWD.
func expandHomePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(expandHome(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// probeDirs returns the first executable named binary inside one of dirs.
func probeDirs(dirs []string, binary string) string {
	for _, dir := range dirs {
		if p := execIn(dir, binary); p != "" {
			return p
		}
	}
	return ""
}

// execIn joins dir and binary and returns the path only when it is executable.
func execIn(dir, binary string) string {
	return resolveExec(filepath.Join(dir, binary))
}

// resolveExec returns p when it is executable, also trying the Windows launcher
// extensions (.cmd/.exe/.bat) so a path written without one still resolves.
func resolveExec(p string) string {
	if isExec(p) {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".cmd", ".exe", ".bat"} {
			if isExec(p + ext) {
				return p + ext
			}
		}
	}
	return ""
}

// expandHome replaces a leading "~" (or "~/", "~\") with the user home
// directory, so a configured path can be written portably.
func expandHome(p string) string {
	if p != "~" && !(len(p) > 1 && p[0] == '~' && (p[1] == '/' || p[1] == '\\')) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// ---------- source-specific finders ----------

// masonPath looks for a binary installed by Neovim Mason.
//
// Mason layout:
//
//	~/.local/share/nvim/mason/
//	├── bin/<binary>          ← symlink to packages/<binary>/<binary>  (Unix)
//	└── packages/<binary>/
//	    └── <binary>
//
// On Windows: %LOCALAPPDATA%\nvim-data\mason\bin\<binary>.cmd  (Node-based)
// or          %LOCALAPPDATA%\nvim-data\mason\bin\<binary>.exe  (native)
func masonPath(home, binary string) string {
	// Unix (Linux / macOS): symlink in bin/
	p := filepath.Join(home, ".local", "share", "nvim", "mason", "bin", binary)
	if isExec(p) {
		return p
	}

	// Windows: %LOCALAPPDATA%\nvim-data\mason\bin\
	// APPDATA points to Roaming; LocalAppData is one level up + Local.
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		// Fallback: derive from APPDATA (Roaming → Local)
		if appData := os.Getenv("APPDATA"); appData != "" {
			localAppData = filepath.Join(filepath.Dir(appData), "Local")
		}
	}
	if localAppData != "" {
		base := filepath.Join(localAppData, "nvim-data", "mason", "bin", binary)
		for _, ext := range []string{"", ".cmd", ".exe"} {
			if isExec(base + ext) {
				return base + ext
			}
		}
	}

	return ""
}

// vscodePath looks for a binary shipped inside a VS Code extension directory.
//
// Extension layout examples:
//
//	rust-lang.rust-analyzer-*/server/rust-analyzer           (native binary)
//	rust-lang.rust-analyzer-*/server/<os>/rust-analyzer      (platform subfolder)
//	<ext>-*/node_modules/.bin/<binary>                       (Node wrapper script)
//	<ext>-*/node_modules/vscode-langservers-extracted/bin/<b> (vscode-langservers)
func vscodePath(home, binary string) string {
	for _, extDir := range vscodeExtDirs(home) {
		entries, err := os.ReadDir(extDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			base := filepath.Join(extDir, e.Name())

			// Build candidate list (order matters: prefer native over Node wrappers).
			candidates := []string{
				// Native binary with platform subfolder (e.g. rust-analyzer on some distros)
				filepath.Join(base, "server", runtime.GOOS, binary),
				// Native binary in server/
				filepath.Join(base, "server", binary),
				// Native binary in bin/
				filepath.Join(base, "bin", binary),
				// Node wrapper in node_modules/.bin/
				filepath.Join(base, "node_modules", ".bin", binary),
				// vscode-langservers-extracted layout
				filepath.Join(base, "node_modules", "vscode-langservers-extracted", "bin", binary),
				// Binary at extension root (rare)
				filepath.Join(base, binary),
			}

			// On Windows also try .cmd / .exe variants.
			// Build the extra slice separately to avoid mutating candidates
			// while ranging over it (the original bug).
			if runtime.GOOS == "windows" {
				var winExtra []string
				for _, c := range candidates {
					winExtra = append(winExtra, c+".cmd", c+".exe")
				}
				candidates = append(candidates, winExtra...)
			}

			for _, c := range candidates {
				if isExec(c) {
					return c
				}
			}
		}
	}
	return ""
}

// vscodeExtDirs returns candidate extension directories for the current OS.
func vscodeExtDirs(home string) []string {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		return []string{
			filepath.Join(appData, "Code", "User", "extensions"),
			filepath.Join(home, ".vscode", "extensions"),
		}
	case "darwin":
		return []string{
			filepath.Join(home, ".vscode", "extensions"),
			filepath.Join(home, "Library", "Application Support", "Code", "User", "extensions"),
		}
	default: // linux
		return []string{
			filepath.Join(home, ".vscode", "extensions"),
			filepath.Join(home, ".vscode-server", "extensions"),
		}
	}
}

// zedPath looks for a binary downloaded by Zed into its languages cache.
//
// Zed layout (Linux / macOS / Windows):
//
//	$XDG_DATA_HOME/zed/languages/<binary>/<binary>_<version>_<extra>
//
// The filename is versioned (e.g. gopls_0.22.0_go_1.25.0), so we scan the
// subdirectory for any file whose name starts with the binary name.
//
// Node-based servers use a different sub-layout:
//
//	…/languages/<binary>/node_modules/.bin/<binary>
//	…/languages/<binary>/node_modules/vscode-langservers-extracted/bin/<binary>
//
// Flatpak installs may use $FLATPAK_XDG_DATA_HOME instead.
//
// zedAliases allows callers to supply additional subdirectory names that Zed
// may use for the same server (e.g. "basedpyright" for pyright-langserver).
func zedPath(home, binary string, zedAliases []string) string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	var langsDirs []string
	switch runtime.GOOS {
	case "darwin":
		langsDirs = []string{
			filepath.Join(home, "Library", "Application Support", "Zed", "languages"),
			// XDG layout used in some non-App-Store builds
			filepath.Join(dataHome, "zed", "languages"),
		}
	case "windows":
		// Zed's user-data-dir on Windows is %LOCALAPPDATA%\Zed (see
		// https://zed.dev/docs/reference/cli), so the servers it downloads
		// live under %LOCALAPPDATA%\Zed\languages. %APPDATA% is a fallback.
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			langsDirs = append(langsDirs, filepath.Join(la, "Zed", "languages"))
		}
		if ad := os.Getenv("APPDATA"); ad != "" {
			langsDirs = append(langsDirs, filepath.Join(ad, "Zed", "languages"))
		}
	default: // linux
		langsDirs = []string{
			filepath.Join(dataHome, "zed", "languages"),
		}
		if fp := os.Getenv("FLATPAK_XDG_DATA_HOME"); fp != "" {
			langsDirs = append(langsDirs, filepath.Join(fp, "zed", "languages"))
		}
	}

	// Build the full set of subdirectory names to probe: the canonical binary
	// name first, then any Zed-specific aliases (e.g. "basedpyright").
	subDirNames := append([]string{binary}, zedAliases...)

	for _, langsDir := range langsDirs {
		for _, subDirName := range subDirNames {
			subDir := filepath.Join(langsDir, subDirName)

			// Case 1: native binary with versioned filename.
			// Zed names the file after the *subdirectory* (e.g. basedpyright_1.x.y),
			// so we accept any executable whose name starts with the subdir name
			// OR the canonical binary name.
			if entries, err := os.ReadDir(subDir); err == nil {
				for _, e := range entries {
					if !e.IsDir() &&
						(strings.HasPrefix(e.Name(), subDirName) || strings.HasPrefix(e.Name(), binary)) {
						if p := filepath.Join(subDir, e.Name()); isExec(p) {
							return p
						}
					}
				}
			}

			// Case 2: Node-based server – node_modules/.bin/<binary>
			if p := execIn(filepath.Join(subDir, "node_modules", ".bin"), binary); p != "" {
				return p
			}

			// Case 3: vscode-langservers-extracted layout
			if p := execIn(filepath.Join(subDir, "node_modules", "vscode-langservers-extracted", "bin"), binary); p != "" {
				return p
			}
		}
	}
	return ""
}

// isExec returns true if the path exists and is executable.
func isExec(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // no exec bit on windows
	}
	return info.Mode()&0o111 != 0
}

// VSCodeExtensionMeta is partial package.json for VS Code extensions.
type VSCodeExtensionMeta struct {
	Name        string `json:"name"`
	Contributes struct {
		Languages []struct {
			ID string `json:"id"`
		} `json:"languages"`
	} `json:"contributes"`
}

// ListVSCodeExtensions returns parsed metadata for all VS Code extensions.
// Useful for building a richer discovery in the future.
func ListVSCodeExtensions(home string) []VSCodeExtensionMeta {
	var out []VSCodeExtensionMeta
	for _, dir := range vscodeExtDirs(home) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pkg := filepath.Join(dir, e.Name(), "package.json")
			data, err := os.ReadFile(pkg)
			if err != nil {
				continue
			}
			var meta VSCodeExtensionMeta
			if json.Unmarshal(data, &meta) == nil {
				out = append(out, meta)
			}
		}
	}
	return out
}

// LanguagesForLSP returns a comma-joined list of languages an LSP supports,
// given a slice of discovered LSPs.
func LanguagesForLSP(infos []LSPInfo) string {
	langs := make([]string, 0, len(infos))
	for _, i := range infos {
		langs = append(langs, i.Language+"("+i.Name+")")
	}
	return strings.Join(langs, ", ")
}
