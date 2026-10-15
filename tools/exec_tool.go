package tools

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"github.com/joho/godotenv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fuckyourcli/types"
)

// Only persistent commands may detach; one-shot commands keep their output reader
// and cancellation scope until completion. Tests can shorten the detach window.
var shBackgroundGrace = 10 * time.Second

const (
	shDefaultTimeout   = 120 * time.Second
	shMaxTimeout       = 300 * time.Second
	shOutputDrainGrace = 200 * time.Millisecond
)

func shTimeout(args map[string]any, persistence bool) (time.Duration, error) {
	if persistence {
		return 0, nil
	}
	value, exists := args["timeout_seconds"]
	if !exists {
		return shDefaultTimeout, nil
	}
	seconds, ok := value.(float64)
	if !ok || !(seconds >= 1 && seconds <= float64(shMaxTimeout/time.Second)) || seconds != float64(int(seconds)) {
		return 0, fmt.Errorf("timeout_seconds must be an integer between 1 and 300")
	}
	return time.Duration(seconds) * time.Second, nil
}

// ─── helper types (shared with tools.go) ─────────────────────────────────────

// persistentProcess tracks a long-running shell process.
// (Kept here so exec_tool.go can manage them; the var persistentProcesses lives
// in tools.go.)

// goFlagsWithBuildVCS returns the GOFLAGS value handed to the sandboxed shell.
// It appends -buildvcs=false so `go build`/`go test` keep working even though
// the sandbox masks .git (VCS stamping would otherwise abort with "error
// obtaining VCS status"). A -buildvcs flag already present is preserved as-is
// and never duplicated.
func goFlagsWithBuildVCS(existing string) string {
	existing = strings.TrimSpace(existing)
	for _, f := range strings.Fields(existing) {
		if name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "="); name == "buildvcs" {
			return existing
		}
	}
	if existing == "" {
		return "-buildvcs=false"
	}
	return existing + " -buildvcs=false"
}

// goBinarySupportsBuildVCS reports whether the sandboxed shell should get
// GOFLAGS=-buildvcs=false. The sandbox masks .git, so `go build`/`go test`
// abort with "error obtaining VCS status" unless the flag is off; the flag only
// exists since Go 1.18, so we must not feed it to an older Go.
//
// The version is read from the `go` binary on PATH with debug/buildinfo (no
// fork). The default is to inject: we skip only when we can prove the Go is
// older than 1.18. A missing `go` (the variable is then inert), an unreadable
// version (wrapper/shim) or an unknown format all still inject.
func goBinarySupportsBuildVCS() bool {
	path, err := exec.LookPath("go")
	if err != nil {
		return true
	}
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return true
	}
	return !goVersionProvenOld(bi.GoVersion, 1, 18)
}

// goVersionProvenOld reports whether v is recognisably a Go version older than
// major.minor. It returns false for anything it cannot parse ("", "go", "go1",
// arbitrary text), so an unknown format never blocks the -buildvcs default.
// It tolerates the "devel go1.27-abcdef" form emitted by tip builds.
func goVersionProvenOld(v string, major, minor int) bool {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "devel ")
	v = strings.TrimPrefix(v, "go")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	maj, ok := leadingInt(parts[0])
	if !ok {
		return false
	}
	min, ok := leadingInt(parts[1])
	if !ok {
		return false
	}
	if maj != major {
		return maj < major
	}
	return min < minor
}

// leadingInt parses the leading decimal digits of s (so "26-devel" -> 26).
func leadingInt(s string) (int, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:i])
	return n, err == nil
}

// executeShTool handles the "sh" case.
// Usage: result := executeShTool(ctx, args, restrictedRoot, globalIgnore)
func executeShTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	cmd, ok := args["command"].(string)
	if !ok {
		return "error: command argument required"
	}
	persistence, _ := args["persistence"].(bool)
	timeout, err := shTimeout(args, persistence)
	if err != nil {
		return "error: " + err.Error()
	}

	// No process or timeout starts until the sandbox envelope is approved.
	envPath := filepath.Join(restrictedRoot, ".env")

	allowedRW, allowedRO := standardAllowedDirs(restrictedRoot)

	denyEnvKeys := []string{
		"AWS_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_SESSION_TOKEN",
		"GITHUB_TOKEN",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GCP_SERVICE_ACCOUNT_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"DATABASE_URL",
		"SECRET_KEY",
		"PRIVATE_KEY",
		"NPM_TOKEN",
		"DOCKER_PASSWORD",
	}

	var secretValues []string
	var secretEnvPairs []string
	var injectedKeys []string
	if envMap, err := godotenv.Read(envPath); err == nil {
		for k, v := range envMap {
			secretEnvPairs = append(secretEnvPairs, k+"="+v)
			injectedKeys = append(injectedKeys, k)
			if v != "" {
				secretValues = append(secretValues, v)
			}
		}
		sort.Strings(injectedKeys)
	}

	details := execApprovalDetails(cmd, persistence, restrictedRoot, allowedRW, allowedRO, injectedKeys, denyEnvKeys, timeout)

	allowed, suggestion := askExecPermission(ctx, cmd, details)
	if !allowed {
		if suggestion != "" {
			return "error: exec cancelled by the user: " + suggestion
		}
		return "error: exec cancelled by the user"
	}
	_ = suggestion

	// The command is parented to the tool-call context, which is the turn context
	// (streamProviderChat → newGenerationContext): aborting the turn (ctrl+c in
	// the TUI, a client disconnect in the server) cancels it and
	// exec.CommandContext kills the sandboxed child. A persistent process must
	// outlive the turn, so it is detached from it (context.WithoutCancel) and is
	// stopped only by kill() through persistentProcess.Cancel.
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if persistence {
		cmdCtx, cancel = context.WithCancel(context.WithoutCancel(ctx))
	} else {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	home, _ := os.UserHomeDir()

	// The sandbox masks .git, so VCS stamping would abort with "error obtaining
	// VCS status". Default GOFLAGS to -buildvcs=false (unless the system Go is
	// older than 1.18), never duplicating a -buildvcs value already set.
	if goBinarySupportsBuildVCS() {
		secretEnvPairs = append(secretEnvPairs, "GOFLAGS="+goFlagsWithBuildVCS(os.Getenv("GOFLAGS")))
	}
	secretEnvPairs = append(secretEnvPairs,
		"PNPM_HOME="+home+"/.local/share/pnpm",
		"PNPM_STORE_DIR="+home+"/.local/share/pnpm/store",
		"npm_config_store_dir="+home+"/.local/share/pnpm/store",
	)
	scfg := sandboxConfig{
		RestrictedRoot: restrictedRoot,
		AllowedRW:      allowedRW,
		AllowedRO:      allowedRO,
		DenyEnv:        denyEnvKeys,
		SecretEnvPairs: secretEnvPairs,
		CmdStr:         cmd,
	}

	result, err := buildSandboxCmd(cmdCtx, scfg)
	if err != nil {
		cancel()
		return fmt.Sprintf("error: failed to build command: %v", err)
	}
	proc, err := startSandboxed(result)
	if err != nil {
		cancel()
		result.Cleanup()
		return fmt.Sprintf("failed to start sandboxed command: %v", err)
	}
	return monitorShProcess(ctx, cmdCtx, cancel, result, proc, cmd, persistence, timeout, secretValues)
}

// monitorShProcess owns cleanup until ownership transfers to the persistent registry.
func monitorShProcess(ctx, cmdCtx context.Context, cancel context.CancelFunc, result sandboxResult, proc sandboxProcess, cmd string, persistence bool, timeout time.Duration, secretValues []string) string {
	registered := false
	defer func() {
		if !registered {
			cancel()
			proc.Close()
			result.Cleanup()
		}
	}()

	outBuf := newTailBuffer(defaultTailBufferBytes)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := proc.Read(buf)
			if n > 0 {
				outBuf.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()

	redact := func(s string) string {
		for _, v := range secretValues {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
		return s
	}
	output := func(status string) string {
		data, _ := outBuf.Snapshot()
		out := redact(string(data))
		if len(out) > 32768 {
			out = out[:32768] + "\n... [output truncated]"
		}
		if out == "" {
			out = "(no output)"
		}
		return status + "\n" + out
	}
	finish := func(cmdErr, ctxErr error) string {
		// Let the reader consume remaining output before closing the PTY/pipe.
		// A descendant may hold it open, so draining must be bounded.
		drain := time.NewTimer(shOutputDrainGrace)
		defer drain.Stop()
		select {
		case <-readDone:
		case <-drain.C:
		}
		proc.Close()
		<-readDone
		return output(shCommandStatus(cmdErr, ctxErr, timeout))
	}

	ctxDone := cmdCtx.Done()
	var background <-chan time.Time
	if persistence {
		ctxDone = ctx.Done()
		timer := time.NewTimer(shBackgroundGrace)
		defer timer.Stop()
		background = timer.C
	}

	select {
	case cmdErr := <-done:
		var ctxErr error
		if cmdErr != nil {
			ctxErr = cmdCtx.Err()
		}
		return finish(cmdErr, ctxErr)
	case <-ctxDone:
		ctxErr := cmdCtx.Err()
		if persistence {
			ctxErr = ctx.Err()
		}
		cancel()
		proc.Close()
		cmdErr := <-done
		return finish(cmdErr, ctxErr)
	case <-background:
		// Completion/cancellation wins if it raced with the detach timer.
		select {
		case cmdErr := <-done:
			return finish(cmdErr, nil)
		case <-ctxDone:
			cancel()
			proc.Close()
			return finish(<-done, ctx.Err())
		default:
		}
		pid := proc.Pid()
		pp := &persistentProcess{
			Result: &result, Proc: proc, Cancel: cancel, Pid: pid,
			Command: cmd, Buf: outBuf, Redact: redact,
		}
		persistentMu.Lock()
		persistentProcesses[pid] = pp
		persistentMu.Unlock()
		registered = true
		go func() { pp.setExited(<-done) }()
		return output(fmt.Sprintf("[persistent process started, PID %d; use sniff(pid) to read its log tail, kill(pid) to stop it]", pid))
	}
}

func shCommandStatus(cmdErr, ctxErr error, timeout time.Duration) string {
	if ctxErr == context.DeadlineExceeded {
		return fmt.Sprintf("error: command timed out (timeout %s); process terminated", timeout)
	}
	if ctxErr != nil {
		return "error: command cancelled; process terminated"
	}
	if cmdErr == nil {
		return "[command completed, exit code 0]"
	}
	if exitErr, ok := cmdErr.(interface{ ExitCode() int }); ok {
		return fmt.Sprintf("command failed: exit code %d (%v)", exitErr.ExitCode(), cmdErr)
	}
	return fmt.Sprintf("command failed: %v", cmdErr)
}

// execApprovalDetails renders the sandbox envelope shown by the "See details"
// option of the exec permission prompt: the full command plus what the sandbox
// will expose. It is built before the command runs, so the user approves exactly
// what will happen — and the command is never truncated by the terminal the way
// a tall inline permission title was. Lines are typed (types.DetailLine) so the
// viewer shell-highlights the command section and colours the envelope instead
// of dumping plain text.
func execApprovalDetails(cmd string, persistence bool, restrictedRoot string, rw, ro, injected, denied []string, timeout time.Duration) []types.DetailLine {
	out := []types.DetailLine{{Kind: types.DetailSection, Text: "Command:"}}
	for _, l := range strings.Split(strings.TrimRight(cmd, "\n"), "\n") {
		out = append(out, types.DetailLine{Kind: types.DetailCommand, Text: "  " + l})
	}
	out = append(out, types.DetailLine{})

	persistenceDesc := fmt.Sprintf("false (one-shot, %s timeout)", timeout)
	if persistence {
		persistenceDesc = "true (stays alive; kill(pid)/sniff(pid) available)"
	}
	out = append(out, types.DetailLine{Text: "Persistence: " + persistenceDesc})
	out = append(out, types.DetailLine{Text: "Workspace root: " + restrictedRoot})
	out = append(out, detailsSection("Writable inside the sandbox (RW):", types.DetailPath, rw)...)
	out = append(out, detailsSection("Readable inside the sandbox (RO):", types.DetailPath, ro)...)
	out = append(out, detailsSection("Injected env (.env keys):", types.DetailEnvKey, injected)...)
	out = append(out, detailsSection("Denied env keys:", types.DetailEnvKey, denied)...)
	return out
}

// detailsSection renders a titled list of typed items, or "(none)" when the
// list is empty. The kind is what the viewer paints each item with (a path or
// an environment key).
func detailsSection(title string, kind types.DetailLineKind, items []string) []types.DetailLine {
	out := []types.DetailLine{{Kind: types.DetailSection, Text: title}}
	if len(items) == 0 {
		return append(out, types.DetailLine{Text: "  (none)"})
	}
	for _, it := range items {
		out = append(out, types.DetailLine{Kind: kind, Text: "  " + it})
	}
	return out
}

// executeKillTool handles the "kill" case.
// Usage: result := executeKillTool(args)
func executeKillTool(args map[string]any) string {
	pid, ok := args["pid"].(float64)
	if !ok {
		return "error: pid argument required as number"
	}
	intPid := int(pid)
	persistentMu.Lock()
	res, exists := persistentProcesses[intPid]
	persistentMu.Unlock()
	if !exists {
		return fmt.Sprintf("error: no persistent process found with PID %d", intPid)
	}
	res.Result.Cleanup()
	res.Proc.Close()
	res.Cancel()
	persistentMu.Lock()
	delete(persistentProcesses, intPid)
	persistentMu.Unlock()
	return fmt.Sprintf("process %d stopped", intPid)
}
