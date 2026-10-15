package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestUpdateCodexVersion(t *testing.T) {
	for _, tc := range []struct {
		name, initial, latest, want     string
		write, offline, failed, missing bool
		code                            int
	}{
		{"print only", "0.150.0\n", "0.161.0", "0.161.0", false, false, false, false, 0},
		{"write", "0.150.0\n", "0.161.0", "0.161.0", true, false, false, false, 0},
		{"offline network", "0.150.0\n", "", "0.150.0", true, false, true, false, 0},
		{"invalid response", "0.150.0\n", "dev", "0.150.0", true, false, false, false, 0},
		{"no downgrade", "0.161.0\n", "0.150.0", "0.161.0", true, false, false, false, 0},
		{"pinned", "0.150.0\n", "0.161.0", "0.150.0", true, true, false, false, 0},
		{"missing fallback", "", "0.161.0", "", true, false, false, true, 1},
		{"invalid fallback", "dev", "0.161.0", "", true, false, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "VERSION")
			if !tc.missing {
				if err := os.WriteFile(file, []byte(tc.initial), 0644); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-file", file}
			if tc.write {
				args = append(args, "-write")
			}
			if tc.offline {
				args = append(args, "-offline")
			}
			var out, errOut bytes.Buffer
			calls := 0
			code := run(args, &out, &errOut, func(context.Context) (string, error) {
				calls++
				if tc.failed {
					return "", errors.New("no internet")
				}
				return tc.latest, nil
			})
			if code != tc.code || strings.TrimSpace(out.String()) != tc.want {
				t.Fatalf("code %d, output %q, stderr %q", code, out.String(), errOut.String())
			}
			if tc.offline && calls != 0 {
				t.Fatal("offline mode made HTTP request")
			}
			if tc.code != 0 {
				if calls != 0 {
					t.Fatal("invalid fallback still fetched")
				}
				return
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			wantFile := tc.initial
			if tc.write && tc.want != strings.TrimSpace(tc.initial) {
				wantFile = tc.want + "\n"
			}
			if string(data) != wantFile {
				t.Fatalf("file changed unexpectedly: %q", data)
			}
			if tc.failed && !strings.Contains(errOut.String(), "Warning:") {
				t.Fatal("offline fallback not reported")
			}
		})
	}
}

func TestUpdateWriteFailureKeepsFallback(t *testing.T) {
	file := filepath.Join(t.TempDir(), "VERSION")
	if err := os.WriteFile(file, []byte("0.150.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"-file", file, "-write"}, &out, &errOut, func(context.Context) (string, error) {
		// Deterministically make the rename fail on every OS, without relying on chmod.
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(file, 0700); err != nil {
			t.Fatal(err)
		}
		return "0.161.0", nil
	})
	if code != 0 || strings.TrimSpace(out.String()) != "0.150.0" || !strings.Contains(errOut.String(), "Warning:") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	files, err := os.ReadDir(filepath.Dir(file))
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary file leaked: %v, %v", files, err)
	}
}

func TestReleaseVersionTemplate(t *testing.T) {
	data, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var source string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "codexversion.BuildVersion") {
			source = strings.TrimSuffix(strings.TrimPrefix(line, "- '"), "'")
		}
	}
	if source == "" {
		t.Fatal("release must inject the resolved Codex build version")
	}
	tmpl, err := template.New("ldflags").Option("missingkey=error").Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{{}, {"CODEX_VERSION": "0.161.0"}} {
		var out bytes.Buffer
		if err := tmpl.Execute(&out, struct {
			Version string
			Env     map[string]string
		}{"1.0.0", env}); err != nil {
			t.Fatal(err)
		}
		want := "-s -w -X main.version=1.0.0"
		if version := env["CODEX_VERSION"]; version != "" {
			want += " -X fuckyourcli/codexversion.BuildVersion=" + version
		}
		if out.String() != want {
			t.Fatalf("release flags = %q, want %q", out.String(), want)
		}
	}
}
