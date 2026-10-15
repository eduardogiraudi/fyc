package lsp

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestExtensionsForLanguage(t *testing.T) {
	tests := []struct {
		lang     string
		expected []string
	}{
		{"go", []string{".go"}},
		{"python", []string{".py"}},
		{"javascript", []string{".js", ".jsx", ".mjs", ".cjs"}},
		{"typescript/javascript", []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}},
		{"unknown", nil},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			got := extensionsForLanguage(tt.lang)
			if len(got) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("at index %d: expected %v, got %v", i, tt.expected[i], got[i])
				}
			}
		})
	}
}

func TestFileExt(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"main.go", ".go"},
		{"test.py", ".py"},
		{"README", ""},
		{".gitignore", ".gitignore"},
		{"path/to/file.js", ".js"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := fileExt(tt.path); got != tt.expected {
				t.Errorf("fileExt(%q) = %q; want %q", tt.path, got, tt.expected)
			}
		})
	}
}

func TestManager_ClientForFile(t *testing.T) {
	ctx := context.Background()
	infos := []LSPInfo{
		{Name: "gopls", Language: "go", Binary: "/nonexistent/gopls", Args: []string{}},
		{Name: "pyright", Language: "python", Binary: "/nonexistent/pyright-langserver", Args: []string{}},
	}

	m, err := NewManager(ctx, "/tmp", infos, Options{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Close()

	_, err = m.clientForFile("test.go")
	if err == nil {
		t.Error("expected error for missing LSP, but got nil")
	}
}

// ---------- integration tests (require real LSPs via Discover) ----------

// pythonLSPManager creates a Manager with only Python LSPs discovered on the system.
// Skips the test if none are found.
func pythonLSPManager(t *testing.T, rootDir string) *Manager {
	t.Helper()
	all := Discover(Options{})
	var pyInfos []LSPInfo
	for _, info := range all {
		if info.Language == "python" {
			pyInfos = append(pyInfos, info)
		}
	}
	if len(pyInfos) == 0 {
		t.Skip("no Python LSP found on this system")
	}
	t.Logf("using Python LSP: %s (%s)", pyInfos[0].Name, pyInfos[0].Binary)

	ctx := context.Background()
	m, err := NewManager(ctx, rootDir, pyInfos, Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Give the LSP time to finish initializing before any requests.
	time.Sleep(500 * time.Millisecond)
	return m
}

// writePyFile writes a small Python file in dir and returns its absolute path.
func writePyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := dir + "/" + name
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestIntegration_PythonHover(t *testing.T) {
	dir := t.TempDir()
	src := `def greet(name: str) -> str:
    """Return a greeting."""
    return "Hello, " + name

result = greet("world")
`
	pyFile := writePyFile(t, dir, "hello.py", src)
	m := pythonLSPManager(t, dir)
	defer m.Close()

	// Hover over "greet" on line 4 (0-based), char 9 — the call site.
	pos := Position{File: pyFile, Line: 4, Character: 9}

	// Retry: pyright needs a moment after didOpen to process the file.
	var result *HoverResult
	var err error
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, err = m.Hover(ctx, pos)
		cancel()
		if err == nil && result.Contents != "" && result.Contents != "(no hover info)" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	t.Logf("Hover result: %s", result.Contents)
	if result.Contents == "" || result.Contents == "(no hover info)" {
		t.Error("expected non-empty hover info for 'greet'")
	}
}

func TestIntegration_PythonDefinition(t *testing.T) {
	dir := t.TempDir()
	src := `def add(a: int, b: int) -> int:
    return a + b

x = add(1, 2)
`
	pyFile := writePyFile(t, dir, "math_utils.py", src)
	m := pythonLSPManager(t, dir)
	defer m.Close()

	// Ask for definition of "add" at the call site (line 3, char 4).
	pos := Position{File: pyFile, Line: 3, Character: 4}

	var locs []Location
	var err error
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		locs, err = m.Definition(ctx, pos)
		cancel()
		if err == nil && len(locs) > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	t.Logf("Definition locations: %+v", locs)
	if len(locs) == 0 {
		t.Error("expected at least one definition location for 'add'")
		return
	}
	if locs[0].Line != 0 {
		t.Errorf("expected definition on line 0, got %d", locs[0].Line)
	}
}

func TestIntegration_PythonDiagnostics(t *testing.T) {
	dir := t.TempDir()
	// Intentional type error: passing a string where int is expected.
	src := `def add(a: int, b: int) -> int:
    return a + b

result: int = add("oops", 2)
`
	pyFile := writePyFile(t, dir, "broken.py", src)
	m := pythonLSPManager(t, dir)
	defer m.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	diags, err := m.Diagnostics(ctx, pyFile)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	t.Logf("Diagnostics (%d):", len(diags))
	for _, d := range diags {
		t.Logf("  line %d sev %d: %s", d.Line, d.Severity, d.Message)
	}
	// Note: pull diagnostics may be unsupported by some LSPs — result can be empty.
	// We just log; we don't fail if empty.
}

func TestIntegration_PythonReferences(t *testing.T) {
	dir := t.TempDir()
	src := `def helper():
    pass

helper()
helper()
`
	pyFile := writePyFile(t, dir, "refs.py", src)
	m := pythonLSPManager(t, dir)
	defer m.Close()

	// References to "helper" from its definition (line 0, char 4).
	pos := Position{File: pyFile, Line: 0, Character: 4}

	var locs []Location
	var err error
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		locs, err = m.References(ctx, pos)
		cancel()
		if err == nil && len(locs) >= 2 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	t.Logf("References (%d): %+v", len(locs), locs)
	if len(locs) < 2 {
		t.Errorf("expected at least 2 references to 'helper', got %d", len(locs))
	}
}
