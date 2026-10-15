package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	log := write("log", "abcdef0123456789\x00feat(cli): add workflow\n\x00\n")
	history := write("history", "# Changelog\n\n## [1.0.0]\n- Initial release.\n")
	var out bytes.Buffer
	if err := run([]string{"draft", "1.2.3", log}, &out); err != nil {
		t.Fatal(err)
	}
	section := write("section", out.String())
	if err := run([]string{"validate", "1.2.3", section}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"assemble", "1.2.3", section, history}, &out); err != nil {
		t.Fatal(err)
	}
	assembled := write("assembled", out.String())
	out.Reset()
	if err := run([]string{"extract", "1.2.3", assembled}, &out); err != nil || !strings.Contains(out.String(), "cli: add workflow") || strings.Contains(out.String(), "Initial release") {
		t.Fatalf("out = %q, err = %v", out.String(), err)
	}
	if err := run([]string{"check-version", "1.2.3"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"unknown", "1.2.3"}, {"check-version", "v1.2.3"}, {"draft", "1.2.3"}, {"validate", "1.2.3", filepath.Join(dir, "missing")}, {"extract", "1.0.0", history}} {
		if run(args, &out) == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
