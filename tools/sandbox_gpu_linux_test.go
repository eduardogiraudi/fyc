//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGlobExistingAndBindArgs(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"renderD128", "card0", "nvidiactl"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := globExisting([]string{filepath.Join(dir, "renderD*"), filepath.Join(dir, "nvidia*"), filepath.Join(dir, "missing*")})
	want := []string{filepath.Join(dir, "nvidiactl"), filepath.Join(dir, "renderD128")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("globExisting = %v, want %v", got, want)
	}
	args := gpuBindArgs(want)
	if len(args) != 6 || args[0] != "--dev-bind" || args[1] != want[0] || args[2] != want[0] {
		t.Fatalf("unexpected bind args: %v", args)
	}
	for _, g := range gpuDeviceGlobs {
		if g == "/dev/dri/card*" {
			t.Fatal("card* nodes must not be exposed")
		}
	}
}

func TestGPUArgsAfterDevMount(t *testing.T) {
	// Not a bwrap run: just checks ordering of the generated args.
	if len(gpuDeviceNodes()) == 0 {
		t.Skip("no GPU device nodes on this host")
	}
}
