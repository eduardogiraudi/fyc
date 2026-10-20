package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepToolSingleFilePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"slash.go", "other.go", "other.txt", "sub/slash.go", "sub/other.go", "sub/nested/slash.go", "slash[1].go"} {
		writeToolTestFile(t, root, name, "needle\n")
	}

	tests := []struct {
		name    string
		path    string
		include string
		exclude string
		want    string
	}{
		{name: "relative", path: "slash.go", want: "slash.go:1: needle"},
		{name: "absolute", path: filepath.Join(root, "slash.go"), want: "slash.go:1: needle"},
		{name: "nested", path: "sub/slash.go", want: "slash.go:1: needle"},
		{name: "normalized", path: "./sub/../slash.go", want: "slash.go:1: needle"},
		{name: "matching glob", path: "slash.go", include: "*.go", want: "slash.go:1: needle"},
		{name: "matching brace glob", path: "slash.go", include: "*.{go,txt}", want: "slash.go:1: needle"},
		{name: "nonmatching glob", path: "slash.go", include: "*.txt", want: "no matches found"},
		{name: "different file glob", path: "slash.go", include: "other.go", want: "no matches found"},
		{name: "excluded", path: "slash.go", include: "*.go", exclude: "slash.go", want: "no matches found"},
		{name: "other excluded", path: "slash.go", exclude: "other.go", want: "slash.go:1: needle"},
		{name: "literal glob characters", path: "slash[1].go", want: "slash[1].go:1: needle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executeGrepTool(map[string]any{
				"pattern": "needle", "path": tt.path,
				"include_glob": tt.include, "exclude_glob": tt.exclude,
			}, root, nil)
			if got != tt.want {
				t.Fatalf("grep = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGrepToolDirectoryPathRemainsRecursive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	writeToolTestFile(t, root, "outside.go", "needle\n")
	writeToolTestFile(t, root, "sub/slash.go", "needle\n")
	writeToolTestFile(t, root, "sub/nested/other.go", "needle\n")
	writeToolTestFile(t, root, "sub/other.txt", "needle\n")

	out := executeGrepTool(map[string]any{"pattern": "needle", "path": "sub", "include_glob": "*.go"}, root, nil)
	if strings.Count(out, "needle") != 2 || !strings.Contains(out, "slash.go:1: needle") ||
		!strings.Contains(out, filepath.Join("nested", "other.go")+":1: needle") {
		t.Fatalf("directory grep = %q", out)
	}
}

func TestGrepRunSingleFileOptions(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "slash.go", "Needle\nneedle\n")
	writeToolTestFile(t, root, "binary.go", "needle\x00binary\n")
	writeToolTestFile(t, root, ".gitignore", "ignored.go\n")
	writeToolTestFile(t, root, "ignored.go", "needle\n")

	tests := []struct {
		name string
		opts GrepOptions
		want int
	}{
		{name: "default", opts: GrepOptions{File: "slash.go"}, want: 1},
		{name: "case insensitive", opts: GrepOptions{File: "slash.go", CaseInsensitive: true}, want: 2},
		{name: "size limit", opts: GrepOptions{File: "slash.go", MaxFileSize: 1}},
		{name: "large file scanner", opts: GrepOptions{File: "slash.go", MmapThreshold: 1}, want: 1},
		{name: "binary skipped", opts: GrepOptions{File: "binary.go"}},
		{name: "binary included", opts: GrepOptions{File: "binary.go", IncludeBinary: true}, want: 1},
		{name: "ignored", opts: GrepOptions{File: "ignored.go"}},
		{name: "ignore file", opts: GrepOptions{File: ".gitignore"}},
		{name: "preloaded ignores", opts: GrepOptions{File: "ignored.go", IgnoreManager: LoadIgnoreFiles(root)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.Root = root
			tt.opts.Pattern = "needle"
			matches, err := GrepRun(tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != tt.want {
				t.Fatalf("matches = %v, want %d", matches, tt.want)
			}
			for _, m := range matches {
				if m.Path != tt.opts.File {
					t.Fatalf("matched %q, want only %q", m.Path, tt.opts.File)
				}
			}
		})
	}
}

func TestGrepRunSingleFileOnMatch(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "slash.go", "needle one\nneedle two\n")
	var seen []GrepMatch
	matches, err := GrepRun(GrepOptions{
		Root: root, File: "slash.go", Pattern: "needle",
		OnMatch: func(m GrepMatch) { seen = append(seen, m) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != nil || len(seen) != 2 || seen[0].Line != 1 || seen[1].Line != 2 {
		t.Fatalf("matches = %v, callback = %v", matches, seen)
	}
}

func TestGrepSingleFileErrorsDoNotSearchParent(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "other.go", "needle\n")
	for _, file := range []string{"missing.go", "../outside.go"} {
		t.Run(file, func(t *testing.T) {
			matches, err := GrepRun(GrepOptions{Root: root, File: file, Pattern: "needle"})
			if err == nil || len(matches) != 0 {
				t.Fatalf("matches = %v, err = %v; want an error without matches", matches, err)
			}
		})
	}
	out := executeGrepTool(map[string]any{"path": "missing.go", "pattern": "needle"}, root, nil)
	if !strings.HasPrefix(out, "grep error:") {
		t.Fatalf("missing file grep = %q", out)
	}
}
