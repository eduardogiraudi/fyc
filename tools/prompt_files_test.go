package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPromptFileSuggestions(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"test.py": "x", "ignore.py": "secret", ".fycignore": "ignore.py\n", "src/nested.py": "y"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ig := LoadIgnoreFiles(root)
	for _, tc := range []struct {
		query string
		want  []string
	}{{"", []string{"src/", "test.py"}}, {"TEST", []string{"test.py"}}, {"src/ne", []string{"src/nested.py"}}} {
		got, err := PromptFileSuggestions(tc.query, root, ig)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("query %q: %v, %v", tc.query, got, err)
		}
	}
	for _, query := range []string{"../", strings.Repeat("x", 1025)} {
		if _, err := PromptFileSuggestions(query, root, ig); err == nil {
			t.Errorf("accepted %q", query)
		}
	}
}

func TestPromptFilesRejectSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PromptFile("link.txt", root, LoadIgnoreFiles(root)); err == nil {
		t.Fatal("symlink attachment accepted")
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := PromptFileSuggestions("escape/", root, nil); err == nil {
		t.Fatal("escaped workspace")
	}
}
