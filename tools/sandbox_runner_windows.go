//go:build windows

package tools

// the runtime half of the dedicated-user
// sandbox — launcher, runner and named-pipe IPC.
//
// The Fase-D engine runs every `sh` command as a *different* Windows identity
// (FycSandbox) instead of as the real user carrying a restricted token. That is
// what brings back PowerShell (HKCU) and isolates DPAPI/objects (section 2). A
// process cannot mint a token for another user on its own, so the command is
// started along the documented logon path:
//
//	fuckyourcli.exe (real user)
//	  └─ CreateProcessWithLogonW(FycSandbox, LOGON_WITH_PROFILE)        ← launcher
//	       └─ fuckyourcli.exe __sandbox-runner --pipe-in=… --pipe-out=… ← runner
//	            └─ CreateProcessAsUser(restricted token, from the runner's OWN token)
//	                 └─ cmd.exe /d /s /c <cmd>                       ← the command
//
// The runner is a second fyc.exe, so it re-enters this package through
// MaybeRunAsSandboxRunner. It is the *only* place that can build the restricted
// token: CreateProcessAsUser on a token derived from the caller's own token is
// the one case that needs no SeAssignPrimaryToken / SeIncreaseQuota — the
// "privilege barrier" (section 6).
//
// LOGON_WITH_PROFILE gives the runner its own HKCU and profile but does NOT
// inherit the launcher's handles or console, so the two talk over *named* pipes
// whose security descriptor grants only the sandbox identity (section 9, named
// pipe hardening in section 14.6). pipe-in carries the base64 JSON spec,
// pipe-out carries the command's merged stdout/stderr, and the runner's exit
// code is the command's exit code.
//
// The launcher plants the runtime allow-list (section 5): the workspace root gets
// one inheritable ACE per SID, and the *runner* plants the synthetic write-lock on
// the sandbox account's own profile — the toolchain caches (GOPATH/GOCACHE, npm,
// pip, cargo, TEMP) all live under that profile, so the identity can write where
// its tools expect. The launcher cannot do that part itself: it is the *real*
// user, which has no WRITE_DAC on the sandbox profile, while the runner has Full
// on its own. The rest of the host stays default-deny for the foreign identity.
//
// Fallback (section 9): when the D1 setup is not in place the caller keeps the
// restricted-token engine (sandbox_windows.go). When the setup *is* in place but
// the runner copy is missing or stale, the launcher fails loudly (it never falls
// back). There is no silent unsandboxed path.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── constants ───────────────────────────────────────────────────────────────

const (
	// sandboxRunnerModeArg is the first argument that turns this binary into the
	// runner (the second process in the D2 chain).
	sandboxRunnerModeArg = "__sandbox-runner"
	// The runner learns the two named pipes by name, not by inherited handle:
	// LOGON_WITH_PROFILE does not carry handles across (section 9).
	sandboxRunnerPipeInArg  = "--pipe-in"
	sandboxRunnerPipeOutArg = "--pipe-out"

	// sandboxRunnerConnectTimeout bounds the wait for the runner to open its
	// ends of the pipes. The first logon creates the sandbox profile and can
	// stall, so it is generous.
	sandboxRunnerConnectTimeout = 300 * time.Second

	// Named-pipe flags (winbase.h) — kept local so the file is self-contained.
	rnrPipeAccessInbound  = 0x00000001 // server reads
	rnrPipeAccessOutbound = 0x00000002 // server writes
	rnrPipeTypeByte       = 0x00000000
	rnrPipeReadmodeByte   = 0x00000000
	rnrPipeWait           = 0x00000000
	rnrPipeUnlimited      = 255
	rnrFirstPipeInstance  = 0x00080000 // FILE_FLAG_FIRST_PIPE_INSTANCE
	rnrOpenExisting       = 3          // OPEN_EXISTING

	rnrWaitTimeout = 0x00000102

	// CreateProcessWithLogonW logon flags.
	rnrLogonWithProfile = 0x00000001

	// winerror.h codes the pipe/connect dance reacts to.
	rnrErrIOPending     = 997 // ERROR_IO_PENDING
	rnrErrPipeConnected = 535 // ERROR_PIPE_CONNECTED
	rnrErrNoData        = 232 // ERROR_NO_DATA
)

// CreateProcessWithLogonW is not exposed by x/sys/windows.
var rnrCreateProcessWithLogonW = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithLogonW")

// rnrIsErr reports whether err carries the given Win32 error code.
func rnrIsErr(err error, code uintptr) bool {
	return err != nil && errors.Is(err, syscall.Errno(code))
}

// ── self-re-exec: the runner entry point ────────────────────────────────────

// MaybeRunAsSandboxRunner must be called FIRST in main(), right after
// MaybeRunAsSandboxChild. In the runner process it never returns: it runs the
// command and exits with its status. In the normal process it is a no-op.
func MaybeRunAsSandboxRunner() {
	if len(os.Args) < 2 || os.Args[1] != sandboxRunnerModeArg {
		return
	}
	os.Exit(sandboxRunnerMain(os.Args[2:]))
}

// parseRunnerArgs extracts the two pipe names from `--pipe-in=<name>
// --pipe-out=<name>`. Split out so it is unit-testable without a live login.
func parseRunnerArgs(args []string) (pipeIn, pipeOut string) {
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, sandboxRunnerPipeInArg+"="):
			pipeIn = strings.TrimPrefix(a, sandboxRunnerPipeInArg+"=")
		case strings.HasPrefix(a, sandboxRunnerPipeOutArg+"="):
			pipeOut = strings.TrimPrefix(a, sandboxRunnerPipeOutArg+"=")
		}
	}
	return pipeIn, pipeOut
}

// sandboxRunnerMain is the runner half: it reads the spec, mints the restricted
// token from its own (sandbox-user) token and runs the command, streaming the
// command's output back on pipe-out. Its return value becomes the process exit
// code and is the command's exit code.
func sandboxRunnerMain(args []string) int {
	pipeIn, pipeOut := parseRunnerArgs(args)
	if pipeIn == "" || pipeOut == "" {
		fmt.Fprintln(os.Stderr, "sandbox runner: missing --pipe-in/--pipe-out")
		return 2
	}

	inH, err := openRunnerPipe(pipeIn, windows.GENERIC_READ)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox runner: open pipe-in: "+err.Error())
		return 3
	}

	outH, err := openRunnerPipe(pipeOut, windows.GENERIC_WRITE)
	if err != nil {
		windows.CloseHandle(inH)
		fmt.Fprintln(os.Stderr, "sandbox runner: open pipe-out: "+err.Error())
		return 3
	}
	defer windows.CloseHandle(outH)

	enc := readAllFromPipe(inH)
	windows.CloseHandle(inH)

	spec, err := unmarshalSandboxSpec(string(enc))
	if err != nil {
		runnerWriteDiag(outH, "sandbox runner: bad spec: "+err.Error())
		return 3
	}

	code := runRunnerChild(spec, outH)
	return code
}

// runRunnerChild is the runner's core: base token = the runner's own token (the
// sandbox user, profile loaded), restricted to [Everyone, logon, synthetic] with
// WRITE_RESTRICTED and — unlike the current engine — *without* disabling the
// user SID (section 10). The synthetic SID is the write lock the launcher
// planted on the RW paths; the user SID stays enabled so the sandbox identity
// keeps its own profile and HKCU.
func runRunnerChild(spec sandboxSpec, outH windows.Handle) int {
	base, err := rtOpenSelfToken()
	if err != nil {
		runnerWriteDiag(outH, "sandbox runner: open token: "+err.Error())
		return win32ExitError
	}
	defer base.Close()

	everyone, logon, synthetic, err := rtRestrictingSIDs(base)
	if err != nil {
		runnerWriteDiag(outH, "sandbox runner: restricting SIDs: "+err.Error())
		return win32ExitError
	}

	// The command writes its toolchain caches (GOPATH/GOCACHE, npm, pip, cargo,
	// TEMP…) under *this* account's profile, and a WRITE_RESTRICTED token needs
	// the synthetic lock there or those writes fail. The launcher (the real user)
	// has no WRITE_DAC on that profile; the runner does (Full on its own profile),
	// so it plants the lock itself — inheritable, idempotent across commands.
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		if gerr := grantRootInherited(home, synthetic); gerr != nil {
			runnerWriteDiag(outH, "sandbox runner: profile write-lock: "+gerr.Error())
		}
	}
	tok, err := rtNewRestrictedToken(base, nil, everyone, logon, synthetic)
	if err != nil {
		runnerWriteDiag(outH, "sandbox runner: CreateRestrictedToken: "+err.Error())
		return win32ExitError
	}
	defer tok.Close()
	if err := rtSetTokenDefaultDacl(tok, logon, synthetic); err != nil {
		runnerWriteDiag(outH, "sandbox runner: default DACL: "+err.Error())
		return win32ExitError
	}

	nul, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		runnerWriteDiag(outH, "sandbox runner: open NUL: "+err.Error())
		return win32ExitError
	}
	defer nul.Close()

	// The command inherits pipe-out as stdout+stderr and NUL as stdin: both must
	// be marked inheritable before CreateProcessAsUser.
	_ = windows.SetHandleInformation(outH, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
	_ = windows.SetHandleInformation(windows.Handle(nul.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)

	// The environment is the runner's own (the sandbox user's, from
	// LOGON_WITH_PROFILE): deny-listed keys stripped, the .env secrets added.
	env := append(filteredEnv(spec.DenyEnv), spec.SecretEnvPairs...)

	code, err := runRunnerChildProcess(tok, windowsShellCmdLine(spec.CmdStr), spec.Cwd, env,
		windows.Handle(nul.Fd()), outH)
	if err != nil {
		runnerWriteDiag(outH, "sandbox runner: "+err.Error())
		return win32ExitError
	}
	return int(code)
}

// runRunnerChildProcess is CreateProcessAsUser with the caller's stdio handles
// and a KILL_ON_JOB_CLOSE job object, so closing/terminating the runner tears
// down the whole command tree (cmd.exe and any grandchild).
func runRunnerChildProcess(tok windows.Token, cmdline, cwd string, env []string, stdin, stdout windows.Handle) (uint32, error) {
	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = stdin
	si.StdOutput = stdout
	si.StdErr = stdout

	cmdlinePtr, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return 0, err
	}
	var cwdPtr *uint16
	if cwd != "" {
		if cwdPtr, err = windows.UTF16PtrFromString(cwd); err != nil {
			return 0, err
		}
	}
	envBlock, err := buildEnvBlock(env)
	if err != nil {
		return 0, err
	}

	job, err := createKillJob()
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job)

	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcessAsUser(tok, nil, cmdlinePtr, nil, nil, true, flags,
		&envBlock[0], cwdPtr, si, &pi); err != nil {
		return 0, fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)

	// Best-effort: the command runs either way, only tree teardown is weaker if
	// the assignment is refused.
	_ = windows.AssignProcessToJobObject(job, pi.Process)

	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// ── runner-side pipe helpers ────────────────────────────────────────────────

func openRunnerPipe(name string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, access, 0, nil, rnrOpenExisting, 0, 0)
}

// readAllFromPipe reads until the peer closes its end (ERROR_BROKEN_PIPE). The
// spec is a single small message written in one shot by the launcher.
func readAllFromPipe(h windows.Handle) []byte {
	var out []byte
	buf := make([]byte, 64*1024)
	for {
		var n uint32
		err := windows.ReadFile(h, buf, &n, nil)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil || n == 0 {
			break
		}
	}
	return out
}

// runnerWriteDiag surfaces a runner-side failure on pipe-out so the user sees it
// (the runner has no console or other channel out of its logon session).
func runnerWriteDiag(h windows.Handle, msg string) {
	var n uint32
	_ = windows.WriteFile(h, []byte(msg+"\r\n"), &n, nil)
}

// ── launcher side ───────────────────────────────────────────────────────────

// sandboxRunnerReady reports whether the D1 setup is in place: a current marker,
// readable credentials and the account present. Any miss means "fall back to the
// restricted-token engine", never "run unsandboxed".
func sandboxRunnerReady() bool {
	if m, ok := sbxReadMarker(); !ok || m.Version < sandboxSetupVersion {
		return false
	}
	if _, ok := sbxLoadCredentials(); !ok {
		return false
	}
	if sid, err := sbxLookupSID(sandboxAccountName); err != nil || sid == nil {
		return false
	}
	return true
}

// runnerBinaryForLaunch returns the ProgramData copy of this binary that the
// sandbox identity is allowed to execute. The setup records the copy's hash in
// the marker: when the running binary differs (an update after `sandbox setup`),
// the launcher refuses to run rather than silently fall back to the
// restricted-token engine — the user must re-run `sandbox setup`.
func runnerBinaryForLaunch() (string, error) {
	m, ok := sbxReadMarker()
	if !ok || m.RunnerPath == "" || m.RunnerHash == "" {
		return "", errors.New("sandbox: the dedicated-user setup has no runner binary — run `fuckyourcli sandbox setup`")
	}
	if !pathExists(m.RunnerPath) {
		return "", fmt.Errorf("sandbox: runner binary %s is missing — run `fuckyourcli sandbox setup`", m.RunnerPath)
	}
	if !runnerUpToDate(m) {
		return "", errors.New("sandbox: the binary changed since setup — run `fuckyourcli sandbox setup`")
	}
	return m.RunnerPath, nil
}

// runnerUpToDate reports whether the marker's runner copy still matches this
// binary: the recorded path must exist and its hash must equal the running
// executable's. runnerBinaryForLaunch is fail-closed on a stale copy, so
// `sandbox setup` uses this to decide idempotency: a stale or missing runner
// must NOT short-circuit the setup, or the launcher stays broken and only
// `sandbox reset` + `sandbox setup` recovers.
func runnerUpToDate(m sbxMarker) bool {
	if m.RunnerPath == "" || m.RunnerHash == "" || !pathExists(m.RunnerPath) {
		return false
	}
	self, err := currentExeSHA256()
	if err != nil {
		return false
	}
	return strings.EqualFold(self, m.RunnerHash)
}

var (
	selfHashOnce sync.Once
	selfHashVal  string
	selfHashErr  error
)

// currentExeSHA256 hashes this process's own executable once per run (the file
// cannot change underneath us), so the check stays cheap across many sh calls.
func currentExeSHA256() (string, error) {
	selfHashOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			selfHashErr = err
			return
		}
		selfHashVal, selfHashErr = sbxFileSHA256(exe)
	})
	return selfHashVal, selfHashErr
}

// runnerLaunch carries everything the spawn closure needs between
// buildSandboxCmd and startSandboxed.
type runnerLaunch struct {
	ctx     context.Context
	user    string
	pass    string
	exe     string
	inName  string
	outName string
	specEnc string
	pipeIn  windows.Handle
	pipeOut windows.Handle
	timeout time.Duration

	mu        sync.Mutex
	handedOff bool
}

// buildRunnerSandboxCmd builds the D2 launcher result when the dedicated-user
// setup is available. It returns ok=false (with a nil error) when it is not, so
// buildSandboxCmd falls back to the restricted-token engine.
func buildRunnerSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, bool, error) {
	if !sandboxRunnerReady() {
		return sandboxResult{}, false, nil
	}
	creds, ok := sbxLoadCredentials()
	if !ok || creds.Password == "" {
		return sandboxResult{}, false, nil
	}
	userSID, err := sbxLookupSID(sandboxAccountName)
	if err != nil || userSID == nil {
		return sandboxResult{}, false, nil // incomplete setup: fall back, never fail open
	}
	synth, err := rtSyntheticSID()
	if err != nil {
		return sandboxResult{}, false, fmt.Errorf("sandbox: synthetic SID: %w", err)
	}
	exe, err := runnerBinaryForLaunch()
	if err != nil {
		return sandboxResult{}, false, err
	}

	// Note: the toolchain caches the sandbox writes live under the *sandbox*
	// account's own profile, which the launcher (the real user) neither owns nor
	// has WRITE_DAC on; the runner plants the synthetic write-lock there (see
	// runRunnerChild). Here the launcher grants only what it owns: the workspace
	// and the launcher-side RW paths.
	spec := sandboxSpec{
		Cwd:            cfg.RestrictedRoot,
		AllowedRW:      cfg.AllowedRW,
		AllowedRO:      cfg.AllowedRO,
		DenyEnv:        cfg.DenyEnv,
		SecretEnvPairs: cfg.SecretEnvPairs,
		CmdStr:         cfg.CmdStr,
		Network:        defaultWindowsSandboxNetwork,
	}

	// Runtime allow-list (section 5): the parent owns the RW trees (workspace +
	// toolchain caches), so it grants the sandbox identity and the synthetic
	// write-lock there. The profile itself stays default-deny.
	plan := planSandboxACL(spec)
	if _, err := ensureDedicatedUserACL(userSID, synth, plan); err != nil {
		return sandboxResult{}, false, fmt.Errorf("sandbox: grant ACL: %w", err)
	}

	enc, err := marshalSandboxSpec(spec)
	if err != nil {
		return sandboxResult{}, false, fmt.Errorf("sandbox: cannot encode spec: %w", err)
	}

	inName, outName, err := runnerPipeNames()
	if err != nil {
		return sandboxResult{}, false, fmt.Errorf("sandbox: pipe name: %w", err)
	}
	inH, err := createRunnerPipe(inName, false, userSID, synth)
	if err != nil {
		return sandboxResult{}, false, fmt.Errorf("sandbox: create pipe-in: %w", err)
	}
	outH, err := createRunnerPipe(outName, true, userSID, synth)
	if err != nil {
		windows.CloseHandle(inH)
		return sandboxResult{}, false, fmt.Errorf("sandbox: create pipe-out: %w", err)
	}

	launch := &runnerLaunch{
		ctx: ctx, user: creds.User, pass: creds.Password, exe: exe,
		inName: inName, outName: outName, specEnc: enc,
		pipeIn: inH, pipeOut: outH, timeout: sandboxRunnerConnectTimeout,
	}
	res := sandboxResult{
		Cleanup: func() { launch.cleanup() },
		spawn:   func() (sandboxProcess, error) { return launch.spawn() },
	}
	return res, true, nil
}

// cleanup releases the pipes when the launch never handed off (i.e. spawn was
// not reached or failed). Idempotent, so the deferred Cleanup after a successful
// run is a no-op.
func (l *runnerLaunch) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handedOff {
		return
	}
	l.handedOff = true
	if l.pipeIn != 0 {
		windows.CloseHandle(l.pipeIn)
		l.pipeIn = 0
	}
	if l.pipeOut != 0 {
		windows.CloseHandle(l.pipeOut)
		l.pipeOut = 0
	}
}

// spawn starts the runner, connects the pipes, hands over the spec and returns
// the running process. Any failure tears the runner down and cleans up.
func (l *runnerLaunch) spawn() (sandboxProcess, error) {
	proc, pid, err := l.startRunner()
	if err != nil {
		l.cleanup()
		return nil, err
	}
	fail := func(what string, e error) (sandboxProcess, error) {
		_ = windows.TerminateProcess(proc, 1)
		windows.CloseHandle(proc)
		l.cleanup()
		return nil, fmt.Errorf("sandbox: runner %s: %w", what, e)
	}

	if err := connectPipeWithTimeout(l.pipeIn, l.timeout); err != nil {
		return fail("pipe-in", err)
	}
	if err := connectPipeWithTimeout(l.pipeOut, l.timeout); err != nil {
		return fail("pipe-out", err)
	}
	if err := writeAllToPipe(l.pipeIn, []byte(l.specEnc)); err != nil {
		return fail("write spec", err)
	}
	// Close our end so the runner's read sees EOF and starts the command.
	windows.CloseHandle(l.pipeIn)

	p := &windowsRunnerProcess{proc: proc, pid: pid}
	p.out = os.NewFile(uintptr(l.pipeOut), "fyc-sandbox-out")

	l.mu.Lock()
	l.pipeIn = 0
	l.handedOff = true
	l.mu.Unlock()

	// Honor context cancellation: killing the runner closes its job handle and,
	// through KILL_ON_JOB_CLOSE, the whole command tree.
	go func() {
		<-l.ctx.Done()
		_ = p.Close()
	}()
	return p, nil
}

// startRunner launches `fyc.exe __sandbox-runner --pipe-in=… --pipe-out=…` as
// the sandbox user with LOGON_WITH_PROFILE (own profile + HKCU, no inherited
// handles or console — hence the named pipes).
// startRunner launches `fyc.exe __sandbox-runner --pipe-in=… --pipe-out=…` as
// the sandbox user with LOGON_WITH_PROFILE (own profile + HKCU, no inherited
// handles or console — hence the named pipes).
func (l *runnerLaunch) startRunner() (windows.Handle, int, error) {
	cmdline := fmt.Sprintf(`"%s" %s %s=%s %s=%s`,
		l.exe, sandboxRunnerModeArg, sandboxRunnerPipeInArg, l.inName,
		sandboxRunnerPipeOutArg, l.outName)
	return rnrLogonRun(l.user, l.pass, l.exe, cmdline, filepath.Dir(l.exe))
}

// ── the running runner, seen from the launcher ──────────────────────────────

type windowsRunnerProcess struct {
	mu     sync.Mutex
	proc   windows.Handle
	pid    int
	out    *os.File
	closed bool
}

type sandboxRunnerExitError uint32

func (e sandboxRunnerExitError) Error() string {
	return fmt.Sprintf("sandbox: command exited with status %d", e)
}

func (e sandboxRunnerExitError) ExitCode() int { return int(e) }

func (p *windowsRunnerProcess) Pid() int { return p.pid }

func (p *windowsRunnerProcess) Read(b []byte) (int, error) {
	p.mu.Lock()
	out := p.out
	p.mu.Unlock()
	if out == nil {
		return 0, io.EOF
	}
	return out.Read(b)
}

// Wait reaps the runner and maps a non-zero exit code (the command's) to an
// error, matching the os/exec contract the caller relies on.
func (p *windowsRunnerProcess) Wait() error {
	p.mu.Lock()
	h := p.proc
	p.mu.Unlock()
	if h == 0 {
		return nil
	}
	if _, err := windows.WaitForSingleObject(h, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return err
	}
	p.mu.Lock()
	if p.proc == h {
		windows.CloseHandle(h)
		p.proc = 0
	}
	p.mu.Unlock()
	if code != 0 {
		return sandboxRunnerExitError(code)
	}
	return nil
}

// Close stops the runner (which tears down the command tree via its job object)
// and releases the read end of pipe-out. Idempotent.
func (p *windowsRunnerProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.proc != 0 {
		// The waiter (Wait) closes the handle once the process is reaped; if
		// nobody is waiting, the handle is released at process exit.
		_ = windows.TerminateProcess(p.proc, 1)
	}
	if p.out != nil {
		p.out.Close()
		p.out = nil
	}
	return nil
}

// ── pipe / ACL plumbing ─────────────────────────────────────────────────────

// runnerPipeNames returns two unique pipe names under \\.\pipe\. Random so a
// same-host attacker cannot pre-create a name we would then connect to.
func runnerPipeNames() (string, string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	s := hex.EncodeToString(b[:])
	return `\\.\pipe\fyc-sbx-in-` + s, `\\.\pipe\fyc-sbx-out-` + s, nil
}

// createRunnerPipe creates one server end. The DACL grants full access only to
// the sandbox account and the synthetic SID: the runner (FycSandbox token) and
// the WRITE_RESTRICTED command (synthetic SID in its restricting list) can use
// the pipe, nobody else. The creating process keeps its handle, so it needs no
// ACE of its own.
func createRunnerPipe(name string, inbound bool, userSID, synth *windows.SID) (windows.Handle, error) {
	access := uint32(rnrPipeAccessOutbound) // parent writes (pipe-in)
	if inbound {
		access = rnrPipeAccessInbound // parent reads (pipe-out)
	}
	sddl := "D:(A;;GA;;;" + userSID.String() + ")(A;;GA;;;" + synth.String() + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return 0, err
	}

	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateNamedPipe(nameP,
		access|rnrFirstPipeInstance,
		rnrPipeTypeByte|rnrPipeReadmodeByte|rnrPipeWait,
		rnrPipeUnlimited, 64*1024, 64*1024, 0, sa)
	runtime.KeepAlive(sd)
	return h, err
}

// connectPipeWithTimeout waits for the runner to open its end. ConnectNamedPipe
// blocks, so it is done overlapped with an event and a timeout (the runner may
// die before connecting, and a first logon can be slow).
func connectPipeWithTimeout(h windows.Handle, d time.Duration) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)

	ov := new(windows.Overlapped)
	ov.HEvent = ev
	// The kernel writes into ov until the I/O completes or is cancelled: keep it
	// alive (and reachable) for the whole function.
	defer runtime.KeepAlive(ov)

	err = windows.ConnectNamedPipe(h, ov)
	if err == nil || rnrIsErr(err, rnrErrPipeConnected) || rnrIsErr(err, rnrErrNoData) {
		return nil
	}
	if !rnrIsErr(err, rnrErrIOPending) {
		return err
	}
	r, werr := windows.WaitForSingleObject(ev, uint32(d/time.Millisecond))
	if werr != nil {
		_ = windows.CancelIoEx(h, ov)
		var n uint32
		_ = windows.GetOverlappedResult(h, ov, &n, true) // wait for the cancel to land
		return werr
	}
	if r == rnrWaitTimeout {
		_ = windows.CancelIoEx(h, ov)
		var n uint32
		_ = windows.GetOverlappedResult(h, ov, &n, true) // wait for the cancel to land
		return errors.New("timeout waiting for the runner to connect")
	}
	var n uint32
	if gerr := windows.GetOverlappedResult(h, ov, &n, false); gerr != nil {
		if rnrIsErr(gerr, rnrErrPipeConnected) {
			return nil
		}
		return gerr
	}
	return nil
}

// writeAllToPipe writes data, retrying short writes (a named pipe can accept
// fewer bytes than requested).
func writeAllToPipe(h windows.Handle, data []byte) error {
	for len(data) > 0 {
		var n uint32
		if err := windows.WriteFile(h, data, &n, nil); err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// ensureDedicatedUserACL is the Fase-D counterpart of
// ensureRestrictedTokenACL: the parent (owner of the RW trees) grants the
// synthetic write-lock and the sandbox user read-write access on the workspace
// root (and on the RW path roots), so the foreign identity can reach a
// workspace that is otherwise default-deny.
//
// The workspace grant is a *single* inheritable ALLOW ACE per SID on the root
// (SUB_CONTAINERS_AND_OBJECTS_INHERIT), not grantTree's per-object walk: the
// walk rewrote the DACL of every existing file on every `sh` command, so on a
// real repo (thousands of files) it dominated the command's latency. Children
// pick the ACE up through inheritance — only the root is touched — and the set
// is skipped when acesForSID already reports the ACE, so a second command on the
// same workspace rewrites nothing (idempotent).
//
// The inheritable root ACE would otherwise bleed into the masked workspace
// subtrees, so every plan.Deny path that lives inside the root (.git/.codex/
// .agents) gets an explicit DENY for both SIDs: a DENY ACE beats any ALLOW,
// inherited or not, and read+execute on a directory denies traversal too, so the
// subtree stays unreachable. Deny paths outside the root are left alone —
// nothing inherits into them.
func ensureDedicatedUserACL(userSID, synth *windows.SID, plan aclPlan) (aclSummary, error) {
	var sum aclSummary
	if plan.Root != "" {
		if err := grantRootInherited(plan.Root, synth); err != nil {
			return sum, err
		}
		if err := grantRootInherited(plan.Root, userSID); err != nil {
			return sum, err
		}
		if err := denyWorkspacePaths(plan.Root, plan.Deny, synth, userSID); err != nil {
			return sum, err
		}
		sum.Granted = append(sum.Granted, plan.Root)
	}
	for _, p := range plan.AllowRW {
		if !pathExists(p) {
			sum.Skipped = append(sum.Skipped, p)
			continue
		}
		if err := grantRootInherited(p, synth); err != nil {
			sum.Skipped = append(sum.Skipped, p)
			continue
		}
		sum.Granted = append(sum.Granted, p)
	}
	sum.Masked = append(sum.Masked, plan.Deny...)
	return sum, nil
}

// grantRootInherited plants one inheritable read-write ALLOW ACE for sid on root
// (replacing grantTree's O(files) walk) unless the object already carries an
// allow ACE for sid, in which case it is a no-op so repeated commands do not
// rewrite the DACL.
func grantRootInherited(root string, sid *windows.SID) error {
	if allow, _, err := acesForSID(root, sid); err == nil && allow {
		return nil
	}
	return setACE(root, sid, windows.GRANT_ACCESS, accessMaskReadWrite, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

// denyWorkspacePaths plants an explicit DENY (read+execute) for each SID on the
// masked paths that live inside the granted root — without it the inheritable
// root ALLOW would leak into .git/.codex/.agents. It only touches existing paths
// and is idempotent; a path outside root is skipped because nothing inherits
// into it. Best-effort: a path whose DACL cannot be read (a leftover DENY can
// leave .git unreadable even to the real user) or written is skipped instead of
// aborting the whole grant — that would take `sh` down for a hardening extra.
func denyWorkspacePaths(root string, deny []string, sids ...*windows.SID) error {
	for _, d := range deny {
		if !pathExists(d) || !pathWithin(root, d) {
			continue
		}
		for _, sid := range sids {
			if _, denyACE, err := acesForSID(d, sid); err != nil {
				continue // cannot even read the DACL: already restricted to us
			} else if denyACE {
				continue
			}
			if err := denyRead(d, sid); err != nil {
				continue // hardening only, never fail the grant on it
			}
		}
	}
	return nil
}

// pathWithin reports whether p is root itself or a descendant of it (used to
// pick the deny paths the inheritable root ACE would otherwise leak into).
func pathWithin(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// rnrLogonRun starts app+cmdline as user with LOGON_WITH_PROFILE (own profile +
// HKCU, no inherited handles) and returns the process handle and PID; the caller
// waits for and closes the handle. It is the single CreateProcessWithLogonW call
// site for starting the runner as the sandbox user.
func rnrLogonRun(user, pass, app, cmdline, cwd string) (windows.Handle, int, error) {
	userP, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return 0, 0, err
	}
	domainP, err := windows.UTF16PtrFromString(".")
	if err != nil {
		return 0, 0, err
	}
	passP, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		return 0, 0, err
	}
	appP, err := windows.UTF16PtrFromString(app)
	if err != nil {
		return 0, 0, err
	}
	cmdP, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return 0, 0, err
	}
	var cwdP *uint16
	if cwd != "" {
		if cwdP, err = windows.UTF16PtrFromString(cwd); err != nil {
			return 0, 0, err
		}
	}

	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))

	var pi windows.ProcessInformation
	r1, _, callErr := rnrCreateProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(userP)),
		uintptr(unsafe.Pointer(domainP)),
		uintptr(unsafe.Pointer(passP)),
		uintptr(rnrLogonWithProfile),
		uintptr(unsafe.Pointer(appP)),
		uintptr(unsafe.Pointer(cmdP)),
		uintptr(windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW),
		0, // lpEnvironment NULL: built from the sandbox user's profile
		uintptr(unsafe.Pointer(cwdP)),
		uintptr(unsafe.Pointer(si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if r1 == 0 {
		return 0, 0, fmt.Errorf("CreateProcessWithLogonW(%s): %w", user, callErr)
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, int(pi.ProcessId), nil
}
