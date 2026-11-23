package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fuckyourcli/lsp"
	"fuckyourcli/store"
)

func TestIgnoreManagerDirectoryRules(t *testing.T) {
	for _, name := range []string{".gitignore", ".fycignore"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"vendor/pkg", "project/deep/node_modules/pkg", "other/node_modules"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
					t.Fatal(err)
				}
			}
			for path, body := range map[string]string{
				name:                                     "vendor/\nlogs/\n",
				filepath.Join("project", "deep", name):   "node_modules/\n",
				"logs":                                   "regular file\n",
				"project/logs":                           "regular file\n",
				"other/vendor":                           "regular file\n",
				"vendor/pkg/index.go":                    "package vendor\n",
				"project/deep/node_modules/pkg/index.js": "export default 1\n",
			} {
				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			im := LoadIgnoreFiles(root)
			if len(im.files) != 2 {
				t.Fatalf("loaded %d ignore files, want 2", len(im.files))
			}
			for _, tc := range []struct {
				path string
				want bool
			}{
				{"vendor", true},
				{"vendor/pkg", true},
				{"vendor/pkg/index.go", true},
				{"project/deep/node_modules", true},
				{"project/deep/node_modules/pkg", true},
				{"project/deep/node_modules/pkg/index.js", true},
				{"other/node_modules", false},
				{"project/deep", false},
				{"logs", false},
				{"project/logs", false},
				{"other/vendor", false},
				{"missing/logs", false},
			} {
				t.Run(tc.path, func(t *testing.T) {
					if got := im.IsIgnored(filepath.Join(root, tc.path), root); got != tc.want {
						t.Errorf("IsIgnored(%q) = %v, want %v", tc.path, got, tc.want)
					}
				})
			}
		})
	}
}

func TestIgnoreManagerPrunesLSPScan(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".gitignore":                "node_modules/\n",
		"main.go":                   "package main\n",
		"node_modules/pkg/index.js": "export default 1\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	im := LoadIgnoreFiles(root)
	var mu sync.Mutex
	var visited []string
	exts := lsp.ScanExtensions(root, func(path string) bool {
		mu.Lock()
		visited = append(visited, path)
		mu.Unlock()
		return im.IsIgnored(path, root)
	})
	if _, ok := exts[".go"]; !ok {
		t.Error("scan missed visible Go file")
	}
	if _, ok := exts[".js"]; ok {
		t.Error("scan included ignored JavaScript file")
	}
	prefix := filepath.Join(root, "node_modules") + string(filepath.Separator)
	for _, path := range visited {
		if strings.HasPrefix(path, prefix) {
			t.Errorf("scan entered ignored directory: %s", path)
		}
	}
}

func TestIgnoreManagerNestedFYC(t *testing.T) {
	for _, rootFYC := range []bool{false, true} {
		name := "without root fyc"
		if rootFYC {
			name = "with root fyc"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"sub/deep", "sibling", "sub-other"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
					t.Fatal(err)
				}
			}
			for path, body := range map[string]string{
				".gitignore":             "git-only.txt\n",
				"sub/.gitignore":         "*\n",
				"sub/.fycignore":         "fyc-only.txt\n!git-only.txt\n",
				"sibling/.gitignore":     "local-git.txt\n",
				".dockerignore":          "*\n!dist/\n",
				"git-only.txt":           "needle\n",
				"sub/git-only.txt":       "needle\n",
				"sub/fyc-only.txt":       "needle\n",
				"sub/deep/git-only.txt":  "needle\n",
				"sub/deep/fyc-only.txt":  "needle\n",
				"sibling/git-only.txt":   "needle\n",
				"sibling/local-git.txt":  "needle\n",
				"sub-other/git-only.txt": "needle\n",
			} {
				writeToolTestFile(t, root, path, body)
			}
			if rootFYC {
				writeToolTestFile(t, root, ".fycignore", "")
			}
			im := LoadIgnoreFiles(root)
			assertIgnoreVisibility(t, root, im, map[string]bool{
				"git-only.txt":           !rootFYC,
				"sub/git-only.txt":       false,
				"sub/fyc-only.txt":       true,
				"sub/deep/git-only.txt":  false,
				"sub/deep/fyc-only.txt":  true,
				"sibling/git-only.txt":   !rootFYC,
				"sibling/local-git.txt":  !rootFYC,
				"sub-other/git-only.txt": !rootFYC,
			})
			if im.IsIgnored(filepath.Join(root, ".dockerignore"), root) {
				t.Fatal(".dockerignore must not be blocked by basename")
			}
		})
	}
}

func TestIgnoreManagerFYCInsideGitIgnoredDirectory(t *testing.T) {
	for _, body := range []string{"", "hidden.txt\n"} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "blocked", "local"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "blocked", "other"), 0755); err != nil {
				t.Fatal(err)
			}
			writeToolTestFile(t, root, ".gitignore", "blocked/\n")
			writeToolTestFile(t, root, "blocked/.gitignore", "*\n")
			writeToolTestFile(t, root, "blocked/local/.fycignore", body)
			writeToolTestFile(t, root, "blocked/local/source.go", "needle\n")
			writeToolTestFile(t, root, "blocked/local/hidden.txt", "needle\n")
			writeToolTestFile(t, root, "blocked/other/hidden.js", "needle\n")
			writeToolTestFile(t, root, "blocked/hidden.js", "needle\n")
			im := LoadIgnoreFiles(root)
			assertIgnoreVisibility(t, root, im, map[string]bool{
				"blocked/local/source.go":  false,
				"blocked/local/hidden.txt": body != "",
				"blocked/other/hidden.js":  true,
				"blocked/hidden.js":        true,
			})
			exts := lsp.ScanExtensions(root, func(path string) bool { return im.IsIgnored(path, root) })
			if _, ok := exts[".go"]; !ok {
				t.Error("LSP scan failed to reach the local fyc override")
			}
			if _, ok := exts[".js"]; ok {
				t.Error("LSP scan included git-ignored files outside the override")
			}
		})
	}
}

func assertIgnoreVisibility(t *testing.T, root string, im *IgnoreManager, ignored map[string]bool) {
	t.Helper()
	matches, err := GrepRun(GrepOptions{Root: root, Pattern: "needle", IgnoreManager: im})
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool)
	for _, match := range matches {
		found[filepath.ToSlash(match.Path)] = true
	}
	for name, want := range ignored {
		if got := im.IsIgnored(filepath.Join(root, name), root); got != want {
			t.Errorf("IsIgnored(%q) = %v, want %v", name, got, want)
		}
		_, err := validatePathInRoot(name, root, im)
		if (err != nil) != want {
			t.Errorf("validatePathInRoot(%q) = %v, want blocked=%v", name, err, want)
		}
		if found[name] == want {
			t.Errorf("grep visibility of %q = %v, want %v", name, found[name], !want)
		}
	}
}

func TestIgnoreManagerFYCRequiresFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".fycignore"), 0755); err != nil {
		t.Fatal(err)
	}
	writeToolTestFile(t, root, ".gitignore", "hidden.txt\n")
	im := LoadIgnoreFiles(root)
	if !im.IsIgnored(filepath.Join(root, "hidden.txt"), root) {
		t.Fatal("a directory named .fycignore must not replace .gitignore")
	}
}

// A rule must protect a file whichever spelling the caller uses: the root the
// manager was loaded with and the one its symlinks resolve to are the same
// directory (macOS /var → /private/var, Windows 8.3 names).
func TestIgnoreManagerMatchesBothSpellingsOfARoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	writeToolTestFile(t, real, ".gitignore", "*.log\n")
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	// The two spellings of the root: the one the manager was loaded with and the
	// one its symlinks resolve to (which is not `real` when TMPDIR is a link too).
	resolved, err := store.ResolvePath(link)
	if err != nil {
		t.Fatal(err)
	}

	im := LoadIgnoreFiles(link)
	for _, root := range []string{link, resolved} {
		if !im.IsIgnored(filepath.Join(root, "debug.log"), root) {
			t.Errorf("%s: the rule did not match under this spelling", root)
		}
		if im.IsIgnored(filepath.Join(root, "main.go"), root) {
			t.Errorf("%s: main.go wrongly flagged as ignored", root)
		}
		if !im.IsIgnored(filepath.Join(root, ".git", "config"), root) {
			t.Errorf("%s: .git is reachable under this spelling", root)
		}
	}
}
