//go:build windows

package tools

// Phase D — Phases 1+2: diagnostic probe for dedicated sandbox users.
//
// The question it answers: if a sandbox runs commands as a DEDICATED LOCAL USER
// (instead of imposing a restricted token on the real user's token,
// as the current engine does), do PowerShell and system TLS clients
// start working again? P5/P6 showed that the block is the user SID made deny-only,
// not the restricted token itself: this test verifies the hypothesis at the root.
//
// Model (identical to what Phase D2 will be, compressed into a test):
//   1. the test process (real user, ELEVATED) creates a test local user;
//   2. prepares a workspace under %ProgramData% (traversable by Users) and copies
//      the test binary there as a runner;
//   3. launches the runner as the test user with CreateProcessWithLogonW +
//      LOGON_WITH_PROFILE (sandbox user's own profile and HKCU);
//   4. the runner (intercepted by init() via argv) opens its OWN token, mints a
//      WRITE_RESTRICTED restricted token with restricting list
//      [Everyone, logon, synthetic] — WITHOUT disabling the user SID, which is the
//      point of Phase D — and launches the command with CreateProcessAsUser,
//      writing stdout/stderr/exit code to files that the parent reads.
//
// Interpretation (Phase 1 + Phase 2, sec. 12):
//   - PowerShell starts    -> the foreign-profile resolves gotcha 6 (P5/P6);
//   - go test remains green -> no regression on the toolchain (the reason for the
//     current engine);
//   - tlsgo (crypto/tls)  -> CONFIRMS path A of sec. 4: Go's *own* TLS
//     passes the restricted token (unlike Schannel);
//   - denyread            -> measures sec. 5: default-deny by identity on the real profile
//     and explicit DENY that overrides the broad grant;
//   - curl (Schannel)     -> remains RED by design: sec. 3/13 do NOT count it
//     as a green criterion, it's a platform limitation, not a failure.
//   Phase 2 green (tlsgo + denyread) => sec. 4 closes on A and the production
//   setup/runner can be written (~1000 LOC).
//
// Execution (from ELEVATED shell, admin — required to create a local user):
//
//	$env:FYC_SANDBOX_USERS_LIVE = "1"
//	go test -v -count=1 -run TestSandboxUsersProbe ./tools/
//
// The probe is self-cleaning: it deletes the user, their profile, and the workspace. The
// only external grant is a best-effort read on GOROOT if it's under the
// real profile (revoked at the end of the test).

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	sandboxUsersLiveEnv   = "FYC_SANDBOX_USERS_LIVE"
	sandboxProbeRunnerArg = "__fyc-probe-runner"
	sandboxProbeLogonFlag = 0x00000001 // LOGON_WITH_PROFILE

	// Phase 2 (sec. 12.2): path A of sec. 4 is measured with a NON-Schannel
	// TLS client (Go crypto/tls) under the same token. URL overridable with
	// FYC_PROBE_TLS_URL.
	sandboxProbeTLSURL = "https://www.example.com"
)

// sandboxProbeSpec is what the parent passes to the runner (JSON file in argv).
type sandboxProbeSpec struct {
	Name   string            `json:"name"`
	Cmd    string            `json:"cmd"`
	Cwd    string            `json:"cwd"`
	OutDir string            `json:"outDir"`
	Env    map[string]string `json:"env,omitempty"`
}

// Functions not exposed by x/sys/windows.
var (
	procCreateProcessWithLogonW  = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithLogonW")
	procGetUserProfileDirectoryW = windows.NewLazySystemDLL("userenv.dll").NewProc("GetUserProfileDirectoryW")
	procNetUserAdd               = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserAdd")
	procNetUserDel               = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserDel")
	procNetLocalGroupAddMembers  = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetLocalGroupAddMembers")

	// LookupAccountNameW (not exposed by x/sys): resolves the SID of the newly created
	// sandbox user, to plant the DENY of sec. 5 on its identity.
	procLookupAccountNameW = windows.NewLazySystemDLL("advapi32.dll").NewProc("LookupAccountNameW")
)

// init intercepts the runner startup: the parent re-executes THIS binary as a
// sandbox user with argv[1] == sandboxProbeRunnerArg. Runs before every test.
func init() {
	if len(os.Args) >= 2 && os.Args[1] == sandboxProbeRunnerArg {
		specPath := ""
		if len(os.Args) >= 3 {
			specPath = os.Args[2]
		}
		os.Exit(runSandboxProbeRunner(specPath))
	}
}

// ── runner (sandbox user side) ────────────────────────────────────────────

func runSandboxProbeRunner(specPath string) int {
	if specPath == "" {
		fmt.Fprintln(os.Stderr, "probe runner: missing spec path")
		return 2
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: read spec: "+err.Error())
		return 2
	}
	var spec sandboxProbeSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: parse spec: "+err.Error())
		return 2
	}

	// Everything the runner writes to stderr (failure diagnostics) ends up
	// in the diag file: the parent cannot capture stderr across the
	// CreateProcessWithLogonW boundary.
	if diag, derr := os.Create(filepath.Join(spec.OutDir, "diag-"+spec.Name+".txt")); derr == nil {
		os.Stderr = diag
		defer diag.Close()
	}

	base, err := rtOpenSelfToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: open token: "+err.Error())
		return 3
	}
	defer base.Close()

	everyone, logon, synthetic, err := rtRestrictingSIDs(base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: restricting SIDs: "+err.Error())
		return 3
	}
	// Phase D: the principal is already a foreign user, so we do NOT disable
	// their SID (unlike the current form (b) engine): we only add
	// WRITE_RESTRICTED + the restricting list.
	tok, err := rtNewRestrictedToken(base, nil, everyone, logon, synthetic)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: CreateRestrictedToken: "+err.Error())
		return 3
	}
	defer tok.Close()
	if err := rtSetTokenDefaultDacl(tok, logon, synthetic); err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: SetTokenDefaultDacl: "+err.Error())
		return 3
	}

	// The allow-list on the workspace (Users + synthetic SID) is planted by the
	// elevated PARENT: the runner runs as the sandbox user and does not have WRITE_DAC,
	// so it cannot modify DACLs.

	outPath := filepath.Join(spec.OutDir, "out-"+spec.Name+".txt")
	codePath := filepath.Join(spec.OutDir, "code-"+spec.Name+".txt")

	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: create out: "+err.Error())
		return 3
	}
	defer outFile.Close()
	_ = windows.SetHandleInformation(windows.Handle(outFile.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)

	nul, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe runner: open NUL: "+err.Error())
		return 3
	}
	defer nul.Close()
	_ = windows.SetHandleInformation(windows.Handle(nul.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)

	env := probeChildEnv(spec.Env)
	code, rerr := probeRunChild(tok, windowsShellCmdLine(spec.Cmd), spec.Cwd, env,
		windows.Handle(outFile.Fd()), windows.Handle(nul.Fd()))
	if rerr != nil {
		_ = os.WriteFile(codePath, []byte("ERR "+rerr.Error()), 0o666)
		return 4
	}
	_ = os.WriteFile(codePath, []byte(strconv.Itoa(int(code))), 0o666)
	return 0
}

// probeChildEnv builds the child's environment: the runner's environment (that
// of the sandbox user, from LOGON_WITH_PROFILE) with per-user variables
// re-derived from the token and spec overrides applied case-insensitively.
func probeChildEnv(overrides map[string]string) []string {
	merged := map[string]string{}
	for k, v := range overrides {
		merged[strings.ToUpper(k)] = v
	}
	if prof := probeProfileDir(); prof != "" {
		probeSetDefault(merged, "USERPROFILE", prof)
		probeSetDefault(merged, "APPDATA", filepath.Join(prof, "AppData", "Roaming"))
		probeSetDefault(merged, "LOCALAPPDATA", filepath.Join(prof, "AppData", "Local"))
		probeSetDefault(merged, "TEMP", filepath.Join(prof, "AppData", "Local", "Temp"))
		probeSetDefault(merged, "TMP", filepath.Join(prof, "AppData", "Local", "Temp"))
	}
	return rtEnv(merged, nil)
}

func probeSetDefault(m map[string]string, k, v string) {
	if _, ok := m[strings.ToUpper(k)]; !ok {
		m[strings.ToUpper(k)] = v
	}
}

// probeProfileDir resolves the home directory of the current process user from the token
// (the profile is that of the sandbox user, loaded via LOGON_WITH_PROFILE).
func probeProfileDir() string {
	tok, err := rtOpenSelfToken()
	if err != nil {
		return ""
	}
	defer tok.Close()
	var size uint32
	if r, _, _ := procGetUserProfileDirectoryW.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&size))); r == 0 || size == 0 {
		return ""
	}
	buf := make([]uint16, size)
	if r, _, _ := procGetUserProfileDirectoryW.Call(uintptr(tok), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// probeRunChild launches `cmd.exe /d /s /c <cmd>` under the restricted token, with
// stdout/stderr redirected to a file handle (the child is on the sandbox user side,
// like the runner, so handle inheritance works).
func probeRunChild(tok windows.Token, cmdline, cwd string, env []string, stdout, stdin windows.Handle) (uint32, error) {
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

	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(tok, nil, cmdlinePtr, nil, nil, true,
		windows.CREATE_UNICODE_ENVIRONMENT, &envBlock[0], cwdPtr, si, &pi); err != nil {
		return 0, fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// ── Phase 2 (sec. 12.2): non-Schannel TLS target and deny-read ─────────────────

// probeWriteTLSModule writes a mini stdlib-only module whose main does an HTTPS GET
// with crypto/tls — the NON-Schannel client of path A (sec. 4). Runs under
// the same token as the `gotest` target (already green): if it negotiates, Go's own TLS
// passes the restricted token while Schannel (curl/.NET) does not (sec. 3).
func probeWriteTLSModule(t *testing.T, dir string) {
	t.Helper()
	rtWriteFile(t, filepath.Join(dir, "go.mod"), "module fyctls\n\ngo 1.21\n")
	rtWriteFile(t, filepath.Join(dir, "main.go"), `package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	url := "https://www.example.com"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	c := &http.Client{
		Timeout:   25 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
	}
	resp, err := c.Get(url)
	if err != nil {
		fmt.Printf("TLS-GO-ERR %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	fmt.Printf("TLS-GO-OK status=%d tls=0x%04x\n", resp.StatusCode, resp.TLS.Version)
}
`)
}

// probeUserSID resolves the SID of a local account by name. Used to plant the
// DENY of sec. 5 on the sandbox identity (or, better, on the FycSandboxUsers group:
// in the probe it doesn't exist yet, so the user is used).
func probeUserSID(name string) (*windows.SID, error) {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var sidSize, domSize, use uint32
	// First call: buffer sizes (expected result ERROR_INSUFFICIENT_BUFFER).
	r, _, callErr := procLookupAccountNameW.Call(0, uintptr(unsafe.Pointer(nameP)), 0,
		uintptr(unsafe.Pointer(&sidSize)), 0, uintptr(unsafe.Pointer(&domSize)), uintptr(unsafe.Pointer(&use)))
	if r == 0 {
		if le, ok := callErr.(syscall.Errno); !ok || le != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, fmt.Errorf("LookupAccountNameW(%s): %w", name, callErr)
		}
	}
	if sidSize == 0 {
		return nil, fmt.Errorf("LookupAccountNameW(%s): null SID size", name)
	}
	sidBuf := make([]byte, sidSize)
	domBuf := make([]uint16, domSize)
	if r, _, callErr := procLookupAccountNameW.Call(0, uintptr(unsafe.Pointer(nameP)),
		uintptr(unsafe.Pointer(&sidBuf[0])), uintptr(unsafe.Pointer(&sidSize)),
		uintptr(unsafe.Pointer(&domBuf[0])), uintptr(unsafe.Pointer(&domSize)),
		uintptr(unsafe.Pointer(&use))); r == 0 {
		return nil, fmt.Errorf("LookupAccountNameW(%s): %w", name, callErr)
	}
	return (*windows.SID)(unsafe.Pointer(&sidBuf[0])).Copy()
}

// probeDenyReadACE plants a read-only DENY (non-inheriting) for the SID on the
// single object: this is the "leave some hidden" mechanism of sec. 5, which
// overrides the broad ALLOW on the parent because SetNamedSecurityInfo orders ACEs
// canonically (deny first).
func probeDenyReadACE(path string, sid *windows.SID) error {
	return setACE(path, sid, windows.DENY_ACCESS,
		windows.ACCESS_MASK(windows.FILE_GENERIC_READ), windows.NO_INHERITANCE)
}

// ── parent test (real user, elevated) ───────────────────────────────────

// TestSandboxUsersProbe
func TestSandboxUsersProbe(t *testing.T) {
	if os.Getenv(sandboxUsersLiveEnv) != "1" {
		t.Skip("set FYC_SANDBOX_USERS_LIVE=1 (from ELEVATED shell) for Phase D probe")
	}
	base := rtBaseToken(t)
	defer base.Close()
	if !rtIsElevated(base) {
		t.Skip("the probe creates a local user: run it from an elevated shell (admin)")
	}

	name := fmt.Sprintf("FycProbe%d", os.Getpid())
	pass, err := probeRandomPassword()
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if err := probeCreateUser(name, pass); err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	t.Logf("created local user %s", name)
	defer func() {
		if err := probeDeleteUser(name); err != nil {
			t.Logf("cleanup: delete user %s: %v", name, err)
		}
		if prof := filepath.Join(os.Getenv("SystemDrive")+`\Users`, name); pathExists(prof) {
			if err := os.RemoveAll(prof); err != nil { // best-effort: might be in use
				t.Logf("cleanup: remove profile %s: %v", prof, err)
			}
		}
	}()

	// workspace under %ProgramData%: traversable by Users (real profile is not).
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = os.Getenv("SystemDrive") + `\ProgramData`
	}
	ws := filepath.Join(programData, "fyc-sandbox-probe-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(ws, 0o777); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	// copy the test binary: the real profile is not executable by the sandbox user
	srcExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	runnerExe := filepath.Join(ws, "fyc-probe.exe")
	if err := probeCopyFile(srcExe, runnerExe); err != nil {
		t.Fatalf("copy runner: %v", err)
	}

	// Go module + cache for the `go test` target
	mod := filepath.Join(ws, "mod")
	rtWriteModule(t, mod)
	rtPrepareGoDirs(t, ws)

	// Phase 2 (sec. 12.2): fixture of the two new targets, written BEFORE the
	// grants so that grantTree covers them.
	//   - tlsgo:    stdlib-only module with crypto/tls (NON-Schannel client, path A sec. 4);
	//   - denyread: one granted file (visible.txt), one with the DENY of sec. 5
	//     (blocked.txt) and a sentinel in the REAL profile, outside any grant
	//     (default-deny by identity).
	tlsMod := filepath.Join(ws, "tlsmod")
	probeWriteTLSModule(t, tlsMod)

	wsVisible := filepath.Join(ws, "visible.txt")
	rtWriteFile(t, wsVisible, "FYC-DENY-WS-VISIBLE")
	wsBlocked := filepath.Join(ws, "blocked.txt")
	rtWriteFile(t, wsBlocked, "FYC-DENY-BLOCKED")

	realProbeDir := filepath.Join(homeDir(), fmt.Sprintf(".fyc-sandbox-probe-%d", os.Getpid()))
	if err := os.MkdirAll(realProbeDir, 0o755); err != nil {
		t.Fatalf("mkdir real profile sentinel: %v", err)
	}
	defer os.RemoveAll(realProbeDir)
	realSecret := filepath.Join(realProbeDir, "secret.txt")
	rtWriteFile(t, realSecret, "FYC-DENY-REAL-SECRET")

	// grant to Users + synthetic SID: Users is needed by the runner (sandbox user,
	// normal token) to read exe/spec and write output; the synthetic SID is
	// what makes the child's write-restricted check pass. It is the PARENT
	// (elevated, has WRITE_DAC) that plants it: the runner does not. Disposable workspace.
	usersSID, err := windows.StringToSid("S-1-5-32-545")
	if err != nil {
		t.Fatalf("Users SID: %v", err)
	}
	if err := grantTree(ws, usersSID, nil); err != nil {
		t.Fatalf("grant Users on workspace: %v", err)
	}
	// The child runs with WRITE_RESTRICTED: besides the normal check (Users) it must
	// pass the one on restricting SIDs, so we also grant the synthetic SID
	// (deterministic, inert outside the sandbox) on the tree.
	synthSID, err := rtSyntheticSID()
	if err != nil {
		t.Fatalf("synthetic SID: %v", err)
	}
	if err := grantTree(ws, synthSID, nil); err != nil {
		t.Fatalf("grant synthetic SID on workspace: %v", err)
	}

	// Sec. 5 — "leave some hidden": the explicit DENY on the sandbox user's SID
	// beats the broad ALLOW on ws (the user is in Users); the parent's SID
	// is not touched. It is planted AFTER grantTree (which would otherwise
	// cover it with an ALLOW).
	sandboxUserSID, err := probeUserSID(name)
	if err != nil {
		t.Fatalf("sandbox user SID: %v", err)
	}
	if err := probeDenyReadACE(wsBlocked, sandboxUserSID); err != nil {
		t.Fatalf("deny-read %s: %v", wsBlocked, err)
	}
	defer rtStripAll(t, sandboxUserSID, wsBlocked)

	// GOROOT: if it's under the real profile, the sandbox user cannot read it.
	// Best-effort read-only grant, revoked at the end of the test.
	goroot := rtGoEnv(ws)["GOROOT"]
	grantedGoroot := ""
	if goroot != "" && strings.HasPrefix(strings.ToLower(goroot), strings.ToLower(homeDir())) {
		if err := probeGrantReadTree(goroot, usersSID); err == nil {
			grantedGoroot = goroot
			t.Logf("GOROOT under profile: granted read to Users on %s (revoked at end of test)", goroot)
		}
	}
	defer func() {
		if grantedGoroot != "" {
			rtStripAll(t, usersSID, grantedGoroot)
		}
	}()

	tlsURL := os.Getenv("FYC_PROBE_TLS_URL")
	if tlsURL == "" {
		tlsURL = sandboxProbeTLSURL
	}
	// denyread reads three files at once and separates them with ---SEP---: the granted
	// one (must pass), the one with the DENY of sec. 5 (must fail), and the sentinel
	// in the real profile (default-deny by identity).
	denyCmd := fmt.Sprintf(`type "%s" & echo ---SEP--- & type "%s" & echo ---SEP--- & type "%s"`,
		wsVisible, wsBlocked, realSecret)

	type target struct {
		name string
		cmd  string
		cwd  string
		env  map[string]string
	}
	targets := []target{
		{"powershell", `powershell -NoProfile -NonInteractive -Command "Write-Output FASE-D-PS-OK"`, ws, nil},
		{"curlv", `curl -V`, ws, nil},
		{"curl", `curl -sS --max-time 25 -o curl-body.txt ` + tlsURL, ws, nil},
		{"tlsgo", `go run . ` + tlsURL, tlsMod, rtGoEnv(ws)},
		{"gotest", `go test ./...`, mod, rtGoEnv(ws)},
		{"denyread", denyCmd, ws, nil},
	}

	for _, tg := range targets {
		tg := tg
		t.Run(tg.name, func(t *testing.T) {
			out, code, err := probeRunTarget(t, name, pass, runnerExe, ws, sandboxProbeSpec{
				Name: tg.name, Cmd: tg.cmd, Cwd: tg.cwd, OutDir: ws, Env: tg.env,
			})
			if err != nil {
				t.Fatalf("execution as sandbox user: %v", err)
			}
			t.Logf("cmd=%q exit=%d\n%s", tg.cmd, code, out)
			switch tg.name {
			case "powershell":
				if !strings.Contains(out, "FASE-D-PS-OK") {
					t.Errorf("PowerShell did NOT start under the sandbox user (exit %d)", code)
				}
			case "curlv":
				low := strings.ToLower(out)
				switch {
				case strings.Contains(low, "schannel"):
					t.Logf("curlv: Schannel backend — system TLS remains broken under restricted token (sec. 3), expected")
				case strings.Contains(low, "openssl"), strings.Contains(low, "libressl"):
					t.Logf("curlv: non-Schannel backend — path A of sec. 4 is verifiable also via curl")
				default:
					t.Logf("curlv: unrecognized backend:\n%s", out)
				}
			case "curl":
				low := strings.ToLower(out)
				if strings.Contains(out, "SEC_E_NO_CREDENTIALS") || strings.Contains(low, "acquirecredentialshandle") {
					// Expected on Windows (system curl = Schannel). Sec. 13: NOT a
					// green criterion, it's the platform limitation of sec. 3.
					t.Logf("curl: TLS Schannel not available under restricted token (expected, sec. 3)")
				} else if code != 0 {
					t.Logf("curl: inconclusive result (network absent?), exit %d: %s", code, out)
				} else {
					t.Logf("curl: TLS negotiated (exit 0)")
				}
			case "tlsgo":
				if strings.Contains(out, "TLS-GO-OK") {
					t.Logf("sec. 4 path A confirmed: crypto/tls negotiates under sandbox user + restricted token")
				} else if code != 0 {
					low := strings.ToLower(out)
					netDown := strings.Contains(low, "no such host") || strings.Contains(low, "timeout") ||
						strings.Contains(low, "connection refused") || strings.Contains(low, "network is unreachable")
					if netDown {
						t.Logf("tlsgo: network absent (not a TLS verdict): %s", out)
					} else {
						t.Errorf("sec. 4 path A NOT confirmed: crypto/tls failed under restricted token (exit %d): %s", code, out)
					}
				}
			case "gotest":
				if code != 0 {
					t.Errorf("go test did NOT run under the sandbox user (exit %d)", code)
				}
			case "denyread":
				parts := strings.Split(out, "---SEP---")
				part := func(i int) string {
					if i < len(parts) {
						return parts[i]
					}
					return ""
				}
				wsOK := strings.Contains(part(0), "FYC-DENY-WS-VISIBLE")
				blockedLeak := strings.Contains(part(1), "FYC-DENY-BLOCKED")
				realLeak := strings.Contains(part(2), "FYC-DENY-REAL-SECRET")
				t.Logf("denyread: ws-visible=%v blocked-leak=%v real-profile-leak=%v", wsOK, blockedLeak, realLeak)
				if !wsOK {
					t.Errorf("denyread: the granted workspace file must remain readable: %q", part(0))
				}
				if blockedLeak {
					t.Errorf("sec. 5: the explicit DENY on the user SID did NOT deny read: %q", part(1))
				}
				if realLeak {
					t.Errorf("sec. 5: the REAL profile is readable by the sandbox user — default-deny by identity failed: %q", part(2))
				}
			}
		})
	}
}

// probeRunTarget writes the spec, launches the runner as sandbox user, waits and
// reads output and exit code.
func probeRunTarget(t *testing.T, name, pass, exe, ws string, spec sandboxProbeSpec) (string, int, error) {
	t.Helper()
	specPath := filepath.Join(ws, "spec-"+spec.Name+".json")
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(specPath, raw, 0o666); err != nil {
		return "", 0, err
	}
	outPath := filepath.Join(ws, "out-"+spec.Name+".txt")
	codePath := filepath.Join(ws, "code-"+spec.Name+".txt")
	_ = os.Remove(outPath)
	_ = os.Remove(codePath)
	_ = os.Remove(filepath.Join(ws, "diag-"+spec.Name+".txt"))

	start := time.Now()
	rc, err := probeRunAsUser(name, pass, exe, specPath, ws)
	t.Logf("runner exit=%d in %s (includes profile loading on first logon)",
		rc, time.Since(start).Round(time.Millisecond))
	if err != nil {
		return "", 0, err
	}

	var out string
	if b, rerr := os.ReadFile(outPath); rerr == nil {
		out = string(b)
	}
	code := -1
	if b, rerr := os.ReadFile(codePath); rerr == nil {
		s := strings.TrimSpace(string(b))
		if n, perr := strconv.Atoi(s); perr == nil {
			code = n
		} else {
			out += "\n[runner] " + s
		}
	} else {
		out += "\n[runner] no exit code file (runner crashed?)"
	}
	if b, rerr := os.ReadFile(filepath.Join(ws, "diag-"+spec.Name+".txt")); rerr == nil {
		out += "\n[runner stderr]\n" + string(b)
	}
	return out, code, nil
}

// probeRunAsUser launches `exe runnerArg specPath` as local user `name`,
// loading its profile (LOGON_WITH_PROFILE). lpEnvironment = NULL: the child's
// environment should be created from the sandbox user's profile.
func probeRunAsUser(name, pass, exe, specPath, cwd string) (uint32, error) {
	userP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	domainP, err := windows.UTF16PtrFromString(".")
	if err != nil {
		return 0, err
	}
	passP, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		return 0, err
	}
	appP, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	cmdP, err := windows.UTF16PtrFromString(fmt.Sprintf(`"%s" %s "%s"`, exe, sandboxProbeRunnerArg, specPath))
	if err != nil {
		return 0, err
	}
	cwdP, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return 0, err
	}

	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation
	r1, _, callErr := procCreateProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(userP)),
		uintptr(unsafe.Pointer(domainP)),
		uintptr(unsafe.Pointer(passP)),
		uintptr(sandboxProbeLogonFlag),
		uintptr(unsafe.Pointer(appP)),
		uintptr(unsafe.Pointer(cmdP)),
		uintptr(windows.CREATE_UNICODE_ENVIRONMENT),
		0, // environment NULL -> created from the sandbox user's profile
		uintptr(unsafe.Pointer(cwdP)),
		uintptr(unsafe.Pointer(si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("CreateProcessWithLogonW: %w", callErr)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	// The first logon creates the profile: it can be slow (minutes, cf. Codex issue
	// #34062). Generous timeout.
	if _, err := windows.WaitForSingleObject(pi.Process, 300000); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// ── local user management (netapi32) ───────────────────────────────────────

// netUserInfo1 mirrors USER_INFO_1 from netapi32.
type netUserInfo1 struct {
	Name        *uint16
	Password    *uint16
	PasswordAge uint32
	Privilege   uint32
	HomeDir     *uint16
	Comment     *uint16
	Flags       uint32
	ScriptPath  *uint16
}

func probeCreateUser(name, pass string) error {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	passP, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		return err
	}
	const (
		userPrivUser = 1      // USER_PRIV_USER
		ufScript     = 0x0001 // UF_SCRIPT
	)
	ui := netUserInfo1{Name: nameP, Password: passP, Privilege: userPrivUser, Flags: ufScript}
	var parmErr uint32
	if r, _, _ := procNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&ui)), uintptr(unsafe.Pointer(&parmErr))); r != 0 {
		return fmt.Errorf("NetUserAdd: %w", syscall.Errno(r))
	}
	// Interactive logon requires the "Allow log on locally" right, which the
	// Users group has: add the user. The group name is localized, so
	// it is resolved from the well-known SID (no hard-coded strings).
	groupName, err := probeWellKnownName("S-1-5-32-545")
	if err != nil {
		return err
	}
	member := struct{ domainAndName *uint16 }{nameP} // LOCALGROUP_MEMBERS_INFO_3
	if r, _, _ := procNetLocalGroupAddMembers.Call(0, uintptr(unsafe.Pointer(groupName)), 3,
		uintptr(unsafe.Pointer(&member)), 1); r != 0 {
		return fmt.Errorf("NetLocalGroupAddMembers(Users): %w", syscall.Errno(r))
	}
	return nil
}

func probeDeleteUser(name string) error {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	if r, _, _ := procNetUserDel.Call(0, uintptr(unsafe.Pointer(nameP))); r != 0 {
		return fmt.Errorf("NetUserDel: %w", syscall.Errno(r))
	}
	return nil
}

// probeWellKnownName resolves the localized name of a SID (e.g. BUILTIN\Users).
func probeWellKnownName(sidStr string) (*uint16, error) {
	sid, err := windows.StringToSid(sidStr)
	if err != nil {
		return nil, err
	}
	name := make([]uint16, 256)
	dom := make([]uint16, 256)
	nameLen, domLen := uint32(len(name)), uint32(len(dom))
	var use uint32
	if err := windows.LookupAccountSid(nil, sid, &name[0], &nameLen, &dom[0], &domLen, &use); err != nil {
		return nil, fmt.Errorf("LookupAccountSid(%s): %w", sidStr, err)
	}
	return &name[0], nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func probeRandomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789!@#$%^&*"
	out := make([]byte, 20)
	for i := range out {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out), nil
}

func probeCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// probeGrantReadTree grants read+execute (inheritable) to sid on root,
// walking recursively: inheritance is not retroactive.
func probeGrantReadTree(root string, sid *windows.SID) error {
	mask := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
	return filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort
		}
		_ = setACE(p, sid, windows.GRANT_ACCESS, mask, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
		return nil
	})
}
