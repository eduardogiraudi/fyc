//go:build windows

package tools

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSandboxSpecRoundTrip(t *testing.T) {
	want := sandboxSpec{
		Cwd:            `C:\work\repo`,
		AllowedRW:      []string{`C:\work\repo`, `C:\Users\u\AppData\Local\go-build`},
		AllowedRO:      []string{`C:\Windows`, `C:\Program Files`},
		DenyEnv:        []string{"GITHUB_TOKEN"},
		SecretEnvPairs: []string{"FOO=bar"},
		CmdStr:         `echo "hello world" & dir /b`,
		Network:        true,
	}

	enc, err := marshalSandboxSpec(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The spec travels through an env var, so it must not need quoting.
	if strings.ContainsAny(enc, `"'\\ `) {
		t.Fatalf("encoded spec must be env-safe, got %q", enc)
	}

	got, err := unmarshalSandboxSpec(enc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cwd != want.Cwd || got.CmdStr != want.CmdStr || got.Network != want.Network {
		t.Fatalf("scalar mismatch: got %+v want %+v", got, want)
	}
	if strings.Join(got.AllowedRW, "|") != strings.Join(want.AllowedRW, "|") {
		t.Fatalf("AllowedRW mismatch: %v vs %v", got.AllowedRW, want.AllowedRW)
	}
	if strings.Join(got.SecretEnvPairs, "|") != strings.Join(want.SecretEnvPairs, "|") {
		t.Fatalf("SecretEnvPairs mismatch: %v vs %v", got.SecretEnvPairs, want.SecretEnvPairs)
	}
}

func TestUnmarshalSandboxSpecRejectsGarbage(t *testing.T) {
	if _, err := unmarshalSandboxSpec("!!!not-base64!!!"); err == nil {
		t.Fatal("invalid base64 must fail")
	}
	notJSON := base64.StdEncoding.EncodeToString([]byte("not json"))
	if _, err := unmarshalSandboxSpec(notJSON); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestNormalizeEnvKeyFoldsCase(t *testing.T) {
	if normalizeEnvKey("Path") != normalizeEnvKey("PATH") {
		t.Fatal("Path and PATH must fold to the same key on Windows")
	}
}

// Windows env names are case-insensitive, so a secret must not leak just
// because it is spelled differently from the deny list.
func TestFilteredEnvIsCaseInsensitiveOnWindows(t *testing.T) {
	t.Setenv("Aws_Secret_Access_Key", "leak")
	out := filteredEnv([]string{"AWS_SECRET_ACCESS_KEY"})
	for _, e := range out {
		if strings.HasPrefix(strings.ToUpper(e), "AWS_SECRET_ACCESS_KEY=") {
			t.Fatalf("mixed-case secret must be filtered, got %q", e)
		}
	}

	kept := false
	for _, e := range filteredEnv([]string{"AWS_SECRET_ACCESS_KEY"}) {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			kept = true
		}
	}
	if !kept {
		t.Fatal("PATH must survive filtering")
	}
}
