//go:build windows

package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"fuckyourcli/config"
)

// TestMain intercepts the self-re-exec used by the Windows sandbox. The parent
// launches this very test binary with FYC_SANDBOX_SPEC set (see buildSandboxCmd),
// and the child must run the requested command instead of the test suite.
// Without this hook the re-exec would just re-run the tests and never produce
// the command output, which is why the Windows plumbing had no live coverage.
func TestMain(m *testing.M) {
	MaybeRunAsSandboxChild() // never returns on the sandbox child
	os.Exit(m.Run())
}

// TestSniffLivePersistentProcess is the Windows counterpart of the Linux live
// test (sniff_live_test.go): it drives the real plumbing end to end — the
// __sandbox-child self-re-exec, cmd.exe, the combined pipe, the job object
// (tree kill via KILL_ON_JOB_CLOSE) and the persistent-process registry.
//
// Opt-in like the Linux test: set FYC_SNIFF_LIVE=1 to run it.
func TestSniffLivePersistentProcess(t *testing.T) {
	if os.Getenv("FYC_SNIFF_LIVE") != "1" {
		t.Skip("set FYC_SNIFF_LIVE=1 to run the live persistent-process/sniff test")
	}
	if !defaultWindowsSandbox {
		t.Skip("defaultWindowsSandbox is off: the live test exercises the sandbox helper")
	}

	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	prevNoAsk := config.Get().NoAsk
	config.SetNoAsk(true)
	defer config.SetNoAsk(prevNoAsk)

	// Shorten the detach window: the test drives a real helper + cmd.exe tree.
	prevGrace := shBackgroundGrace
	shBackgroundGrace = 3 * time.Second
	defer func() { shBackgroundGrace = prevGrace }()

	ctx := context.Background()
	ignore := &IgnoreManager{}

	start := func(cmd string) int {
		t.Helper()
		out := executeShTool(ctx, map[string]any{"command": cmd, "persistence": true}, root, ignore)
		t.Logf("sh -> %s", strings.ReplaceAll(out, "\n", " | "))
		if !strings.Contains(out, "persistent process started") {
			t.Fatalf("not registered as persistent: %s", out)
		}
		persistentMu.Lock()
		defer persistentMu.Unlock()
		for pid, pp := range persistentProcesses {
			if pp.Command == cmd {
				return pid
			}
		}
		t.Fatalf("no persistent process registered for %q", cmd)
		return 0
	}
	cleanup := func(pid int) {
		persistentMu.Lock()
		pp, ok := persistentProcesses[pid]
		if ok {
			delete(persistentProcesses, pid)
		}
		persistentMu.Unlock()
		if ok {
			pp.Proc.Close()
			pp.Cancel()
		}
	}

	// Sleep comes from ping: cmd.exe has no sleep builtin, and PowerShell does
	// not start inside the restricted-token sandbox. ping
	// plus a NUL redirect both work under this engine — unlike the removed
	// AppContainer, where neither NUL nor raw ICMP sockets were reachable.
	sleep := func(secs int) string {
		return fmt.Sprintf(`ping -n %d 127.0.0.1 >nul`, secs+1)
	}
	cmd1 := "echo boot & " + sleep(1) + " & echo after-return & " + sleep(30)
	pid1 := start(cmd1)
	defer cleanup(pid1)

	time.Sleep(5 * time.Second)
	log1 := executeSniffTool(map[string]any{"pid": float64(pid1), "last_n": float64(50)})
	t.Logf("sniff(pid1):\n%s", log1)
	if b := sniffBody(log1); !strings.Contains(b, "boot") || !strings.Contains(b, "after-return") {
		t.Fatalf("process did not survive / log incomplete:\n%s", log1)
	}
	if !strings.Contains(log1, "running") {
		t.Fatalf("process should still be running:\n%s", log1)
	}

	tail := executeSniffTool(map[string]any{"pid": float64(pid1), "last_n": float64(1)})
	if b := sniffBody(tail); !strings.Contains(b, "after-return") || strings.Contains(b, "boot") {
		t.Fatalf("last_n=1 window wrong:\n%s", tail)
	}

	if got := executeKillTool(map[string]any{"pid": float64(pid1)}); !strings.Contains(got, "stopped") {
		t.Fatalf("kill: %q", got)
	}
	if got := executeSniffTool(map[string]any{"pid": float64(pid1)}); !strings.Contains(got, "no persistent process found") {
		t.Fatalf("sniff after kill: %q", got)
	}

	// Process 2: outlives the detach window but exits on its own, so the waiter
	// goroutine must flip the exit state.
	cmd2 := sleep(5) + " & echo bye"
	pid2 := start(cmd2)
	defer cleanup(pid2)

	deadline := time.Now().Add(20 * time.Second)
	var log2 string
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		log2 = executeSniffTool(map[string]any{"pid": float64(pid2)})
		if strings.Contains(log2, "exited") && strings.Contains(log2, "bye") {
			break
		}
	}
	t.Logf("sniff(pid2):\n%s", log2)
	if !strings.Contains(log2, "exited") || !strings.Contains(log2, "bye") {
		t.Fatalf("exit state not reported:\n%s", log2)
	}
}

// sniffBody returns the payload after the first header separator, so assertions
// do not match words that only appear in the command shown in the header.
// (Windows copy of the Linux-only helper in sniff_live_test.go.)
func sniffBody(s string) string {
	if i := strings.Index(s, "---\n"); i >= 0 {
		return s[i+len("---\n"):]
	}
	return s
}
