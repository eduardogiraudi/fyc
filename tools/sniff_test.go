package tools

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// registerTestProcess installs a fake persistent process in the global map so
// sniff can be exercised without spawning a real PTY/sandbox.
func registerTestProcess(t *testing.T, pid int, out string) *persistentProcess {
	t.Helper()
	b := newTailBuffer(defaultTailBufferBytes)
	if out != "" {
		if _, err := b.Write([]byte(out)); err != nil {
			t.Fatalf("write seed output: %v", err)
		}
	}
	pp := &persistentProcess{
		Pid:     pid,
		Command: "test-server --port 8080",
		Buf:     b,
		Redact:  func(s string) string { return s },
	}
	persistentMu.Lock()
	persistentProcesses[pid] = pp
	persistentMu.Unlock()
	t.Cleanup(func() {
		persistentMu.Lock()
		delete(persistentProcesses, pid)
		persistentMu.Unlock()
	})
	return pp
}

func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestSniffUnknownPid(t *testing.T) {
	got := executeSniffTool(map[string]any{"pid": float64(987654)})
	if !strings.Contains(got, "no persistent process found") {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestSniffMissingPid(t *testing.T) {
	got := executeSniffTool(map[string]any{})
	if !strings.Contains(got, "pid argument required") {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestSniffDefaultsToConservativeTail(t *testing.T) {
	registerTestProcess(t, 700001, numberedLines(200))
	got := executeSniffTool(map[string]any{"pid": float64(700001)})
	if !strings.Contains(got, "line 200") {
		t.Fatalf("tail must contain the last line:\n%s", got)
	}
	if strings.Contains(got, "line 150\n") {
		t.Fatalf("default window must drop older lines:\n%s", got)
	}
	if !strings.Contains(got, "50 shown of 200") {
		t.Fatalf("header must report the default window:\n%s", got)
	}
}

func TestSniffLastN(t *testing.T) {
	registerTestProcess(t, 700002, numberedLines(100))
	got := executeSniffTool(map[string]any{"pid": float64(700002), "last_n": float64(3)})
	for _, want := range []string{"line 98", "line 99", "line 100"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line 97\n") {
		t.Fatalf("window larger than last_n:\n%s", got)
	}
}

func TestSniffLastNClamped(t *testing.T) {
	registerTestProcess(t, 700003, numberedLines(5))
	got := executeSniffTool(map[string]any{"pid": float64(700003), "last_n": float64(0)})
	if !strings.Contains(got, "1 shown of 5") {
		t.Fatalf("last_n=0 must clamp to 1:\n%s", got)
	}
}

func TestSniffPatternFiltersLines(t *testing.T) {
	out := "INFO boot\nERROR boom\nINFO ok\nERROR crash\n"
	registerTestProcess(t, 700004, out)
	got := executeSniffTool(map[string]any{"pid": float64(700004), "pattern": "ERROR"})
	if !strings.Contains(got, "ERROR boom") || !strings.Contains(got, "ERROR crash") {
		t.Fatalf("matching lines missing:\n%s", got)
	}
	if strings.Contains(got, "INFO") {
		t.Fatalf("non-matching lines must be filtered:\n%s", got)
	}
	if !strings.Contains(got, "(2 matched)") {
		t.Fatalf("match count missing:\n%s", got)
	}
}

func TestSniffInvalidPattern(t *testing.T) {
	registerTestProcess(t, 700005, "x\n")
	got := executeSniffTool(map[string]any{"pid": float64(700005), "pattern": "("})
	if !strings.Contains(got, "invalid pattern") {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestSniffMaxBytesCapsOutput(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "%04d %s\n", i, strings.Repeat("x", 100))
	}
	b.WriteString("LAST-MARKER")
	registerTestProcess(t, 700006, b.String())

	got := executeSniffTool(map[string]any{"pid": float64(700006), "last_n": float64(500), "max_bytes": float64(1024)})
	if !strings.Contains(got, "earlier output truncated") {
		t.Fatalf("byte cap marker missing:\n%s", got)
	}
	if !strings.Contains(got, "LAST-MARKER") {
		t.Fatalf("tail must survive the byte cap:\n%s", got)
	}
	if len(got) > 4096 {
		t.Fatalf("output too large after capping: %d bytes", len(got))
	}
}

func TestSniffRedactsSecrets(t *testing.T) {
	pp := registerTestProcess(t, 700007, "token=hunter2\nok\n")
	pp.Redact = func(s string) string { return strings.ReplaceAll(s, "hunter2", "[REDACTED]") }
	got := executeSniffTool(map[string]any{"pid": float64(700007)})
	if strings.Contains(got, "hunter2") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redaction not applied:\n%s", got)
	}
}

func TestSniffReportsExitState(t *testing.T) {
	pp := registerTestProcess(t, 700008, "bye\n")
	pp.setExited(errors.New("exit status 1"))
	got := executeSniffTool(map[string]any{"pid": float64(700008)})
	if !strings.Contains(got, "exited (exit status 1)") {
		t.Fatalf("exit state missing:\n%s", got)
	}
}

func TestSniffAnsiAndCRLFNormalized(t *testing.T) {
	registerTestProcess(t, 700009, "\x1b[31mred\x1b[0m\r\nplain\r\n")
	got := executeSniffTool(map[string]any{"pid": float64(700009)})
	if !strings.Contains(got, "red") || !strings.Contains(got, "plain") {
		t.Fatalf("content missing:\n%s", got)
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\r") {
		t.Fatalf("escape/CR must be stripped:\n%q", got)
	}
}

// TestSniffConcurrentAccess exercises the reader/writer/waiter interleaving so
// `go test -race` can catch unsynchronized access to the buffer or the exit
// state.
func TestSniffConcurrentAccess(t *testing.T) {
	pp := registerTestProcess(t, 700010, "boot\n")
	const writers = 2
	const iterations = 3000

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				pp.Buf.Write([]byte(fmt.Sprintf("w%d line %d\n", w, i)))
				pp.setExited(nil)
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = executeSniffTool(map[string]any{
					"pid":       float64(700010),
					"last_n":    float64(20),
					"max_bytes": float64(2048),
					"pattern":   "line",
				})
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}
