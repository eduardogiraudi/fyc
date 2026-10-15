package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreManager_BlocksIgnoreFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .gitignore with rules
	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("secrets.txt\nnode_modules/\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".fycignore"), []byte("*.local\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "secrets.txt"), []byte("secret"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "config.local"), []byte("local config"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules/pkg"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "node_modules/pkg/index.js"), []byte("const x = 1\n"), 0644)

	// Create .gitignore in a subdirectory
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir/.gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir/app.log"), []byte("error"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir/main.go"), []byte("package main\n"), 0644)

	im := LoadIgnoreFiles(tmpDir)

	tests := []struct {
		path        string
		wantIgnored bool
		desc        string
	}{
		{filepath.Join(tmpDir, ".gitignore"), true, "root .gitignore — blocked"},
		{filepath.Join(tmpDir, ".fycignore"), true, "root .fycignore — blocked"},
		{filepath.Join(tmpDir, "subdir/.gitignore"), true, "subdir .gitignore — blocked"},
		{filepath.Join(tmpDir, ".dockerignore"), true, ".dockerignore does not exist but basename is blocked"},
		{filepath.Join(tmpDir, "secrets.txt"), true, "secrets.txt matched by .gitignore"},
		{filepath.Join(tmpDir, "node_modules/pkg/index.js"), true, "node_modules matched by .gitignore"},
		{filepath.Join(tmpDir, "config.local"), true, "config.local matched by .fycignore"},
		{filepath.Join(tmpDir, "subdir/app.log"), true, "app.log matched by subdir/.gitignore"},
		{filepath.Join(tmpDir, "main.go"), false, "main.go NOT blocked"},
		{filepath.Join(tmpDir, "subdir/main.go"), false, "subdir/main.go NOT blocked"},
	}

	for _, tt := range tests {
		got := im.IsIgnored(tt.path, tmpDir)
		if got != tt.wantIgnored {
			t.Errorf("%s: IsIgnored(%s) = %v, want %v", tt.desc, tt.path, got, tt.wantIgnored)
		}
	}
}

func TestValidatePathInRoot_BlocksIgnoreFiles(t *testing.T) {
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.secret\n"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir/.gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "data.secret"), []byte("secret\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir/trace.log"), []byte("log\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir/main.go"), []byte("package main\n"), 0644)

	im := LoadIgnoreFiles(tmpDir)

	tests := []struct {
		relPath string
		wantErr bool
		desc    string
	}{
		{".gitignore", true, ".gitignore blocked by validatePathInRoot"},
		{".fycignore", true, ".fycignore blocked (basename check)"},
		{".dockerignore", true, ".dockerignore blocked (basename check)"},
		{"subdir/.gitignore", true, "subdir/.gitignore blocked"},
		{"data.secret", true, "data.secret matched by *.secret"},
		{"subdir/trace.log", true, "trace.log matched by subdir/.gitignore"},
		{"main.go", false, "main.go allowed"},
		{"subdir/main.go", false, "subdir/main.go allowed"},
	}

	for _, tt := range tests {
		_, err := validatePathInRoot(tt.relPath, tmpDir, im)
		gotErr := err != nil
		if gotErr != tt.wantErr {
			t.Errorf("%s: validatePathInRoot(%q) err=%v, wantErr=%v", tt.desc, tt.relPath, err, tt.wantErr)
		}
	}
}

func TestDeletePathInRoot_BlocksIgnoredFiles(t *testing.T) {
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("secrets.txt\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "secrets.txt"), []byte("secret\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644)

	root, err := getRoot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	im := LoadIgnoreFiles(tmpDir)

	// .gitignore must be blocked by validatePathInRoot
	_, err = validatePathInRoot(".gitignore", tmpDir, im)
	if err == nil {
		t.Error("validatePathInRoot should block .gitignore")
	}

	// secrets.txt must be blocked (matched by .gitignore)
	_, err = validatePathInRoot("secrets.txt", tmpDir, im)
	if err == nil {
		t.Error("validatePathInRoot should block secrets.txt")
	}

	// main.go must be allowed
	rel, err := validatePathInRoot("main.go", tmpDir, im)
	if err != nil {
		t.Fatalf("main.go should be allowed: %v", err)
	}

	// deletePathInRoot on main.go must work
	err = deletePathInRoot(root, rel)
	if err != nil {
		t.Errorf("deletePathInRoot on main.go failed: %v", err)
	}

	// Verify that main.go has been deleted
	if _, err := os.Stat(filepath.Join(tmpDir, "main.go")); !os.IsNotExist(err) {
		t.Error("main.go should have been deleted")
	}
}

func TestLs_FiltersIgnoredFiles(t *testing.T) {
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("ignored.txt\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "ignored.txt"), []byte("ignored\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("visible\n"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir/visible.go"), []byte("package main\n"), 0644)

	im := LoadIgnoreFiles(tmpDir)
	root, err := getRoot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Simulate ls on the root
	dirFile, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer dirFile.Close()

	entries, err := dirFile.ReadDir(0)
	if err != nil {
		t.Fatal(err)
	}

	var visible []string
	for _, e := range entries {
		childAbs := filepath.Join(tmpDir, e.Name())
		if im.IsIgnored(childAbs, tmpDir) {
			continue
		}
		visible = append(visible, e.Name())
	}

	// .gitignore, ignored.txt should not appear, visible.txt should
	for _, name := range visible {
		if name == ".gitignore" || name == "ignored.txt" {
			t.Errorf("ls should not show %s (ignored)", name)
		}
	}

	foundVisible := false
	for _, name := range visible {
		if name == "visible.txt" {
			foundVisible = true
			break
		}
	}
	if !foundVisible {
		t.Error("ls should show visible.txt")
	}
}

func TestRead_BlocksIgnoredFiles(t *testing.T) {
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("secret.txt\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "secret.txt"), []byte("topsecret\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "public.txt"), []byte("hello\n"), 0644)

	im := LoadIgnoreFiles(tmpDir)
	root, err := getRoot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Reading .gitignore blocked
	_, err = validatePathInRoot(".gitignore", tmpDir, im)
	if err == nil {
		t.Error("read on .gitignore should be blocked")
	}

	// Reading secret.txt blocked
	_, err = validatePathInRoot("secret.txt", tmpDir, im)
	if err == nil {
		t.Error("read on secret.txt should be blocked")
	}

	// Reading public.txt allowed
	rel, err := validatePathInRoot("public.txt", tmpDir, im)
	if err != nil {
		t.Fatalf("read on public.txt should be allowed: %v", err)
	}

	f, err := root.Open(rel)
	if err != nil {
		t.Fatalf("open public.txt failed: %v", err)
	}
	defer f.Close()

	data := make([]byte, 10)
	n, _ := f.Read(data)
	if string(data[:n]) != "hello\n" {
		t.Errorf("public.txt content = %q, want %q", string(data[:n]), "hello\n")
	}
}

func TestGrep_BlocksIgnoredFiles(t *testing.T) {
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.secret\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("// visible\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "data.secret"), []byte("// hidden\n"), 0644)

	im := LoadIgnoreFiles(tmpDir)

	matches, err := GrepRun(GrepOptions{
		Pattern:       "//",
		Root:          tmpDir,
		IgnoreManager: im,
	})
	if err != nil {
		t.Fatalf("GrepRun failed: %v", err)
	}

	for _, m := range matches {
		if m.Path == "data.secret" {
			t.Error("grep should not search in data.secret (ignored)")
		}
		if m.Path == ".gitignore" {
			t.Error("grep should not search in .gitignore (ignored)")
		}
	}

	found := false
	for _, m := range matches {
		if m.Path == "main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Error("grep should find matches in main.go")
	}
}

func TestGrepExcludeGlobMatchesNameInSubdirs(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "sub"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "root.go"), []byte("needle\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "sub", "keep.go"), []byte("needle\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "sub", "drop_test.go"), []byte("needle\n"), 0644)

	matches, err := GrepRun(GrepOptions{
		Pattern:     "needle",
		Root:        tmpDir,
		ExcludeGlob: "*_test.go",
	})
	if err != nil {
		t.Fatalf("GrepRun: %v", err)
	}
	got := map[string]bool{}
	for _, m := range matches {
		got[filepath.ToSlash(m.Path)] = true
	}
	if got["sub/drop_test.go"] {
		t.Errorf("sub/drop_test.go should be excluded by *_test.go (matches: %v)", got)
	}
	if !got["sub/keep.go"] {
		t.Errorf("sub/keep.go should be searched (matches: %v)", got)
	}
	if !got["root.go"] {
		t.Errorf("root.go should be searched (matches: %v)", got)
	}
}

func TestGrepSkipsBinaryFilesByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "text.go"), []byte("needle text\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "blob.bin"), []byte("needle\x00binary"), 0644)

	matches, err := GrepRun(GrepOptions{Pattern: "needle", Root: tmpDir})
	if err != nil {
		t.Fatalf("GrepRun: %v", err)
	}
	for _, m := range matches {
		if m.Path == "blob.bin" {
			t.Error("blob.bin (binary) must be skipped by default")
		}
	}

	matches, err = GrepRun(GrepOptions{Pattern: "needle", Root: tmpDir, IncludeBinary: true})
	if err != nil {
		t.Fatalf("GrepRun (IncludeBinary): %v", err)
	}
	found := false
	for _, m := range matches {
		if m.Path == "blob.bin" {
			found = true
		}
	}
	if !found {
		t.Error("blob.bin must be searched when IncludeBinary is true")
	}
}
