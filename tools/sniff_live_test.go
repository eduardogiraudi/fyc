//go:build linux

package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"fuckyourcli/config"
)

// TestSniffLivePersistentProcess is an opt-in integration test (set
// FYC_SNIFF_LIVE=1) that starts real persistent sandboxed processes and checks:
//
//   - a persistent process survives the sh tool call and keeps writing;
//   - sniff reads its tail (window + byte cap) and reports "running";
//   - kill stops it and removes it from the map;
//   - a process that ends on its own is reported as "exited".
func TestSniffLivePersistentProcess(t *testing.T) {
	if os.Getenv("FYC_SNIFF_LIVE") != "1" {
		t.Skip("set FYC_SNIFF_LIVE=1 to run the live persistent-process/sniff test")
	}

	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	prevNoAsk := config.Get().NoAsk
	config.SetNoAsk(true)
	defer config.SetNoAsk(prevNoAsk)

	// Shorten the detach window: the test drives a real PTY/bwrap process.
	prevGrace := shBackgroundGrace
	shBackgroundGrace = 1 * time.Second
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

	// Process 1: survives the tool call (the old deferred cancel() killed it) and
	// keeps writing afterwards.
	cmd1 := "echo boot; sleep 2; echo after-return; sleep 30"
	pid1 := start(cmd1)
	defer cleanup(pid1)

	time.Sleep(1500 * time.Millisecond)
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

	// Process 2: exits on its own after registration, so the waiter goroutine
	// must flip the exit state.
	cmd2 := "sleep 3; echo bye"
	pid2 := start(cmd2)
	defer cleanup(pid2)

	deadline := time.Now().Add(4 * time.Second)
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
func sniffBody(s string) string {
	if i := strings.Index(s, "---\n"); i >= 0 {
		return s[i+len("---\n"):]
	}
	return s
}

func TestShLiveOneShot(t *testing.T) {
	if os.Getenv("FYC_SNIFF_LIVE") != "1" {
		t.Skip("set FYC_SNIFF_LIVE=1 to run real sandboxed one-shot commands")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	previous := config.Get().NoAsk
	config.SetNoAsk(true)
	defer config.SetNoAsk(previous)
	for _, tc := range []struct {
		name, command, status, output string
		timeout                       float64
		cancelAfter                   time.Duration
	}{
		{"beyond ten seconds", "echo boot; sleep 12; echo completed", "exit code 0", "completed", 20, 0},
		{"quick success", "echo quick", "exit code 0", "quick", 120, 0},
		{"failure with output", "echo failure-details; exit 7", "command failed: exit code 7", "failure-details", 120, 0},
		{"timeout", "echo partial; sleep 30; echo must-not-complete", "command timed out", "partial", 1, 0},
		{"cancellation", "echo partial; sleep 30; echo must-not-complete", "command cancelled", "partial", 120, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelAfter > 0 {
				timer := time.AfterFunc(tc.cancelAfter, cancel)
				defer timer.Stop()
			}
			start := time.Now()
			out := executeShTool(ctx, map[string]any{"command": tc.command, "persistence": false, "timeout_seconds": tc.timeout}, root, nil)
			t.Logf("sh (%s): %s", time.Since(start), out)
			if !strings.Contains(out, tc.status) || !strings.Contains(out, tc.output) {
				t.Fatal(out)
			}
			if strings.Contains(out, "PID") || strings.Contains(out, "background") || strings.Contains(out, "must-not-complete") {
				t.Fatalf("one-shot result leaked background state or continued after interruption: %s", out)
			}
		})
	}
}
