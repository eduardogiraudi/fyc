//go:build windows

package tools

// Fase 0 probes for the restricted-token sandbox engine (RESTRICTED-TOKEN.md §5).
//
// Gated live tests (opt-in with FYC_RESTRICTED_TOKEN_LIVE=1) that measure, on the
// real host, what the design hinges on:
//
//	P1  CreateRestrictedToken + CreateProcessAsUser start WITHOUT elevation?
//	P2  Does the Go toolchain (`go test ./...`) run INSIDE the restricted-token
//	    process — the exact workload the AppContainer cannot run (§1)?
//	P3a Deny-list (explicit DENY ACEs): are sensitive paths unreadable, and are
//	    writes confined to the granted roots? (form (a), §4)
//	P3b Allow-list (user SID disabled + active SIDs re-granted): are sensitive
//	    paths unreadable by default *and* does P2 stay green? (form (b), §4)
//
// The probe-local token plumbing (rtProbeNewToken/rtProbeSetDefaultDacl plus the
// diagnostic rtRawRestrictedToken) reuses the production token/SID helpers from
// restricted_token_windows.go (rtOpenSelfToken/rtUserSID/rtLogonSID/
// rtEveryoneSID/rtSyntheticSID/rtRestrictingSIDs/rtSAA) instead of duplicating
// them — that duplication is what broke `go test ./...` once the engine landed.
// The ACL primitives (setACE/stripSID/grantTree/acesForSID, accessMaskReadWrite)
// and the shell/env helpers (windowsShellCmdLine/buildEnvBlock/filteredEnv) are
// reused as-is, exactly as RESTRICTED-TOKEN.md section 3 predicts.
//
// The facts measured on Windows 11 (17/09/2026) with a non-elevated shell — they
// shape this file and the engine it probes:
//
//   - CreateRestrictedToken + CreateProcessAsUser run without elevation (P1), and
//     the Go toolchain runs inside the restricted token (P2);
//   - restricting SIDs must be passed with Attributes = 0; SE_GROUP_ENABLED makes
//     CreateRestrictedToken fail with ERROR_INVALID_PARAMETER (0x57);
//   - a WRITE_RESTRICTED child must not *allocate* a console: CREATE_NO_WINDOW
//     and CREATE_NEW_CONSOLE die with 0xC0000142 (STATUS_DLL_INIT_FAILED); inherit
//     a console (or attach a ConPTY) instead;
//   - anonymous pipes require the restricting list to contain a SID that is also
//     in the token's Default DACL. Setting the token Default DACL to
//     [logon, synthetic] does it: logon satisfies the normal check, the synthetic
//     lock the write check, so the sandbox can use the pipes `go` creates without
//     granting the user or SYSTEM on pre-existing files;
//   - with the user SID disabled plus that Default DACL fix, form (b) is green:
//     user-private files are default-deny on read, the toolchain builds and tests
//     inside the sandbox, and the host keeps its access. Form (a) is not: a DENY
//     on the sandbox lock does not deny reads, and a DENY on the user SID also
//     denies the host.
//
// Run (must be OUTSIDE the fyc AppContainer: `go build` cannot run inside it,
// see RESTRICTED-TOKEN.md §1):
//
//	FYC_RESTRICTED_TOKEN_LIVE=1 go test -v -run TestRestrictedToken ./tools/
//
// A red P3a is a *finding*, not a broken probe: see the comment on
// TestRestrictedTokenP3aDenyList.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const restrictedTokenLiveEnv = "FYC_RESTRICTED_TOKEN_LIVE"

func rtRequireLive(t *testing.T) {
	t.Helper()
	if os.Getenv(restrictedTokenLiveEnv) != "1" {
		t.Skip("set FYC_RESTRICTED_TOKEN_LIVE=1 to run the restricted-token probes")
	}
}

// ── restricted token ────────────────────────────────────────────────────────

// CreateRestrictedToken flags (winnt.h): rtDisableMaxPrivilege/rtWriteRestricted
// now live in restricted_token_windows.go, and the probes reuse them from there.

var rtCreateRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

// rtTokenOptions is the shape of the restricted token each probe needs.
type rtTokenOptions struct {
	disableMaxPrivilege bool           // DISABLE_MAX_PRIVILEGE
	writeRestricted     bool           // WRITE_RESTRICTED
	disableUserSID      bool           // SidsToDisable = {user SID}  (form (b))
	restrictingSIDs     []*windows.SID // SidsToRestrict (the "locks")
}

// The token/SID plumbing (rtOpenSelfToken, rtUserSID, rtLogonSID, rtEveryoneSID,
// rtSyntheticSID, rtSAA) now lives in restricted_token_windows.go: the probes
// reuse it from production instead of duplicating it (that duplication is what
// broke `go test ./...` once the engine landed).

// rtProbeLocks is production's rtRestrictingSIDs flattened to the
// [Everyone, logon, synthetic] slice the probes pass around.
func rtProbeLocks(base windows.Token) ([]*windows.SID, error) {
	world, logon, synth, err := rtRestrictingSIDs(base)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{world, logon, synth}, nil
}

// rtProbeSetDefaultDacl replaces the token's Default DACL: objects the sandbox
// creates (anonymous pipes, events…, which is what `go` needs to talk to its
// tools) then carry an ACE for each listed SID, so a write-restricted token can
// still use its own IPC objects. Listing only the synthetic lock — never the
// user or SYSTEM — keeps pre-existing user files outside the write scope.
// (Production's rtSetTokenDefaultDacl is the fixed form-(b) two-SID version;
// the probes need an arbitrary list.)
func rtProbeSetDefaultDacl(t *testing.T, tok windows.Token, sids ...*windows.SID) {
	t.Helper()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, s := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(s),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries: %v", err)
	}
	dacl := struct{ DefaultDacl *windows.ACL }{acl}
	if err := windows.SetTokenInformation(tok, windows.TokenDefaultDacl,
		(*byte)(unsafe.Pointer(&dacl)), uint32(unsafe.Sizeof(dacl))); err != nil {
		t.Fatalf("SetTokenInformation(TokenDefaultDacl): %v", err)
	}
}

// rtProbeNewToken wraps CreateRestrictedToken with the probe's option struct
// (production's rtNewRestrictedToken is the fixed form-(b) factory). The base
// token must have been opened with TOKEN_DUPLICATE; the returned token must be
// closed.
func rtProbeNewToken(base windows.Token, opts rtTokenOptions) (windows.Token, error) {
	var flags uint32
	if opts.disableMaxPrivilege {
		flags |= rtDisableMaxPrivilege
	}
	if opts.writeRestricted {
		flags |= rtWriteRestricted
	}
	var disable []*windows.SID
	if opts.disableUserSID {
		sid, err := rtUserSID(base)
		if err != nil {
			return 0, err
		}
		disable = append(disable, sid)
	}
	return rtRawRestrictedToken(base, flags, disable, rtSAA(opts.restrictingSIDs...))
}

// rtRawRestrictedToken is the low-level CreateRestrictedToken call (also used by
// the diagnostic matrix, which needs to vary the restricting SIDs directly).
func rtRawRestrictedToken(base windows.Token, flags uint32, disable []*windows.SID, restrict []windows.SIDAndAttributes) (windows.Token, error) {
	disableBuf := make([]windows.SIDAndAttributes, len(disable))
	for i, s := range disable {
		disableBuf[i] = windows.SIDAndAttributes{Sid: s}
	}
	var disablePtr, restrictPtr unsafe.Pointer
	if len(disableBuf) > 0 {
		disablePtr = unsafe.Pointer(&disableBuf[0])
	}
	if len(restrict) > 0 {
		restrictPtr = unsafe.Pointer(&restrict[0])
	}

	var out windows.Token
	r1, _, callErr := rtCreateRestrictedToken.Call(
		uintptr(base),
		uintptr(flags),
		uintptr(len(disableBuf)), uintptr(disablePtr),
		0, 0,
		uintptr(len(restrict)), uintptr(restrictPtr),
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(disableBuf)
	runtime.KeepAlive(restrict)
	if r1 == 0 {
		return 0, fmt.Errorf("CreateRestrictedToken(flags=0x%02x disable=%d restrict=%d): %w",
			flags, len(disableBuf), len(restrict), callErr)
	}
	return out, nil
}

// rtIsElevated reports whether the token is (presently) elevated. Ctx for P1:
// the "no admin in setup" claim only means something from a medium-IL process.
func rtIsElevated(tok windows.Token) bool {
	var elevated uint32
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &n); err != nil {
		return false
	}
	return elevated != 0
}

// ── child process ───────────────────────────────────────────────────────────

// rtRun starts `cmd.exe /d /s /c <cmdStr>` with the given restricted token via
// CreateProcessAsUser and returns the combined stdout+stderr and the exit code.
//
// Unlike the AppContainer path (which needs STARTUPINFOEX + an attribute list),
// a restricted token is an ordinary token, so a plain STARTUPINFO with
// STARTF_USESTDHANDLES is enough — and NUL, the console and `dir` work again
// (that is the whole point of RESTRICTED-TOKEN.md).
// rtRun is the common case: the child attaches to the caller's console and its
// std stream is the caller's pipe. Attaching (flags 0), not DETACHED_PROCESS: a
// WRITE_RESTRICTED token cannot *allocate* a console (CREATE_NO_WINDOW and
// CREATE_NEW_CONSOLE die with 0xC0000142), and DETACHED leaves every
// console-subsystem descendant to allocate its own — which pops console windows.
// Attaching to the console the caller already has needs no allocation.
func rtRun(t *testing.T, tok windows.Token, cmdStr, cwd string, env []string) (string, int, error) {
	return rtRunFlags(t, tok, cmdStr, cwd, env, 0)
}

// rtRunFlags is rtRun with explicit process-creation flags, so the startup
// diagnostic can try DETACHED_PROCESS / CREATE_NEW_CONSOLE / inheriting the
// parent console.
func rtRunFlags(t *testing.T, tok windows.Token, cmdStr, cwd string, env []string, creationFlags uint32) (string, int, error) {
	t.Helper()

	sa := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	var rd, wr windows.Handle
	if err := windows.CreatePipe(&rd, &wr, sa, 0); err != nil {
		return "", 0, fmt.Errorf("CreatePipe: %w", err)
	}
	defer windows.CloseHandle(rd)
	// The read end must not travel into the child, or we never see EOF.
	if err := windows.SetHandleInformation(rd, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(wr)
		return "", 0, fmt.Errorf("SetHandleInformation: %w", err)
	}

	// stdin from the NUL device: unreachable inside the AppContainer, fine here.
	nul, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		windows.CloseHandle(wr)
		return "", 0, fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer nul.Close()
	nulHandle := windows.Handle(nul.Fd())
	_ = windows.SetHandleInformation(nulHandle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)

	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = nulHandle
	si.StdOutput = wr
	si.StdErr = wr

	cmdline, err := windows.UTF16PtrFromString(windowsShellCmdLine(cmdStr))
	if err != nil {
		windows.CloseHandle(wr)
		return "", 0, err
	}
	var cwdPtr *uint16
	if cwd != "" {
		if cwdPtr, err = windows.UTF16PtrFromString(cwd); err != nil {
			windows.CloseHandle(wr)
			return "", 0, err
		}
	}
	envBlock, err := buildEnvBlock(env)
	if err != nil {
		windows.CloseHandle(wr)
		return "", 0, err
	}

	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT) | creationFlags
	var pi windows.ProcessInformation
	callErr := windows.CreateProcessAsUser(tok, nil, cmdline, nil, nil, true, flags,
		&envBlock[0], cwdPtr, si, &pi)
	// Close our copy of the write end *before* waiting for EOF, in every case.
	windows.CloseHandle(wr)
	if callErr != nil {
		return "", 0, fmt.Errorf("CreateProcessAsUser: %w", callErr)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)

	var out []byte
	buf := make([]byte, 32*1024)
	for {
		var n uint32
		if err := windows.ReadFile(rd, buf, &n, nil); err != nil {
			break // ERROR_BROKEN_PIPE once the child (and its children) are gone
		}
		if n == 0 {
			break
		}
		out = append(out, buf[:n]...)
	}

	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return string(out), 0, fmt.Errorf("WaitForSingleObject: %w", err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return string(out), 0, fmt.Errorf("GetExitCodeProcess: %w", err)
	}
	return normalizeSniffOutput(string(out)), int(code), nil
}

// ── environment ─────────────────────────────────────────────────────────────

// rtEnv builds the child environment: os.Environ() minus denyKeys, with
// overrides applied case-insensitively (Windows env names are not case-aware).
func rtEnv(overrides map[string]string, deny []string) []string {
	base := filteredEnv(deny)
	out := make([]string, 0, len(base)+len(overrides))
	remaining := make(map[string]string, len(overrides))
	for k, v := range overrides {
		remaining[strings.ToUpper(k)] = v
	}
	for _, e := range base {
		if strings.HasPrefix(e, "=") { // drive-relative vars (=C:=…): keep verbatim
			out = append(out, e)
			continue
		}
		i := strings.IndexByte(e, '=')
		if i < 0 {
			out = append(out, e)
			continue
		}
		up := strings.ToUpper(e[:i])
		if v, ok := remaining[up]; ok {
			out = append(out, e[:i]+"="+v)
			delete(remaining, up)
			continue
		}
		out = append(out, e)
	}
	for k, v := range remaining {
		out = append(out, k+"="+v)
	}
	return out
}

func rtProbeRoot(ws string) string { return filepath.Join(ws, ".fyc-rt") }

// rtPrepareGoDirs creates the writable caches the toolchain needs, all inside
// the workspace so a single grant covers them.
func rtPrepareGoDirs(t *testing.T, ws string) {
	t.Helper()
	root := rtProbeRoot(ws)
	for _, d := range []string{"gocache", "gotmp", "gopath", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}

// rtGoEnv pins a hermetic toolchain run: no network, no user config file, no
// telemetry, local toolchain, caches inside the workspace.
func rtGoEnv(ws string) map[string]string {
	root := rtProbeRoot(ws)
	path := os.Getenv("PATH")
	m := map[string]string{
		"GO111MODULE": "on",
		"GOPROXY":     "off",
		"GOFLAGS":     "-mod=mod",
		"GOTOOLCHAIN": "local",
		"CGO_ENABLED": "0",
		"GOENV":       "off",
		"GOTELEMETRY": "off",
		"GOCACHE":     filepath.Join(root, "gocache"),
		"GOTMPDIR":    filepath.Join(root, "gotmp"),
		"GOPATH":      filepath.Join(root, "gopath"),
		"TEMP":        filepath.Join(root, "tmp"),
		"TMP":         filepath.Join(root, "tmp"),
		"PATH":        path,
	}
	if gr := runtime.GOROOT(); gr != "" {
		m["GOROOT"] = gr
		m["PATH"] = filepath.Join(gr, "bin") + string(os.PathListSeparator) + path
	}
	return m
}

// rtWriteModule lays down a dependency-free module (stdlib only) so `go test`
// needs neither the network nor the module cache.
func rtWriteModule(t *testing.T, dir string) {
	t.Helper()
	rtWriteFile(t, filepath.Join(dir, "go.mod"), "module fycprobe\n\ngo 1.21\n")
	rtWriteFile(t, filepath.Join(dir, "lib.go"),
		"package fycprobe\n\nfunc Add(a, b int) int { return a + b }\n")
	rtWriteFile(t, filepath.Join(dir, "lib_test.go"),
		"package fycprobe\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"1+2 != 3\")\n\t}\n}\n")
}

// ── ACL helpers (thin wrappers over the production primitives) ───────────────

func rtGrantTree(t *testing.T, sid *windows.SID, root string) {
	t.Helper()
	if err := grantTree(root, sid, nil); err != nil {
		t.Fatalf("grantTree %s: %v", root, err)
	}
}

func rtDenyRead(t *testing.T, sid *windows.SID, path string) {
	t.Helper()
	if !pathExists(path) {
		return
	}
	if err := setACE(path, sid, windows.DENY_ACCESS, windows.ACCESS_MASK(windows.FILE_GENERIC_READ),
		windows.NO_INHERITANCE); err != nil {
		t.Fatalf("deny-read %s: %v", path, err)
	}
}

// rtStripAll removes every ACE for sid from each path (recursively), leaving the
// host filesystem as it was.
func rtStripAll(t *testing.T, sid *windows.SID, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if !pathExists(p) {
			continue
		}
		_ = filepath.WalkDir(p, func(sub string, _ os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			_, _ = stripSID(sub, sid)
			return nil
		})
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

func rtWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func rtSIDStr(sid *windows.SID, err error) string {
	if err != nil {
		return "<err:" + err.Error() + ">"
	}
	return sid.String()
}

func rtLogContext(t *testing.T, tok windows.Token) {
	t.Helper()
	u, uerr := rtUserSID(tok)
	l, lerr := rtLogonSID(tok)
	t.Logf("token context: elevated=%v user=%s logon=%s",
		rtIsElevated(tok), rtSIDStr(u, uerr), rtSIDStr(l, lerr))
	if rtIsElevated(tok) {
		t.Logf("WARNING: this process is elevated; P1's \"no elevation\" claim is only meaningful from a medium-IL process")
	}
}

// rtHomeProbe creates (and returns) a throwaway directory under the user's home
// — outside any granted root, so it doubles as the default-deny / confinement
// target. Callers must defer os.RemoveAll on it.
func rtHomeProbe(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(homeDir(), ".fyc-rt-probe-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func rtBaseToken(t *testing.T) windows.Token {
	t.Helper()
	tok, err := rtOpenSelfToken()
	if err != nil {
		t.Fatalf("open own token: %v", err)
	}
	return tok
}

// TestRestrictedTokenDiagStartup bisects why a WRITE_RESTRICTED child dies at
// startup with 0xC0000142 (STATUS_DLL_INIT_FAILED): it varies the restricting
// list and the process-creation flags. Informational, no assertions.
func TestRestrictedTokenDiagStartup(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	world, err := rtEveryoneSID()
	if err != nil {
		t.Fatalf("world SID: %v", err)
	}
	logon, err := rtLogonSID(base)
	if err != nil {
		t.Fatalf("logon SID: %v", err)
	}
	synth, err := rtSyntheticSID()
	if err != nil {
		t.Fatalf("synthetic SID: %v", err)
	}
	user, err := rtUserSID(base)
	if err != nil {
		t.Fatalf("user SID: %v", err)
	}
	interactive, err := windows.StringToSid("S-1-5-4")
	if err != nil {
		t.Fatalf("INTERACTIVE SID: %v", err)
	}
	authenticated, err := windows.StringToSid("S-1-5-11")
	if err != nil {
		t.Fatalf("Authenticated Users SID: %v", err)
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatalf("SYSTEM SID: %v", err)
	}
	users, err := windows.StringToSid("S-1-5-32-545")
	if err != nil {
		t.Fatalf("Users SID: %v", err)
	}
	env := rtEnv(nil, []string{restrictedTokenLiveEnv})

	run := func(name string, restrict []windows.SIDAndAttributes, flags uint32) {
		tok, err := rtRawRestrictedToken(base, rtDisableMaxPrivilege|rtWriteRestricted, nil, restrict)
		if err != nil {
			t.Logf("%-42s -> create ERR %v", name, err)
			return
		}
		defer tok.Close()
		out, code, runErr := rtRunFlags(t, tok, `echo DIAG-OK`, "", env, flags)
		if runErr != nil {
			t.Logf("%-42s -> run ERR %v", name, runErr)
			return
		}
		t.Logf("%-42s -> exit=0x%08x out=%q", name, uint32(code), out)
	}

	const (
		createNoWindow  = 0x08000000 // CREATE_NO_WINDOW
		detachedProcess = 0x00000008 // DETACHED_PROCESS
	)

	run("trio + CREATE_NO_WINDOW", rtSAA(world, logon, synth), createNoWindow)
	run("trio + DETACHED_PROCESS", rtSAA(world, logon, synth), detachedProcess)
	// CREATE_NEW_CONSOLE is deliberately not probed any more: it pops a console
	// window and still dies with 0xC0000142.
	run("trio + no flags (inherit console)", rtSAA(world, logon, synth), 0)
	run("[user] + CREATE_NO_WINDOW", rtSAA(user), createNoWindow)
	run("[user,trio] + CREATE_NO_WINDOW", rtSAA(user, world, logon, synth), createNoWindow)
	run("[Everyone,Auth,logon,interactive,synth]",
		rtSAA(world, authenticated, logon, interactive, synth), createNoWindow)
	// Pipe probes: `go` and cmd talk over anonymous pipes. Which restricting list
	// lets a pipe be created tells us whether form (b) can run the toolchain.
	pipeLists := []struct {
		name string
		sids []*windows.SID
	}{
		{"[synth]", []*windows.SID{synth}},
		{"[logon]", []*windows.SID{logon}},
		{"[Everyone]", []*windows.SID{world}},
		{"[Everyone,logon,synth]", []*windows.SID{world, logon, synth}},
		{"[user]", []*windows.SID{user}},
		{"[user,trio]", []*windows.SID{user, world, logon, synth}},
		{"[system]", []*windows.SID{system}},
		{"[authenticated]", []*windows.SID{authenticated}},
		{"[interactive]", []*windows.SID{interactive}},
		{"[Users]", []*windows.SID{users}},
		{"[system,trio]", []*windows.SID{system, world, logon, synth}},
	}
	for _, pl := range pipeLists {
		tok, err := rtRawRestrictedToken(base, rtDisableMaxPrivilege|rtWriteRestricted, nil, rtSAA(pl.sids...))
		if err != nil {
			t.Logf("pipe %-24s -> create ERR %v", pl.name, err)
			continue
		}
		out, code, runErr := rtRunFlags(t, tok, `echo hello | findstr hello`, "", env, 0)
		tok.Close()
		if runErr != nil {
			t.Logf("pipe %-24s -> run ERR %v", pl.name, runErr)
			continue
		}
		t.Logf("pipe %-24s -> exit=0x%08x out=%q", pl.name, uint32(code), out)
	}

	// Default-DACL fix: put the synthetic lock in the token's Default DACL so the
	// pipes `go` creates carry a SID the restricting check accepts.
	for _, disableUser := range []bool{false, true} {
		var dis []*windows.SID
		if disableUser {
			dis = []*windows.SID{user}
		}
		tok, err := rtRawRestrictedToken(base, rtDisableMaxPrivilege|rtWriteRestricted, dis, rtSAA(world, logon, synth))
		if err != nil {
			t.Logf("daclsdk(disableUser=%v)     -> create ERR %v", disableUser, err)
			continue
		}
		if disableUser {
			rtProbeSetDefaultDacl(t, tok, logon, synth)
		} else {
			rtProbeSetDefaultDacl(t, tok, user, system, synth)
		}
		out, code, runErr := rtRunFlags(t, tok, `echo hello | findstr hello`, "", env, 0)
		tok.Close()
		if runErr != nil {
			t.Logf("daclsdk(disableUser=%v)     -> run ERR %v", disableUser, runErr)
			continue
		}
		t.Logf("daclsdk(disableUser=%v)     -> exit=0x%08x out=%q", disableUser, uint32(code), out)
	}
}

// ── P1 ──────────────────────────────────────────────────────────────────────

// TestRestrictedTokenP1CreateProcess answers P1: does CreateRestrictedToken +
// CreateProcessAsUser start without elevation? Exit code and child output must
// travel back.
func TestRestrictedTokenP1CreateProcess(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	rtLogContext(t, base)

	tok, err := rtProbeNewToken(base, rtTokenOptions{disableMaxPrivilege: true})
	if err != nil {
		t.Fatalf("CreateRestrictedToken: %v", err)
	}
	defer tok.Close()

	env := rtEnv(nil, []string{restrictedTokenLiveEnv})

	out, code, err := rtRun(t, tok, `echo P1-OK`, "", env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (echo): %v", err)
	}
	t.Logf("P1 echo: exit=%d out=%q", code, out)
	if code != 0 {
		t.Errorf("P1 exit code = %d, want 0 (out=%q)", code, out)
	}
	if !strings.Contains(out, "P1-OK") {
		t.Errorf("P1 child output missing P1-OK: %q", out)
	}

	// The two idioms the AppContainer could not run: NUL redirection and `dir`.
	out, code, err = rtRun(t, tok, `echo x>nul & echo NUL-OK & dir /b >nul & echo DIR-OK`, "", env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (nul/dir): %v", err)
	}
	t.Logf("P1 nul/dir: exit=%d out=%q", code, out)
	for _, want := range []string{"NUL-OK", "DIR-OK"} {
		if !strings.Contains(out, want) {
			t.Errorf("P1 %s missing (device isolation should be gone): %q", want, out)
		}
	}

	out, code, err = rtRun(t, tok, `exit /b 7`, "", env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (exit): %v", err)
	}
	t.Logf("P1 exit-code: exit=%d out=%q", code, out)
	if code != 7 {
		t.Errorf("P1 exit code = %d, want 7", code)
	}
}

// ── P2 ──────────────────────────────────────────────────────────────────────

// TestRestrictedTokenP2Toolchain answers P2: with the restricted token, does
// the Go toolchain build/test a module *inside* the process? (The AppContainer
// fails here with `open NUL: Accesso negato`, RESTRICTED-TOKEN.md §1.)
func TestRestrictedTokenP2Toolchain(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()

	tok, err := rtProbeNewToken(base, rtTokenOptions{disableMaxPrivilege: true})
	if err != nil {
		t.Fatalf("CreateRestrictedToken: %v", err)
	}
	defer tok.Close()

	ws := t.TempDir()
	mod := filepath.Join(ws, "mod")
	rtWriteModule(t, mod)
	rtPrepareGoDirs(t, ws)

	env := rtEnv(rtGoEnv(ws), []string{restrictedTokenLiveEnv})
	out, code, err := rtRun(t, tok, `go test ./...`, mod, env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (go test): %v", err)
	}
	t.Logf("P2 go test: exit=%d\n%s", code, out)
	if code != 0 {
		t.Errorf("P2: `go test ./...` failed inside the restricted token (exit %d)", code)
	}
	if !strings.Contains(out, "ok") && !strings.Contains(out, "PASS") {
		t.Errorf("P2: unexpected `go test` output:\n%s", out)
	}
}

// ── P3a ─────────────────────────────────────────────────────────────────────

// TestRestrictedTokenP3aDenyList answers P3a (form (a), RESTRICTED-TOKEN.md §4):
// keep the user SID active and plant explicit DENY ACEs on the sensitive paths.
//
// What it actually shows, once the child can start (restricted list =
// [Everyone, logon, synthetic]):
//
//   - reads are NOT protected: with WRITE_RESTRICTED the restricting SIDs are
//     consulted for *writes*, not reads, so a DENY on the sandbox "lock" does not
//     deny reads. Denying the *user* SID instead would work but also denies the
//     host (the same identity) — the collateral check measures exactly that.
//   - writes ARE confined: a write outside the granted root is denied.
//
// The first assertion (sensitive readable) is deliberately expected to fail as
// red: it is the finding that form (a) cannot recover read default-deny.
func TestRestrictedTokenP3aDenyList(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	locks, err := rtProbeLocks(base)
	if err != nil {
		t.Fatalf("restricting SIDs: %v", err)
	}
	user, err := rtUserSID(base)
	if err != nil {
		t.Fatalf("user SID: %v", err)
	}
	synth := locks[len(locks)-1]
	t.Logf("restricting SIDs: %v", locks)

	tok, err := rtProbeNewToken(base, rtTokenOptions{
		disableMaxPrivilege: true,
		writeRestricted:     true, // the model of RESTRICTED-TOKEN.md §3
		restrictingSIDs:     locks,
	})
	if err != nil {
		t.Fatalf("CreateRestrictedToken: %v", err)
	}
	defer tok.Close()

	ws := t.TempDir()
	gitCfg := filepath.Join(ws, ".git", "config")
	rtWriteFile(t, gitCfg, "RT-GIT-SECRET")
	visible := filepath.Join(ws, "visible.txt")
	rtWriteFile(t, visible, "RT-WS-VISIBLE")

	homeProbe := rtHomeProbe(t)
	defer os.RemoveAll(homeProbe)
	secret := filepath.Join(homeProbe, "secret.txt")
	rtWriteFile(t, secret, "RT-HOME-SECRET")

	// The synthetic lock is the only granted writer: the workspace becomes a
	// writable root, nothing else does.
	rtGrantTree(t, synth, ws)
	defer rtStripAll(t, synth, ws)

	// The deny-list: an explicit DENY for the sandbox lock on the sensitive paths.
	rtDenyRead(t, synth, gitCfg)
	rtDenyRead(t, synth, secret)
	defer rtStripAll(t, synth, gitCfg, secret)

	env := rtEnv(nil, []string{restrictedTokenLiveEnv})

	outVis, _, err := rtRun(t, tok, fmt.Sprintf(`type "%s"`, visible), ws, env)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	t.Logf("P3a workspace read: %q", outVis)
	if !strings.Contains(outVis, "RT-WS-VISIBLE") {
		t.Errorf("P3a: the workspace file must stay readable: %q", outVis)
	}

	outGit, _, _ := rtRun(t, tok, fmt.Sprintf(`type "%s"`, gitCfg), ws, env)
	outHome, _, _ := rtRun(t, tok, fmt.Sprintf(`type "%s"`, secret), ws, env)
	gitDenied := !strings.Contains(outGit, "RT-GIT-SECRET")
	homeDenied := !strings.Contains(outHome, "RT-HOME-SECRET")
	t.Logf("P3a deny-list read denial: .git/config=%v home=%v (out .git=%q home=%q)",
		gitDenied, homeDenied, outGit, outHome)
	if !gitDenied || !homeDenied {
		t.Errorf("FINDING: deny-list (DENY for the sandbox lock SID) does NOT deny reads "+
			"under a WRITE_RESTRICTED token — form (a) fails open for reads "+
			"(.git/config denied=%v home denied=%v). Reads are only ever as tight as the "+
			"user SID. See RESTRICTED-TOKEN.md §4(b).", gitDenied, homeDenied)
	}

	// Write confinement: inside the granted root it works, outside it is denied.
	outW, _, _ := rtRun(t, tok, fmt.Sprintf(`echo x> "%s" & echo WROTE`, filepath.Join(ws, "written.txt")), ws, env)
	t.Logf("P3a write inside granted root: %q", outW)
	if !pathExists(filepath.Join(ws, "written.txt")) {
		t.Errorf("P3a: a write inside the granted root must succeed (out=%q)", outW)
	}
	outside := filepath.Join(homeProbe, "outside.txt")
	_, _, _ = rtRun(t, tok, fmt.Sprintf(`echo x> "%s"`, outside), ws, env)
	t.Logf("P3a write outside granted roots: created=%v", pathExists(outside))
	if pathExists(outside) {
		t.Errorf("P3a: a write outside the granted roots must be denied (form (a) write confinement)")
	}

	// Collateral: a DENY for the *user* SID also denies the host process.
	collateral := filepath.Join(homeProbe, "collateral.txt")
	rtWriteFile(t, collateral, "RT-COLLATERAL")
	rtDenyRead(t, user, collateral)
	defer rtStripAll(t, user, collateral)
	if _, err := os.ReadFile(collateral); err == nil {
		t.Errorf("FINDING: a DENY(user SID) ACE did not deny the host — the deny list is not host-safe")
	} else {
		t.Logf("host read with DENY(user SID) denied (%v) — deny list on the user SID is not host-safe", err)
	}
}

// ── P3b ─────────────────────────────────────────────────────────────────────

// TestRestrictedTokenP3bAllowList answers P3b (form (b), RESTRICTED-TOKEN.md §4):
// disable the user SID so the user's own files become default-deny, and re-grant
// an *active* SID (the logon SID) only on the paths the toolchain needs.
//
// Green here means: sensitive paths are unreadable *and* P2 stays green — the
// full upgrade the document is hoping for.
func TestRestrictedTokenP3bAllowList(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	rtLogContext(t, base)
	locks, err := rtProbeLocks(base)
	if err != nil {
		t.Fatalf("restricting SIDs: %v", err)
	}
	logon := locks[1]
	synth := locks[2]
	t.Logf("allow-list SIDs: logon=%s synthetic=%s", logon, synth)

	tok, err := rtProbeNewToken(base, rtTokenOptions{
		disableMaxPrivilege: true,
		writeRestricted:     true,
		disableUserSID:      true,
		restrictingSIDs:     locks,
	})
	if err != nil {
		t.Fatalf("CreateRestrictedToken: %v", err)
	}
	defer tok.Close()
	// Give the token a Default DACL that satisfies both the normal check and the
	// restricting check, so pipes/events the sandbox creates (which `go` needs to
	// talk to its tools) are usable, while pre-existing user files stay outside
	// the write scope.
	rtProbeSetDefaultDacl(t, tok, logon, synth)

	ws := t.TempDir()
	mod := filepath.Join(ws, "mod")
	rtWriteModule(t, mod)
	rtPrepareGoDirs(t, ws)
	visible := filepath.Join(ws, "visible.txt")
	rtWriteFile(t, visible, "RT-WS-VISIBLE")

	// The allow-list: grant the logon SID (active in the normal check, and in the
	// restricting list) on the workspace tree only. %WINDIR%/%ProgramFiles% are
	// reached through Everyone/Users for reads; writes there are not wanted.
	rtGrantTree(t, logon, ws)
	defer rtStripAll(t, logon, ws)

	homeProbe := rtHomeProbe(t)
	defer os.RemoveAll(homeProbe)
	secret := filepath.Join(homeProbe, "secret.txt")
	rtWriteFile(t, secret, "RT-HOME-SECRET")

	env := rtEnv(rtGoEnv(ws), []string{restrictedTokenLiveEnv})

	// 1. A granted workspace file is readable.
	out, code, err := rtRun(t, tok, fmt.Sprintf(`type "%s"`, visible), ws, env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (workspace read): %v", err)
	}
	t.Logf("P3b workspace read: exit=0x%08x out=%q", uint32(code), out)
	if !strings.Contains(out, "RT-WS-VISIBLE") {
		t.Errorf("P3b: the granted workspace file must be readable: %q", out)
	}

	// 2. A user-private file outside the workspace is unreadable by default.
	out, code, err = rtRun(t, tok, fmt.Sprintf(`type "%s"`, secret), ws, env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (secret read): %v", err)
	}
	t.Logf("P3b sensitive read: exit=0x%08x out=%q", uint32(code), out)
	if strings.Contains(out, "RT-HOME-SECRET") {
		t.Errorf("P3b: the user-private file must be default-deny once the user SID is disabled: %q", out)
	}

	// 3. The toolchain still works inside the sandbox (the P2 requirement).
	out, code, err = rtRun(t, tok, `go test ./...`, mod, env)
	if err != nil {
		t.Fatalf("CreateProcessAsUser (go test): %v", err)
	}
	t.Logf("P3b go test: exit=0x%08x\n%s", uint32(code), out)
	if code != 0 {
		t.Errorf("P3b: `go test ./...` failed inside the allow-list sandbox (exit 0x%08x)", uint32(code))
	}
	if !strings.Contains(out, "ok") && !strings.Contains(out, "PASS") {
		t.Errorf("P3b: unexpected `go test` output:\n%s", out)
	}

	// 4. The host is unaffected: the grants/denies are pinned to the logon SID,
	// which the host process does not carry.
	if _, err := os.ReadFile(secret); err != nil {
		t.Errorf("P3b: the host must keep access to its own files: %v", err)
	}
	if _, err := os.ReadFile(visible); err != nil {
		t.Errorf("P3b: the host must keep access to the workspace: %v", err)
	}
}

// ── P4 ──────────────────────────────────────────────────────────────────────

// rtTokenGroupSIDs returns a private copy of every group SID the token carries.
func rtTokenGroupSIDs(tok windows.Token) ([]*windows.SID, error) {
	g, err := tok.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("GetTokenGroups: %w", err)
	}
	all := g.AllGroups()
	out := make([]*windows.SID, 0, len(all))
	for i := range all {
		sid, err := all[i].Sid.Copy()
		if err != nil {
			return nil, fmt.Errorf("copy group SID: %w", err)
		}
		out = append(out, sid)
	}
	return out, nil
}

// rtSetControlledDacl gives path a PROTECTED DACL (no inheritance) carrying
// exactly two ACEs: a full grant for the host user and a read grant for group.
// That makes the file a controlled leak target — readable from the sandbox
// exactly while group is an active SID, unreadable once it is disabled. It
// mechanism behind the section 4 leak (`.git`, the user
// profile): an object any active *group* SID grants read on.
func rtSetControlledDacl(t *testing.T, path string, user, group *windows.SID) {
	t.Helper()
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user),
			},
		},
		{
			AccessPermissions: windows.ACCESS_MASK(windows.FILE_GENERIC_READ),
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(group),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo(%s): %v", path, err)
	}
}

// TestRestrictedTokenP4AggressiveDisableList answers the open question of Fase A
// form (b) turns the *user* SID deny-only, but the token
// keeps its *group* SIDs enabled (Everyone, BUILTIN\Users, Authenticated Users,
// BUILTIN\Administrators…), so any object one of those grants read on stays
// readable — which is why `.git`, `~/.config/fyc` and most of the user
// profile still leak today. The only lever a restricted token
// has over reads is SidsToDisable, so this probe widens the disable list and
// checks the three things that must hold at once:
//
//	(1) the Go toolchain still runs inside the sandbox (P2 stays green);
//	(2) the controlled leak target (a file only a group SID grants) is unreadable;
//	(3) the host is untouched.
//
// A SID cannot sit in SidsToDisable and SidsToRestrict at the same time
// (CreateRestrictedToken -> ERROR_INVALID_PARAMETER): the
// variants that disable Everyone therefore drop it from the restricting list —
// exactly the trade-off this probe measures (does process startup survive
// without Everyone in the restricting list?).
//
// It is an exploratory probe: it logs one verdict line per variant and asserts
// only host-safety. Run it OUTSIDE the fyc sandbox (a restricted token cannot
// mint another):
//
//	FYC_RESTRICTED_TOKEN_LIVE=1 go test -v -run TestRestrictedTokenP4 ./tools/
//	FYC_RESTRICTED_TOKEN_LIVE=1 go test -v -run TestRestrictedTokenP4 ./tools/
func TestRestrictedTokenP4AggressiveDisableList(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	rtLogContext(t, base)

	user, err := rtUserSID(base)
	if err != nil {
		t.Fatalf("user SID: %v", err)
	}
	logon, err := rtLogonSID(base)
	if err != nil {
		t.Fatalf("logon SID: %v", err)
	}
	world, err := rtEveryoneSID()
	if err != nil {
		t.Fatalf("Everyone SID: %v", err)
	}
	synth, err := rtSyntheticSID()
	if err != nil {
		t.Fatalf("synthetic SID: %v", err)
	}
	byName := func(s string) *windows.SID {
		sid, err := windows.StringToSid(s)
		if err != nil {
			t.Fatalf("StringToSid(%s): %v", s, err)
		}
		return sid
	}
	admins := byName("S-1-5-32-544") // BUILTIN\Administrators
	users := byName("S-1-5-32-545")  // BUILTIN\Users
	authUsers := byName("S-1-5-11")  // Authenticated Users

	// every group the token carries except the
	// logon SID (plus the user SID). The integrity label (S-1-16-x) is skipped:
	// it grants no access.
	groups, err := rtTokenGroupSIDs(base)
	if err != nil {
		t.Fatalf("GetTokenGroups: %v", err)
	}
	allExceptLogon := []*windows.SID{user}
	for _, s := range groups {
		str := s.String()
		if str == logon.String() || strings.HasPrefix(str, "S-1-16-") {
			continue
		}
		allExceptLogon = append(allExceptLogon, s)
	}
	t.Logf("P4 variant (a) disables %d SIDs (token carries %d groups)", len(allExceptLogon), len(groups))

	// The engine's own disable list (rtDisableList): {user} plus Administrators
	// when the token carries it enabled. On an elevated host this must equal b0.
	engineDisable, err := rtDisableList(base, user)
	if err != nil {
		t.Fatalf("rtDisableList: %v", err)
	}

	variants := []struct {
		name     string
		disable  []*windows.SID
		restrict []*windows.SID
	}{
		{"leak {user} only (pre-de-elevation)", []*windows.SID{user}, []*windows.SID{world, logon, synth}},
		{"engine (rtDisableList)", engineDisable, []*windows.SID{world, logon, synth}},
		{"b0 {user,Adm} (de-elevate)", []*windows.SID{user, admins}, []*windows.SID{world, logon, synth}},
		{"b1 {user,Adm,Users,Auth} Everyone kept", []*windows.SID{user, admins, users, authUsers}, []*windows.SID{world, logon, synth}},
		{"b2 {user,Adm,Users,Auth,Everyone}", []*windows.SID{user, admins, users, authUsers, world}, []*windows.SID{logon, synth}},
		{"a  all groups except logon", allExceptLogon, []*windows.SID{logon, synth}},
	}

	kernel32 := filepath.Join(os.Getenv("SystemRoot"), "System32", "kernel32.dll")
	realGit, _ := filepath.Abs(filepath.Join("..", ".git", "config"))

	for _, v := range variants {
		tok, err := rtRawRestrictedToken(base, rtDisableMaxPrivilege|rtWriteRestricted, v.disable, rtSAA(v.restrict...))
		if err != nil {
			t.Logf("P4 %-42s -> create ERR %v", v.name, err)
			continue
		}
		rtProbeSetDefaultDacl(t, tok, logon, synth)

		ws := t.TempDir()
		mod := filepath.Join(ws, "mod")
		rtWriteModule(t, mod)
		rtPrepareGoDirs(t, ws)
		visible := filepath.Join(ws, "visible.txt")
		rtWriteFile(t, visible, "P4-WS-VISIBLE")
		rtGrantTree(t, logon, ws)

		homeProbe := rtHomeProbe(t)
		secret := filepath.Join(homeProbe, "secret.txt")
		rtWriteFile(t, secret, "P4-HOME-SECRET")
		groupFile := filepath.Join(homeProbe, "group.txt")
		rtWriteFile(t, groupFile, "P4-GROUP-SECRET")
		rtSetControlledDacl(t, groupFile, user, users)

		env := rtEnv(rtGoEnv(ws), []string{restrictedTokenLiveEnv})
		read := func(path string) string {
			out, _, _ := rtRun(t, tok, fmt.Sprintf(`type "%s"`, path), ws, env)
			return out
		}

		startOut, startCode, startErr := rtRun(t, tok, `echo P4-OK`, ws, env)
		starts := startErr == nil && startCode == 0 && strings.Contains(startOut, "P4-OK")

		sysOut, _, _ := rtRun(t, tok, fmt.Sprintf(`dir /b "%s"`, kernel32), ws, env)
		sysOK := strings.Contains(strings.ToLower(sysOut), "kernel32.dll")

		wsRead := strings.Contains(read(visible), "P4-WS-VISIBLE")
		homeLeak := strings.Contains(read(secret), "P4-HOME-SECRET")
		groupLeak := strings.Contains(read(groupFile), "P4-GROUP-SECRET")
		realGitLeak := strings.Contains(read(realGit), "repositoryformatversion")

		goOut, goCode, _ := rtRun(t, tok, `go test ./...`, mod, env)
		toolchainOK := goCode == 0 && (strings.Contains(goOut, "ok") || strings.Contains(goOut, "PASS"))

		_, _, _ = rtRun(t, tok, fmt.Sprintf(`echo x> "%s"`, filepath.Join(ws, "written.txt")), ws, env)
		writeIn := pathExists(filepath.Join(ws, "written.txt"))
		outside := filepath.Join(homeProbe, "outside.txt")
		_, _, _ = rtRun(t, tok, fmt.Sprintf(`echo x> "%s"`, outside), ws, env)
		writeOutDenied := !pathExists(outside)

		green := starts && toolchainOK && !homeLeak && !groupLeak && wsRead && writeOutDenied
		verdict := "RED"
		if green {
			verdict = "GREEN"
		}
		t.Logf("P4 %-42s start=%v sys=%v ws=%v homeLeak=%v groupLeak=%v realGitLeak=%v toolchain=%v writeIn=%v writeOutDenied=%v => %s",
			v.name, starts, sysOK, wsRead, homeLeak, groupLeak, realGitLeak, toolchainOK, writeIn, writeOutDenied, verdict)
		if !starts {
			t.Logf("P4   start detail: exit=0x%08x err=%v out=%q", uint32(startCode), startErr, startOut)
		}
		if !toolchainOK {
			t.Logf("P4   go test: exit=0x%08x out=%q", uint32(goCode), goOut)
		}

		if _, err := os.ReadFile(secret); err != nil {
			t.Errorf("P4 %s: the host lost access to its own files: %v", v.name, err)
		}

		rtStripAll(t, logon, ws)
		tok.Close()
		os.RemoveAll(homeProbe)
	}
}

// ── P5 ──────────────────────────────────────────────────────────────────────

// TestRestrictedTokenP5PowerShell bisects *why* `powershell.exe` dies at startup
// inside the sandbox with 0x8007000E (HRESULT_FROM_WIN32(14), ERROR_OUTOFMEMORY)
// while `cmd.exe`, `go` and even a .NET tool (`csc.exe`) start fine. It runs the
// same PowerShell command under a matrix of token shapes and logs which ones
// start, so the culprit shows up as the row where the outcome flips:
//
//	dm            DISABLE_MAX_PRIVILEGE only (a restricted token with nothing
//	              else: no restricting list, no disabled SID, no WRITE_RESTRICTED)
//	+restrict     restricting list [Everyone, logon, synthetic]
//	+wr           WRITE_RESTRICTED (writes must also satisfy the restricting SIDs)
//	+dacl         token Default DACL set to [logon, synthetic] (production's fix)
//	+disable{…}   SidsToDisable (form (b): the user SID; the engine also adds
//	              Administrators when it is enabled — see rtDisableList)
//
// If PowerShell starts under +wr but not under +restrict (or vice versa), that
// one ingredient is the cause; if it only fails once a SID is disabled, the
// cause is "the token is a stranger" (its own profile/HKCU become unreadable).
//
// It is informational: it logs, and asserts nothing. Run OUTSIDE the sandbox
// (gotcha 5: a restricted token cannot mint another):
//
//	FYC_RESTRICTED_TOKEN_LIVE=1 go test -v -count=1 -run TestRestrictedTokenP5 ./tools/
func TestRestrictedTokenP5PowerShell(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	rtLogContext(t, base)

	user, err := rtUserSID(base)
	if err != nil {
		t.Fatalf("user SID: %v", err)
	}
	everyone, logon, synth, err := rtRestrictingSIDs(base)
	if err != nil {
		t.Fatalf("restricting SIDs: %v", err)
	}
	admins, err := rtAdministratorsSID()
	if err != nil {
		t.Fatalf("Administrators SID: %v", err)
	}
	engineDisable, err := rtDisableList(base, user)
	if err != nil {
		t.Fatalf("rtDisableList: %v", err)
	}

	const cmd = `powershell -NoProfile -NonInteractive -Command "Write-Output P5-OK"`

	// Control: the same command on the host, no restricted token involved.
	{
		cout, cerr := exec.Command(windowsShell(), "/d", "/s", "/c", cmd).CombinedOutput()
		t.Logf("P5 %-46s -> err=%v ok=%v out=%q", "host control (no token)", cerr,
			strings.Contains(string(cout), "P5-OK"), normalizeSniffOutput(string(cout)))
	}

	trio := []*windows.SID{everyone, logon, synth}
	cases := []struct {
		name     string
		flags    uint32
		restrict []*windows.SID
		disable  []*windows.SID
		dacl     bool
	}{
		{"dm", rtDisableMaxPrivilege, nil, nil, false},
		{"dm+restrict", rtDisableMaxPrivilege, trio, nil, false},
		{"dm+wr", rtDisableMaxPrivilege | rtWriteRestricted, nil, nil, false},
		{"dm+wr+restrict", rtDisableMaxPrivilege | rtWriteRestricted, trio, nil, false},
		{"dm+wr+restrict+dacl", rtDisableMaxPrivilege | rtWriteRestricted, trio, nil, true},
		{"dm+wr+restrict+dacl+disable{user}", rtDisableMaxPrivilege | rtWriteRestricted, trio, []*windows.SID{user}, true},
		{"dm+wr+restrict+dacl+disable{user,adm}", rtDisableMaxPrivilege | rtWriteRestricted, trio, []*windows.SID{user, admins}, true},
		{"dm+wr+restrict+dacl+disable{engine}", rtDisableMaxPrivilege | rtWriteRestricted, trio, engineDisable, true},
	}

	env := rtEnv(nil, []string{restrictedTokenLiveEnv})
	for _, c := range cases {
		tok, err := rtRawRestrictedToken(base, c.flags, c.disable, rtSAA(c.restrict...))
		if err != nil {
			t.Logf("P5 %-46s -> create ERR %v", c.name, err)
			continue
		}
		if c.dacl {
			rtProbeSetDefaultDacl(t, tok, logon, synth)
		}
		out, code, runErr := rtRun(t, tok, cmd, "", env)
		tok.Close()
		switch {
		case runErr != nil:
			t.Logf("P5 %-46s -> run ERR %v", c.name, runErr)
		default:
			t.Logf("P5 %-46s -> exit=0x%08x ok=%v out=%q",
				c.name, uint32(code), strings.Contains(out, "P5-OK"), out)
		}
	}
}

// ── P6 ──────────────────────────────────────────────────────────────────────

// TestRestrictedTokenP6PowerShellFileGrants asks whether a *narrow file grant*
// is enough to make PowerShell start under the engine token — the alternative
// to documenting "PowerShell is unusable". P5 proved the
// trigger is the deny-only user SID; P6 asks whether the missing thing is a
// *file* (something ACEs can fix) or the *registry* (HKCU, which no file ACE can
// reach).
//
// Each row plants an inheritable ACE for the sandbox's session (logon) SID on a
// candidate per-user tree — plus a recursive walk on the leaves, because an
// inheritable ACE is not retroactive and the *existing* children must become
// readable — then runs PowerShell under the engine's token (dm|wr, restricting
// trio, rtDisableList, Default DACL) and strips the ACEs at the end.
//
// The Crypto/Protect row touches the user's DPAPI key stores and is gated behind
// FYC_P6_CRYPTO=1 (off by default). Run OUTSIDE the sandbox (gotcha 5):
//
//	FYC_RESTRICTED_TOKEN_LIVE=1 go test -v -count=1 -run TestRestrictedTokenP6 ./tools/
//	$env:FYC_P6_CRYPTO = "1"   # optional: also try the credential stores
func TestRestrictedTokenP6PowerShellFileGrants(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	rtLogContext(t, base)

	user, err := rtUserSID(base)
	if err != nil {
		t.Fatalf("user SID: %v", err)
	}
	everyone, logon, synth, err := rtRestrictingSIDs(base)
	if err != nil {
		t.Fatalf("restricting SIDs: %v", err)
	}
	engineDisable, err := rtDisableList(base, user)
	if err != nil {
		t.Fatalf("rtDisableList: %v", err)
	}

	appdata := os.Getenv("APPDATA")
	local := os.Getenv("LOCALAPPDATA")
	roamingMS := filepath.Join(appdata, "Microsoft")
	localMS := filepath.Join(local, "Microsoft")
	roamingPS := filepath.Join(roamingMS, "Windows", "PowerShell")
	localPS := filepath.Join(localMS, "Windows", "PowerShell")

	const cmd = `powershell -NoProfile -NonInteractive -Command "Write-Output P6-OK"`
	env := rtEnv(nil, []string{restrictedTokenLiveEnv})

	var touched []string
	grant := func(p string, deep bool) {
		if p == "" || !pathExists(p) {
			return
		}
		touched = append(touched, p)
		if err := setACE(p, logon, windows.GRANT_ACCESS, accessMaskReadWrite,
			windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT); err != nil {
			t.Logf("P6 grant %s: %v", p, err)
		}
		if deep {
			if err := grantTree(p, logon, nil); err != nil {
				t.Logf("P6 grantTree %s: %v", p, err)
			}
		}
	}

	type p6row struct {
		name    string
		shallow []string
		deep    []string
	}
	rows := []p6row{
		{"baseline (engine, no file grant)", nil, nil},
		{"+roaming PS tree", []string{appdata, roamingMS, filepath.Join(roamingMS, "Windows")}, []string{roamingPS}},
		{"+local PS tree", []string{local, localMS, filepath.Join(localMS, "Windows")}, []string{localPS}},
		{"+both PS trees", []string{appdata, roamingMS, filepath.Join(roamingMS, "Windows"), local, localMS, filepath.Join(localMS, "Windows")}, []string{roamingPS, localPS}},
	}
	if os.Getenv("FYC_P6_CRYPTO") == "1" {
		rows = append(rows, p6row{
			"+PS + crypto/protect (DPAPI)",
			[]string{appdata, local},
			[]string{roamingPS, localPS, filepath.Join(roamingMS, "Crypto"), filepath.Join(roamingMS, "Protect"), filepath.Join(localMS, "Crypto")},
		})
	}

	for _, r := range rows {
		for _, p := range r.shallow {
			grant(p, false)
		}
		for _, p := range r.deep {
			grant(p, true)
		}

		tok, err := rtRawRestrictedToken(base, rtDisableMaxPrivilege|rtWriteRestricted,
			engineDisable, rtSAA(everyone, logon, synth))
		if err != nil {
			t.Logf("P6 %-40s -> create ERR %v", r.name, err)
			continue
		}
		rtProbeSetDefaultDacl(t, tok, logon, synth)
		out, code, runErr := rtRun(t, tok, cmd, "", env)
		tok.Close()
		switch {
		case runErr != nil:
			t.Logf("P6 %-40s -> run ERR %v", r.name, runErr)
		default:
			t.Logf("P6 %-40s -> exit=0x%08x ok=%v out=%q",
				r.name, uint32(code), strings.Contains(out, "P6-OK"), out)
		}
	}

	// Cleanup: strip every ACE P6 planted, from every path it touched.
	t.Logf("P6 cleanup: stripping the logon ACE from %d touched paths", len(touched))
	rtStripAll(t, logon, touched...)
}

// ── diagnostic matrix ───────────────────────────────────────────────────────

// TestRestrictedTokenDiagTokenMatrix pins down which CreateRestrictedToken
// inputs this host accepts *and* whether a child started with each token can
// initialize. Measured facts to keep on record: restricting-SID Attributes must
// be 0 (SE_GROUP_ENABLED -> ERROR_INVALID_PARAMETER); a single-SID restricting
// list (synthetic, or even logon) dies at startup with 0xC0000142, while the
// three-element list [Everyone, logon, synthetic] starts. It asserts nothing.
func TestRestrictedTokenDiagTokenMatrix(t *testing.T) {
	rtRequireLive(t)

	base := rtBaseToken(t)
	defer base.Close()
	world, err := rtEveryoneSID()
	if err != nil {
		t.Fatalf("world SID: %v", err)
	}
	logon, err := rtLogonSID(base)
	if err != nil {
		t.Fatalf("logon SID: %v", err)
	}
	synth, err := rtSyntheticSID()
	if err != nil {
		t.Fatalf("synthetic SID: %v", err)
	}
	user, err := rtUserSID(base)
	if err != nil {
		t.Fatalf("user SID: %v", err)
	}
	t.Logf("user=%s logon=%s synthetic=%s world=%s", user, logon, synth, world)

	cases := []struct {
		name     string
		flags    uint32
		disable  []*windows.SID
		restrict []windows.SIDAndAttributes
	}{
		{"0x1 base, no restrict", rtDisableMaxPrivilege, nil, nil},
		{"0x9 no restrict", rtDisableMaxPrivilege | rtWriteRestricted, nil, nil},
		{"0x9 + [synth] attrs=0", rtDisableMaxPrivilege | rtWriteRestricted, nil, rtSAA(synth)},
		{"0x9 + [synth] attrs=ENABLED", rtDisableMaxPrivilege | rtWriteRestricted, nil, []windows.SIDAndAttributes{{Sid: synth, Attributes: windows.SE_GROUP_ENABLED}}},
		{"0x9 + [logon]", rtDisableMaxPrivilege | rtWriteRestricted, nil, rtSAA(logon)},
		{"0x9 + [Everyone]", rtDisableMaxPrivilege | rtWriteRestricted, nil, rtSAA(world)},
		{"0x9 + [Everyone,logon,synth]", rtDisableMaxPrivilege | rtWriteRestricted, nil, rtSAA(world, logon, synth)},
		{"0x9 + trio, disable user", rtDisableMaxPrivilege | rtWriteRestricted, []*windows.SID{user}, rtSAA(world, logon, synth)},
		{"0x1 + [logon] (reads restricted too)", rtDisableMaxPrivilege, nil, rtSAA(logon)},
	}
	env := rtEnv(nil, []string{restrictedTokenLiveEnv})
	for _, c := range cases {
		tok, err := rtRawRestrictedToken(base, c.flags, c.disable, c.restrict)
		if err != nil {
			t.Logf("%-34s -> create ERR  %v", c.name, err)
			continue
		}
		out, code, runErr := rtRun(t, tok, `echo MATRIX-OK`, "", env)
		switch {
		case runErr != nil:
			t.Logf("%-34s -> create OK, run ERR  %v", c.name, runErr)
		default:
			t.Logf("%-34s -> create OK, run exit=0x%08x out=%q", c.name, uint32(code), out)
		}
		tok.Close()
	}
}
