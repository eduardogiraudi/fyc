// Package plugins installs portable Agent Plugins without running package code.
package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fuckyourcli/types"
)

const PluginSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
const MCPSchema = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
const maxFile = 8 << 20
const maxTotal = 64 << 20
const maxFiles = 4096

var identifier = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type File struct {
	Data       []byte
	Executable bool
}

type Skill struct{ Name, Description, Path string }

type Server struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type Package struct {
	Name, Version, Description string
	Files                      map[string]File
	Skills                     []Skill
	Servers                    map[string]Server
}

func validID(s string) bool { return len(s) > 0 && len(s) <= 64 && identifier.MatchString(s) }

func safePath(s string) bool {
	return s != "" && s != "." && !strings.ContainsAny(s, "\\:\x00") && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("package exceeds size limit (%d bytes)", limit)
	}
	return b, err
}

// junkDirs are skipped while reading a local source: dependency and tool-cache
// directories embed machine-specific paths, symlinks or compiled artifacts and
// can never be part of a portable plugin.
var junkDirs = map[string]bool{
	".venv": true, "venv": true, "node_modules": true, "__pycache__": true,
	".pytest_cache": true, ".ruff_cache": true, ".mypy_cache": true,
	".tox": true, ".nox": true,
}

// snapshot reads a whole directory into memory. source is a directory the user
// pointed add at: repository metadata (.git, including a worktree pointer
// file) and junkDirs are skipped. The installed bundle is read strictly
// instead: every file present takes part in the integrity digest, so a runtime
// that writes into its own plugin directory disables the plugin instead of
// silently replacing approved bytes.
func snapshot(dir string, source bool) (map[string]File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := map[string]File{}
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if source {
			// A development tree carries repository metadata and dependency or
			// cache directories that can never be portable plugin content:
			// machine-specific paths, symlinks, compiled artifacts.
			if d.Name() == ".git" || junkDirs[d.Name()] {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		if !safePath(p) {
			return fmt.Errorf("unsafe package path %q", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("symlinks and special files are not allowed: %s", p)
		}
		f, err := root.Open(p)
		if err != nil {
			return err
		}
		data, err := readLimited(f, maxFile)
		f.Close()
		if err != nil {
			return err
		}
		total += len(data)
		if total > maxTotal || len(files) >= maxFiles {
			return fmt.Errorf("package exceeds file/size limits")
		}
		files[p] = File{data, info.Mode()&0o111 != 0}
		return nil
	})
	return files, err
}

func digest(files map[string]File) string {
	h := sha256.New()
	for _, p := range sortedKeys(files) {
		f := files[p]
		// Permission bits differ across hosts; approval covers paths and bytes.
		fmt.Fprintf(h, "%d:%s:%d:", len(p), p, len(f.Data))
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parse(files map[string]File) (*Package, error) {
	seen := map[string]bool{}
	total := 0
	for name, file := range files {
		if !safePath(name) || seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("unsafe or duplicate package path %q", name)
		}
		seen[strings.ToLower(name)] = true
		total += len(file.Data)
		if len(file.Data) > maxFile || total > maxTotal || len(files) > maxFiles {
			return nil, fmt.Errorf("package exceeds file/size limits")
		}
	}
	var manifest struct {
		Schema      string `json:"$schema"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	f, ok := files["plugin.json"]
	if !ok {
		return nil, fmt.Errorf("plugin.json missing: select a plugin directory with --subdir")
	}
	if err := json.Unmarshal(f.Data, &manifest); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	if manifest.Schema != PluginSchema {
		return nil, fmt.Errorf("unsupported plugin schema %q (expected Agent Plugins 1.0.0)", manifest.Schema)
	}
	if !validID(manifest.Name) {
		return nil, fmt.Errorf("invalid plugin name %q", manifest.Name)
	}
	p := &Package{Name: manifest.Name, Version: manifest.Version, Description: manifest.Description, Files: files, Servers: map[string]Server{}}
	for _, name := range sortedKeys(files) {
		parts := strings.Split(name, "/")
		if len(parts) != 3 || parts[0] != "skills" || parts[2] != "SKILL.md" {
			continue
		}
		if !validID(parts[1]) {
			return nil, fmt.Errorf("invalid skill directory: %s", name)
		}
		n, desc := skillMetadata(string(files[name].Data))
		if n != parts[1] || desc == "" {
			return nil, fmt.Errorf("%s requires matching name and non-empty description in YAML frontmatter", name)
		}
		p.Skills = append(p.Skills, Skill{p.Name + "--" + n, desc, name})
	}
	if f, ok := files["mcp.json"]; ok {
		var doc struct {
			Schema  string            `json:"$schema"`
			Servers map[string]Server `json:"mcpServers"`
		}
		dec := json.NewDecoder(bytes.NewReader(f.Data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mcp.json (unsupported fields/authentication must be configured separately): %w", err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("mcp.json: trailing JSON")
		}
		if doc.Schema != MCPSchema {
			return nil, fmt.Errorf("unsupported MCP schema %q", doc.Schema)
		}
		for _, name := range sortedKeys(doc.Servers) {
			s := doc.Servers[name]
			if !validID(name) {
				return nil, fmt.Errorf("invalid MCP server name %q", name)
			}
			if err := validateServer(s); err != nil {
				return nil, fmt.Errorf("MCP server %s: %w", name, err)
			}
			p.Servers[name] = s
		}
	}
	if len(p.Skills) == 0 && len(p.Servers) == 0 {
		return nil, fmt.Errorf("plugin has no supported skills or MCP servers")
	}
	return p, nil
}

func validateServer(s Server) error {
	switch s.Type {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" || s.URL != "" || len(s.Headers) > 0 {
			return fmt.Errorf("invalid stdio configuration")
		}
		for _, key := range []string{"PLUGIN_ROOT", "PLUGIN_DATA"} {
			if _, ok := s.Env[key]; ok {
				return fmt.Errorf("reserved env %s", key)
			}
		}
		if s.Cwd != "" && !strings.HasPrefix(s.Cwd, "./") && s.Cwd != "${PLUGIN_ROOT}" && !strings.HasPrefix(s.Cwd, "${PLUGIN_ROOT}/") && s.Cwd != "${PLUGIN_DATA}" && !strings.HasPrefix(s.Cwd, "${PLUGIN_DATA}/") {
			return fmt.Errorf("invalid cwd")
		}
		for _, base := range []string{"./", "${PLUGIN_ROOT}/", "${PLUGIN_DATA}/"} {
			if strings.HasPrefix(s.Cwd, base) {
				tail := strings.TrimPrefix(s.Cwd, base)
				if tail != "" && !safePath(tail) {
					return fmt.Errorf("unsafe cwd")
				}
			}
		}
		if strings.HasPrefix(s.Command, "./") && !safePath(strings.TrimPrefix(s.Command, "./")) {
			return fmt.Errorf("unsafe command")
		}
	case "streamable-http", "sse":
		if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 || s.Cwd != "" {
			return fmt.Errorf("invalid HTTP configuration")
		}
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("invalid MCP URL")
		}
	default:
		return fmt.Errorf("unsupported MCP transport %q", s.Type)
	}
	return nil
}

// skillMetadata reads portable name/description fields, including block scalars.
func skillMetadata(text string) (name, description string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return
		}
		key, value, ok := strings.Cut(lines[i], ":")
		if !ok || (key != "name" && key != "description") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == ">" || value == "|" || value == ">-" || value == "|-" {
			var chunks []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t")) {
				i++
				chunks = append(chunks, strings.TrimSpace(lines[i]))
			}
			value = strings.Join(chunks, " ")
		} else if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if key == "name" {
			name = value
		} else {
			description = value
		}
	}
	return "", ""
}

func (p *Package) MCPServers(root, dataDir string, expand func(string) (string, error)) ([]types.MCPServerConfig, error) {
	var out []types.MCPServerConfig
	for _, name := range sortedKeys(p.Servers) {
		s := p.Servers[name]
		// Expand once: credentials must not be interpreted as placeholders.
		resolve := func(v string) (string, error) {
			v = strings.ReplaceAll(v, "${PLUGIN_ROOT}", root)
			v = strings.ReplaceAll(v, "${PLUGIN_DATA}", dataDir)
			return expand(v)
		}
		cfg := types.MCPServerConfig{Name: p.Name + "--" + name, Transport: s.Type, Env: map[string]string{}, Headers: map[string]string{}}
		var err error
		cfg.Command, err = resolve(s.Command)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(cfg.Command, "./") {
			cfg.Command = filepath.Join(root, cfg.Command)
		}
		cfg.Endpoint, err = resolve(s.URL)
		if err != nil {
			return nil, err
		}
		if s.Type == "streamable-http" {
			cfg.Transport = "streamableHttp"
		}
		if s.Type == "stdio" {
			cfg.WorkingDirectory = root
			if s.Cwd != "" {
				cfg.WorkingDirectory, err = resolve(s.Cwd)
				if err != nil {
					return nil, err
				}
				if strings.HasPrefix(cfg.WorkingDirectory, "./") {
					cfg.WorkingDirectory = filepath.Join(root, cfg.WorkingDirectory)
				}
			}
			cfg.Env["PLUGIN_ROOT"], cfg.Env["PLUGIN_DATA"] = root, dataDir
			// Keep runtime caches out of the integrity-checked bundle: Python
			// writes __pycache__ next to the imported sources and uv creates
			// .venv in the project directory, and either would change the
			// approved digest on the next start. Both are pointed at the mutable
			// data dir; a plugin can still override either through its own env.
			cfg.Env["PYTHONPYCACHEPREFIX"] = filepath.Join(dataDir, "pycache")
			cfg.Env["UV_PROJECT_ENVIRONMENT"] = filepath.Join(dataDir, "venv")
		}
		for _, arg := range s.Args {
			v, err := resolve(arg)
			if err != nil {
				return nil, err
			}
			cfg.Args = append(cfg.Args, v)
		}
		for k, v := range s.Env {
			cfg.Env[k], err = resolve(v)
			if err != nil {
				return nil, err
			}
		}
		for k, v := range s.Headers {
			cfg.Headers[k], err = resolve(v)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, cfg)
	}
	return out, nil
}
