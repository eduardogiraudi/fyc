package lsp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Manager holds multiple LSP clients (one per language server) and routes
// requests to the right one based on file extension.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client // key: LSPInfo.Name
	byExt   map[string]*Client // key: file extension (e.g. ".go")
	rootDir string
	ctx     context.Context
	// opts carries the user's discovery options so the heartbeat rescans with
	// the same precedence the initial discovery used.
	opts Options
}

// NewManager starts all discoverable LSPs for the given workspace root
// and spawns a background goroutine that rescans every 30 seconds for
// newly installed language servers.
func NewManager(ctx context.Context, rootDir string, infos []LSPInfo, opts Options) (*Manager, error) {
	m := &Manager{
		clients: make(map[string]*Client),
		byExt:   make(map[string]*Client),
		rootDir: rootDir,
		ctx:     ctx,
		opts:    opts,
	}

	for _, info := range infos {
		_ = m.startClient(info) // non-fatal: log inside
	}

	go m.heartbeat()

	return m, nil
}

// startClient starts a single LSP client and registers it.
// Must NOT be called with m.mu held.
func (m *Manager) startClient(info LSPInfo) error {
	c, err := New(m.ctx, info.Binary, info.Args, info.Name, m.rootDir)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.clients[info.Name] = c
	for _, ext := range extensionsForLanguage(info.Language) {
		if _, exists := m.byExt[ext]; !exists {
			m.byExt[ext] = c
		}
	}
	return nil
}

// heartbeat rescans the system for LSPs every 30 seconds and silently
// starts any newly discovered servers not already running.
func (m *Manager) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.scanAndAdd()
		}
	}
}

// scanAndAdd discovers all LSPs on the system and starts any that are
// not already registered in m.clients.
func (m *Manager) scanAndAdd() {
	for _, info := range Discover(m.opts) {
		m.mu.RLock()
		_, already := m.clients[info.Name]
		m.mu.RUnlock()

		if already {
			continue
		}

		// New LSP found — start it silently; ignore errors (binary may be
		// broken or not yet fully installed).
		_ = m.startClient(info)
	}
}

// Close shuts down all LSP clients.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		_ = c.Close()
	}
}

// ActiveLSPs returns a summary of running LSP clients.
func (m *Manager) ActiveLSPs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for name, c := range m.clients {
		var exts []string
		for ext, cc := range m.byExt {
			if cc == c {
				exts = append(exts, ext)
			}
		}
		out = append(out, fmt.Sprintf("%s → %s", name, strings.Join(exts, ", ")))
	}
	return out
}

// clientForFile returns the LSP client for a given file path.
func (m *Manager) clientForFile(filePath string) (*Client, error) {
	ext := fileExt(filePath)
	m.mu.RLock()
	c, ok := m.byExt[ext]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no LSP available for extension %q (file: %s)", ext, filePath)
	}
	return c, nil
}

// ---------- routed queries ----------

func (m *Manager) Definition(ctx context.Context, pos Position) ([]Location, error) {
	c, err := m.clientForFile(pos.File)
	if err != nil {
		return nil, err
	}
	return c.Definition(ctx, pos)
}

func (m *Manager) References(ctx context.Context, pos Position) ([]Location, error) {
	c, err := m.clientForFile(pos.File)
	if err != nil {
		return nil, err
	}
	return c.References(ctx, pos)
}

func (m *Manager) Hover(ctx context.Context, pos Position) (*HoverResult, error) {
	c, err := m.clientForFile(pos.File)
	if err != nil {
		return nil, err
	}
	return c.Hover(ctx, pos)
}

func (m *Manager) Diagnostics(ctx context.Context, filePath string) ([]Diagnostic, error) {
	c, err := m.clientForFile(filePath)
	if err != nil {
		return nil, err
	}
	return c.Diagnostics(ctx, filePath)
}

func (m *Manager) WorkspaceSymbols(ctx context.Context, lspName, query string) ([]SymbolResult, error) {
	m.mu.RLock()
	c, ok := m.clients[lspName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no LSP named %q", lspName)
	}
	return c.WorkspaceSymbols(ctx, query)
}

func (m *Manager) Rename(ctx context.Context, pos Position, newName string) (map[string]string, error) {
	c, err := m.clientForFile(pos.File)
	if err != nil {
		return nil, err
	}
	return c.Rename(ctx, pos, newName)
}

// ---------- helpers ----------

func fileExt(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return path[idx:]
}

func extensionsForLanguage(lang string) []string {
	lang = strings.ToLower(lang)
	m := map[string][]string{
		"go":                    {".go"},
		"rust":                  {".rs"},
		"python":                {".py"},
		"typescript":            {".ts", ".tsx"},
		"typescript/javascript": {".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
		"javascript":            {".js", ".jsx", ".mjs", ".cjs"},
		"c/c++":                 {".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
		"c":                     {".c", ".h"},
		"c++":                   {".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
		"lua":                   {".lua"},
		"ruby":                  {".rb"},
		"java":                  {".java"},
		"kotlin":                {".kt", ".kts"},
		"bash":                  {".sh", ".bash"},
		"yaml":                  {".yaml", ".yml"},
		"json":                  {".json"},
		"html":                  {".html", ".htm"},
		"css":                   {".css"},
		"markdown":              {".md", ".mdx"},
		"toml":                  {".toml"},
		"nix":                   {".nix"},
		"terraform":             {".tf", ".tfvars"},
		"zig":                   {".zig"},
		"elixir":                {".ex", ".exs"},
		"erlang":                {".erl", ".hrl"},
		"haskell":               {".hs", ".lhs"},
		"ocaml":                 {".ml", ".mli"},
		"svelte":                {".svelte"},
		"vue":                   {".vue"},
		"php":                   {".php"},
		"sql":                   {".sql"},
		"dockerfile":            {"Dockerfile", ".dockerfile"},
	}
	if exts, ok := m[lang]; ok {
		return exts
	}
	return nil
}
