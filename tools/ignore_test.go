package tools

import (
	"fuckyourcli/lsp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestIgnoreManagerDirectoryRules(t *testing.T) {
	for _, name := range []string{".gitignore", ".dockerignore", ".fycignore"} {
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
