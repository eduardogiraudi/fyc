package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/plugins"
)

func TestAddOptions(t *testing.T) {
	for _, args := range [][]string{{"owner/repo", "--global", "--ref", "v1", "--subdir", "plugins/demo"}, {"--global", "--ref=v1", "--subdir=plugins/demo", "owner/repo"}} {
		o, err := parseAddOptions(args, &bytes.Buffer{})
		if err != nil || o.source != "owner/repo" || !o.global || o.ref != "v1" || o.subdir != "plugins/demo" {
			t.Fatalf("options: %+v %v", o, err)
		}
	}
	for _, args := range [][]string{{}, {"a", "b"}, {"a", "--global", "--project"}, {"a", "--ref"}, {"a", "--unknown"}} {
		if _, err := parseAddOptions(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}

func TestAddCancellationAndInstallation(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	workspace, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "skills/review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"$schema":"`+plugins.PluginSchema+`","name":"demo","version":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills/review/SKILL.md"), []byte("---\nname: review\ndescription: Review code\n---\nDo the review.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := addOptions{source: source}
	called := false
	if err := installAdd(context.Background(), o, workspace, &out, func() bool { called = true; return false }); err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(out.String(), "Installation cancelled") || len(plugins.Load(workspace)) != 0 {
		t.Fatalf("cancellation failed: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, ".fyc")); !os.IsNotExist(err) {
		t.Fatalf("cancelled installation wrote project files: %v", err)
	}
	o.yes = true
	if err := installAdd(context.Background(), o, workspace, &out, func() bool { t.Fatal("--yes prompted"); return false }); err != nil {
		t.Fatal(err)
	}
	if len(plugins.Load(workspace)) != 1 || !strings.Contains(out.String(), "demo--review") {
		t.Fatalf("installation failed: %s", out.String())
	}
}
