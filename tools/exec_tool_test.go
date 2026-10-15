package tools

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"fuckyourcli/types"
)

func TestGoFlagsWithBuildVCS(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		want     string
	}{
		{"empty adds the flag", "", "-buildvcs=false"},
		{"whitespace only", "   ", "-buildvcs=false"},
		{"appends to existing flags", "-mod=mod", "-mod=mod -buildvcs=false"},
		{"trims surrounding spaces", "  -mod=mod  ", "-mod=mod -buildvcs=false"},
		{"keeps explicit =false", "-buildvcs=false", "-buildvcs=false"},
		{"keeps explicit =true", "-buildvcs=true", "-buildvcs=true"},
		{"keeps bare flag", "-buildvcs", "-buildvcs"},
		{"keeps double-dash form", "--buildvcs=false", "--buildvcs=false"},
		{"keeps flag among others", "-v -buildvcs=true -mod=mod", "-v -buildvcs=true -mod=mod"},
		{"not confused by similar names", "-buildvcsz=1", "-buildvcsz=1 -buildvcs=false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := goFlagsWithBuildVCS(tc.existing); got != tc.want {
				t.Fatalf("goFlagsWithBuildVCS(%q) = %q, want %q", tc.existing, got, tc.want)
			}
		})
	}
}

func TestGoVersionProvenOld(t *testing.T) {
	cases := []struct {
		v            string
		major, minor int
		want         bool
	}{
		{"go1.17.13", 1, 18, true},
		{"1.17", 1, 18, true},
		{"go1.10", 1, 18, true},
		{"go1.18", 1, 18, false},
		{"go1.25.0", 1, 18, false},
		{"1.18", 1, 18, false},
		{"go2.0", 1, 18, false},
		// Unknown formats are never "proven old": the default is to inject.
		{"", 1, 18, false},
		{"go", 1, 18, false},
		{"go1", 1, 18, false},
		{"gox.y", 1, 18, false},
		// Tip builds report "devel go1.27-<hash> [date]".
		{"devel go1.27-abc123", 1, 18, false},
		{"devel go1.17-abc123", 1, 18, true},
	}
	for _, tc := range cases {
		if got := goVersionProvenOld(tc.v, tc.major, tc.minor); got != tc.want {
			t.Errorf("goVersionProvenOld(%q, %d, %d) = %v, want %v", tc.v, tc.major, tc.minor, got, tc.want)
		}
	}
}

func TestLeadingInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"18", 18, true},
		{"26-devel", 26, true},
		{"25rc1", 25, true},
		{"", 0, false},
		{"x1", 0, false},
	}
	for _, tc := range cases {
		got, ok := leadingInt(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("leadingInt(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestGoBinarySupportsBuildVCS runs against the real `go` in PATH: the module
// requires Go 1.25, so the detected binary must always support -buildvcs.
func TestGoBinarySupportsBuildVCS(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	if !goBinarySupportsBuildVCS() {
		p, _ := exec.LookPath("go")
		bi, _ := buildinfo.ReadFile(p)
		ver := "?"
		if bi != nil {
			ver = bi.GoVersion
		}
		t.Fatalf("goBinarySupportsBuildVCS() = false for %s (%s)", p, ver)
	}
}

func TestCommandPreview(t *testing.T) {
	if got := commandPreview("echo hi"); got != "echo hi" {
		t.Fatalf("single line: got %q", got)
	}
	got := commandPreview("line1\nline2\nline3\n")
	if !strings.HasPrefix(got, "line1") || !strings.Contains(got, "+2 more line") {
		t.Fatalf("multi-line preview = %q", got)
	}
	long := commandPreview("x" + strings.Repeat("y", 500))
	if r := []rune(long); len(r) != 121 || r[len(r)-1] != '…' {
		t.Fatalf("long preview not capped: len=%d", len(r))
	}
}

func TestExecApprovalDetails(t *testing.T) {
	got := execApprovalDetails(
		"bash -c 'x'\necho hi",
		true,
		"/ws",
		[]string{"/ws", "/tmp/rw"},
		[]string{"/ro"},
		[]string{"A", "B"},
		[]string{"SECRET"},
		shDefaultTimeout,
	)
	joined := strings.Join(types.DetailLinesText(got), "\n")
	for _, want := range []string{
		"Command:", "  bash -c 'x'", "  echo hi",
		"Persistence: true", "Workspace root: /ws",
		"Writable inside the sandbox (RW):", "  /tmp/rw",
		"Readable inside the sandbox (RO):", "  /ro",
		"Injected env (.env keys):", "  A", "  B",
		"Denied env keys:", "  SECRET",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in details:\n%s", want, joined)
		}
	}

	empty := strings.Join(types.DetailLinesText(execApprovalDetails("x", false, "/ws", nil, nil, nil, nil, shDefaultTimeout)), "\n")
	if !strings.Contains(empty, "Persistence: false") {
		t.Errorf("one-shot persistence not shown:\n%s", empty)
	}
	if n := strings.Count(empty, "(none)"); n != 4 {
		t.Errorf("expected 4 empty sections, got %d:\n%s", n, empty)
	}
}

// TestExecApprovalDetailsKinds guards the viewer contract: the command section
// is typed as commands (the TUI shell-highlights it) and the envelope lists as
// paths or env keys (the TUI colours them) instead of plain text.
func TestExecApprovalDetailsKinds(t *testing.T) {
	got := execApprovalDetails("echo hi", false, "/ws",
		[]string{"/ws"}, []string{"/ro"}, []string{"A"}, []string{"SECRET"}, shDefaultTimeout)

	kindOf := func(text string) (types.DetailLineKind, bool) {
		for _, l := range got {
			if l.Text == text {
				return l.Kind, true
			}
		}
		return types.DetailText, false
	}
	for _, tc := range []struct {
		text string
		want types.DetailLineKind
	}{
		{"Command:", types.DetailSection},
		{"  echo hi", types.DetailCommand},
		{"Persistence: false (one-shot, 2m0s timeout)", types.DetailText},
		{"Writable inside the sandbox (RW):", types.DetailSection},
		{"  /ro", types.DetailPath},
		{"  A", types.DetailEnvKey},
		{"  SECRET", types.DetailEnvKey},
	} {
		kind, ok := kindOf(tc.text)
		if !ok {
			t.Errorf("missing detail line %q", tc.text)
			continue
		}
		if kind != tc.want {
			t.Errorf("%q has kind %d, want %d", tc.text, kind, tc.want)
		}
	}
}

func TestShTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       map[string]any
		persistent bool
		want       time.Duration
		invalid    bool
	}{
		{"default", nil, false, 120 * time.Second, false},
		{"minimum", map[string]any{"timeout_seconds": float64(1)}, false, time.Second, false},
		{"maximum", map[string]any{"timeout_seconds": float64(300)}, false, 300 * time.Second, false},
		{"zero", map[string]any{"timeout_seconds": float64(0)}, false, 0, true},
		{"negative", map[string]any{"timeout_seconds": float64(-1)}, false, 0, true},
		{"too large", map[string]any{"timeout_seconds": float64(301)}, false, 0, true},
		{"fractional", map[string]any{"timeout_seconds": 1.5}, false, 0, true},
		{"string", map[string]any{"timeout_seconds": "120"}, false, 0, true},
		{"null", map[string]any{"timeout_seconds": nil}, false, 0, true},
		{"nan", map[string]any{"timeout_seconds": math.NaN()}, false, 0, true},
		{"infinity", map[string]any{"timeout_seconds": math.Inf(1)}, false, 0, true},
		{"persistent ignores timeout", map[string]any{"timeout_seconds": "ignored"}, true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shTimeout(tc.args, tc.persistent)
			if got != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("timeout = %s, err = %v", got, err)
			}
		})
	}
	out := executeShTool(context.Background(), map[string]any{"command": "must not run", "timeout_seconds": float64(301)}, "", nil)
	if !strings.HasPrefix(out, "error: timeout_seconds") {
		t.Fatalf("invalid timeout not rejected before execution: %s", out)
	}
	custom := strings.Join(types.DetailLinesText(execApprovalDetails("x", false, "/ws", nil, nil, nil, nil, 300*time.Second)), "\n")
	if !strings.Contains(custom, "5m0s timeout") {
		t.Fatalf("approval does not show actual timeout: %s", custom)
	}
}

type shTestExitError int

func (e shTestExitError) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e shTestExitError) ExitCode() int { return int(e) }

type shTestProcess struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	done chan struct{}
	once sync.Once
	err  error
}

func newShTestProcess() *shTestProcess {
	r, w := io.Pipe()
	return &shTestProcess{r: r, w: w, done: make(chan struct{})}
}
func (p *shTestProcess) Pid() int                   { return 987654 }
func (p *shTestProcess) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *shTestProcess) Wait() error                { <-p.done; return p.err }
func (p *shTestProcess) finish(err error) {
	p.once.Do(func() { p.err = err; p.w.Close(); close(p.done) })
}
func (p *shTestProcess) Close() error {
	p.finish(shTestExitError(-1))
	return p.r.Close()
}
func (p *shTestProcess) write(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(p.w, text); err != nil {
		t.Fatal(err)
	}
}

func startShMonitorTest(t *testing.T, persistent bool, timeout, grace time.Duration) (*shTestProcess, <-chan string, context.CancelFunc, context.Context, <-chan struct{}) {
	t.Helper()
	previous := shBackgroundGrace
	shBackgroundGrace = grace
	t.Cleanup(func() { shBackgroundGrace = previous })
	ctx, cancelTurn := context.WithCancel(context.Background())
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if persistent {
		cmdCtx, cancel = context.WithCancel(context.WithoutCancel(ctx))
	} else {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	proc := newShTestProcess()
	cleaned := make(chan struct{})
	out := make(chan string, 1)
	go func() {
		out <- monitorShProcess(ctx, cmdCtx, cancel, sandboxResult{Cleanup: func() { close(cleaned) }}, proc, "test-command", persistent, timeout, []string{"SECRET_VALUE"})
	}()
	t.Cleanup(func() {
		cancelTurn()
		persistentMu.Lock()
		_, registered := persistentProcesses[proc.Pid()]
		persistentMu.Unlock()
		if registered {
			executeKillTool(map[string]any{"pid": float64(proc.Pid())})
		}
		cancel()
		proc.Close()
	})
	return proc, out, cancelTurn, cmdCtx, cleaned
}

func shMonitorResult(t *testing.T, out <-chan string) string {
	t.Helper()
	select {
	case result := <-out:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("sh monitor did not return")
	}
	return ""
}

func TestShOneShotWaitsBeyondBackgroundGrace(t *testing.T) {
	proc, out, _, cmdCtx, cleaned := startShMonitorTest(t, false, time.Second, 10*time.Millisecond)
	proc.write(t, "boot\n")
	select {
	case result := <-out:
		t.Fatalf("one-shot detached prematurely: %s", result)
	case <-time.After(40 * time.Millisecond):
	}
	proc.write(t, "completed\n")
	proc.finish(nil)
	result := shMonitorResult(t, out)
	for _, want := range []string{"exit code 0", "boot", "completed"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q: %s", want, result)
		}
	}
	if strings.Contains(result, "PID") || strings.Contains(result, "background") {
		t.Fatal(result)
	}
	if cmdCtx.Err() == nil {
		t.Fatal("one-shot context not released")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("sandbox not cleaned up")
	}
}

func TestShEarlyCompletion(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			proc, out, _, _, cleaned := startShMonitorTest(t, persistent, time.Second, 500*time.Millisecond)
			proc.write(t, "finished\n")
			proc.finish(nil)
			select {
			case result := <-out:
				if !strings.Contains(result, "exit code 0") || strings.Contains(result, "PID") {
					t.Fatal(result)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("early completion waited for timer")
			}
			select {
			case <-cleaned:
			default:
				t.Fatal("completed process not cleaned up")
			}
		})
	}
}

func TestShTimeoutAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		persistent, cancel bool
		want               string
	}{
		{"timeout", false, false, "command timed out"},
		{"cancel one-shot", false, true, "command cancelled"},
		{"cancel persistent before registration", true, true, "command cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc, out, cancel, _, cleaned := startShMonitorTest(t, tc.persistent, 100*time.Millisecond, time.Second)
			proc.write(t, "partial SECRET_VALUE\n")
			if tc.cancel {
				cancel()
			}
			result := shMonitorResult(t, out)
			if !strings.HasPrefix(result, "error:") || !strings.Contains(result, tc.want) || !strings.Contains(result, "partial [REDACTED]") || !strings.Contains(result, "process terminated") {
				t.Fatal(result)
			}
			if strings.Contains(result, "SECRET_VALUE") || strings.Contains(result, "PID") {
				t.Fatal(result)
			}
			select {
			case <-proc.done:
			default:
				t.Fatal("process not terminated")
			}
			select {
			case <-cleaned:
			default:
				t.Fatal("sandbox not cleaned up")
			}
		})
	}
}

func TestShFailurePreservesOutputAndExitCode(t *testing.T) {
	proc, out, _, _, _ := startShMonitorTest(t, false, time.Second, 10*time.Millisecond)
	proc.write(t, "failure details\n")
	proc.finish(shTestExitError(7))
	result := shMonitorResult(t, out)
	if !strings.HasPrefix(result, "command failed: exit code 7") || !strings.Contains(result, "failure details") {
		t.Fatal(result)
	}
}

func TestShPersistentSurvivesReturn(t *testing.T) {
	proc, out, cancelTurn, cmdCtx, cleaned := startShMonitorTest(t, true, 0, 10*time.Millisecond)
	proc.write(t, "boot\n")
	result := shMonitorResult(t, out)
	if !strings.Contains(result, "persistent process started, PID") {
		t.Fatal(result)
	}
	cancelTurn()
	if cmdCtx.Err() != nil {
		t.Fatal("persistent process tied to turn context")
	}
	select {
	case <-cleaned:
		t.Fatal("persistent sandbox cleaned prematurely")
	default:
	}
	proc.write(t, "after-return\n")
	log := executeSniffTool(map[string]any{"pid": float64(proc.Pid())})
	if !strings.Contains(log, "after-return") || !strings.Contains(log, "running") {
		t.Fatal(log)
	}
	proc.finish(nil)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(log, "exited") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		log = executeSniffTool(map[string]any{"pid": float64(proc.Pid())})
	}
	if !strings.Contains(log, "exited") {
		t.Fatal(log)
	}
	if result := executeKillTool(map[string]any{"pid": float64(proc.Pid())}); !strings.Contains(result, "stopped") {
		t.Fatal(result)
	}
	if result := executeSniffTool(map[string]any{"pid": float64(proc.Pid())}); !strings.Contains(result, "no persistent process found") {
		t.Fatal(result)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("kill did not clean sandbox")
	}
}

func TestShStatusSurvivesOutputTruncation(t *testing.T) {
	proc, out, _, _, _ := startShMonitorTest(t, false, time.Second, 10*time.Millisecond)
	proc.write(t, strings.Repeat("x", 40000))
	proc.finish(shTestExitError(2))
	result := shMonitorResult(t, out)
	if !strings.HasPrefix(result, "command failed: exit code 2") || !strings.Contains(result, "output truncated") {
		t.Fatal("exit status lost when truncating output")
	}
}
