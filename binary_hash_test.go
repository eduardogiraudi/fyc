package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestComputeFileSHA(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", []byte("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := computeFileSHA(path)
			if err != nil || got != tc.want {
				t.Fatalf("computeFileSHA() = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}

func TestComputeFileSHAErrors(t *testing.T) {
	dir := t.TempDir()
	if got, err := computeFileSHA(filepath.Join(dir, "missing")); got != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing binary = %q, %v; want empty hash and not-exist error", got, err)
	}
	if got, err := computeFileSHA(dir); got != "" || err == nil {
		t.Fatalf("directory = %q, %v; want empty hash and error", got, err)
	}
}

func TestComputeBinarySHA(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	got, err := computeBinarySHA()
	if err != nil || got != want {
		t.Fatalf("computeBinarySHA() = %q, %v; want %q, nil", got, err, want)
	}
	if short := computeShortSHA(); short != want[:16] {
		t.Fatalf("computeShortSHA() = %q, want %q", short, want[:16])
	}
}

func TestHashCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := computeBinarySHA()
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(t.TempDir(), "config")
	t.Setenv("FYC_CONFIG_DIR", configDir)
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestHashCommandProcess$")
	cmd.Env = append(os.Environ(), "FYC_HASH_COMMAND_HELPER=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--hash failed: %v; stderr: %s", err, stderr.String())
	}
	if got := stdout.String(); got != want+"\n" {
		t.Fatalf("stdout = %q, want %q", got, want+"\n")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	if _, err := os.Stat(configDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--hash touched configuration: stat error = %v", err)
	}
}

func TestHashCommandProcess(t *testing.T) {
	if os.Getenv("FYC_HASH_COMMAND_HELPER") != "1" {
		return
	}
	os.Args = []string{os.Args[0], "--hash"}
	main()
	os.Exit(0)
}
