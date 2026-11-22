//go:build darwin

package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real thing: a sandboxed command must be able to write a workspace reached
// through a symlink, which is what $TMPDIR always is on macOS (/var/folders/… →
// /private/var/…). With the unresolved spelling in the profile sandbox-exec
// denies the write and the command exits non-zero, so this fails loudly instead
// of leaving the journal test to guess why nothing changed.
func TestDarwinSandboxWritesThroughASymlinkedWorkspace(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is not available")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := buildSandboxCmd(ctx, sandboxConfig{
		RestrictedRoot: link,
		AllowedRW:      []string{link},
		CmdStr:         "printf hello > out.txt && cat out.txt",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer res.Cleanup()

	proc, err := startSandboxed(res)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := proc.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := proc.Wait()
	_ = proc.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	if waitErr != nil {
		t.Fatalf("the sandboxed command failed: %v\n%s", waitErr, out.String())
	}
	data, err := os.ReadFile(filepath.Join(real, "out.txt"))
	if err != nil {
		t.Fatalf("the command could not write its workspace: %v\n%s", err, out.String())
	}
	if strings.TrimSpace(string(data)) != "hello" {
		t.Fatalf("out.txt = %q, want hello\n%s", data, out.String())
	}
}
