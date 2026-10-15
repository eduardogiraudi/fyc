//go:build windows

package tools

// Restricted-token sandbox, the sole Windows sandbox engine (the AppContainer
// it superseded was removed: it is
// the only engine under which `go build`/`go test` run inside the sandbox
// (RESTRICTED-TOKEN.md §1 — the AppContainer's device isolation makes NUL
// unreachable, and the Go toolchain opens it as stdin for its own tools).
//
// Form (b) (allow-list), the only one the Phase 0 probes (P3b) found safe for
// reads: the token's own user SID is disabled — the user's files become
// default-deny, exactly like a foreign account — and a still-active SID (the
// token's logon SID) is re-granted, explicitly, on the paths the sandboxed
// process actually needs (workspace, toolchain caches). On an elevated host
// that alone is not enough: BUILTIN\Administrators stays ENABLED and keeps the
// user profile (and `.git`, and every Administrators-granted file) readable, so
// it is disabled too (rtDisableList) — the de-elevation the Fase A probe (P4)
// found necessary and sufficient. Form (a) (deny-list
// on top of an active user SID) was measured to fail open for reads (P3a) and is
// not implemented here.
//
// Facts this file relies on, measured on Windows 11 (17/09/2026,
// restrictedtoken_windows_live_test.go):
//
//   - CreateRestrictedToken + CreateProcessAsUser run without elevation;
//   - restricting SIDs must be passed with Attributes = 0 — SE_GROUP_ENABLED
//     makes CreateRestrictedToken fail with ERROR_INVALID_PARAMETER (0x57);
//   - a WRITE_RESTRICTED child must not *allocate* a console: CREATE_NO_WINDOW
//     and CREATE_NEW_CONSOLE die at startup with 0xC0000142
//     (STATUS_DLL_INIT_FAILED). The console must be inherited instead — no
//     STARTUPINFOEX and no attribute list (a restricted token needs neither);
//   - anonymous pipes (what `go` uses to talk to its own tools) need a SID that
//     is both in the restricting list and in the token's Default DACL: the
//     restricting list is [Everyone, logon, synthetic] and the token's Default
//     DACL is set to [logon, synthetic] — logon satisfies the normal access
//     check, the synthetic SID satisfies the write-restricted check, and
//     neither grants the user or SYSTEM on pre-existing files;
//   - with the user SID disabled and that Default DACL fix, the toolchain
//     builds and tests inside the sandbox, sensitive paths are unreadable, and
//     the host process (which does not carry the logon or synthetic SID of the
//     *sandboxed* token — each CreateRestrictedToken call mints its own logon
//     SID copy tied to that token, and the synthetic SID is inert outside any
//     sandbox) keeps its own access unaffected.
//
// setACE/stripSID/grantTree, accessMaskReadWrite and the shell/env helpers
// (windowsShellCmdLine/buildEnvBlock/filteredEnv/planSandboxACL/
// standardAllowedDirs, plus the aclPlan type) are reused as-is from
// sandbox_acl_windows.go, sandbox_windows.go and
// sandbox_paths_windows.go/sandbox.go — only the SID granted on RW paths
// changes (the logon SID instead of an AppContainer SID).

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── constants ───────────────────────────────────────────────────────────────

const (
	// CreateRestrictedToken flags (winnt.h). DISABLE_MAX_PRIVILEGE drops every
	// privilege not required to run; WRITE_RESTRICTED makes the access check
	// for *write* access also require the restricting SIDs (RESTRICTED-TOKEN.md
	// §3), which is what turns "grant logon" into "grant logon, but only where
	// we also planted the synthetic lock".
	rtDisableMaxPrivilege = 0x00000001
	rtWriteRestricted     = 0x00000008
)

// rtSyntheticAuthority + rtSyntheticSubAuthority build S-1-0-<marker>: a SID
// under the NULL authority that belongs to no real principal on this or any
// other host. ACEs planted for it are inert outside this sandbox — a host
// token never carries it — which is exactly what makes it safe to grant on
// writable roots without widening who (else) can write there.
var rtSyntheticAuthority = windows.SECURITY_NULL_SID_AUTHORITY

const rtSyntheticSubAuthority = 0x465943 // "FYC" packed into a subauthority

var rtCreateRestrictedTokenProc = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

// ── SIDs ────────────────────────────────────────────────────────────────────

// rtOpenSelfToken duplicates the current process token with the rights
// CreateRestrictedToken and CreateProcessAsUser need. The caller closes it.
func rtOpenSelfToken() (windows.Token, error) {
	const access = windows.TOKEN_DUPLICATE | windows.TOKEN_ASSIGN_PRIMARY | windows.TOKEN_QUERY |
		windows.TOKEN_ADJUST_DEFAULT | windows.TOKEN_ADJUST_SESSIONID
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), access, &tok); err != nil {
		return 0, fmt.Errorf("sandbox: OpenProcessToken: %w", err)
	}
	return tok, nil
}

// rtUserSID returns a private copy of the token's user SID — the SID that
// form (b) disables so the user's own files stop being readable by default.
func rtUserSID(tok windows.Token) (*windows.SID, error) {
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("sandbox: GetTokenUser: %w", err)
	}
	return u.User.Sid.Copy()
}

// rtLogonSID returns the token's logon SID (SE_GROUP_LOGON_ID): a session-
// scoped SID that stays active once the user SID is disabled, and the SID
// this engine grants on the paths the sandbox may read and write.
func rtLogonSID(tok windows.Token) (*windows.SID, error) {
	g, err := tok.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("sandbox: GetTokenGroups: %w", err)
	}
	for _, grp := range g.AllGroups() {
		if grp.Attributes&windows.SE_GROUP_LOGON_ID != 0 {
			return grp.Sid.Copy()
		}
	}
	return nil, errors.New("sandbox: no logon SID (SE_GROUP_LOGON_ID) in token")
}

// rtEveryoneSID is the World SID (S-1-1-0): the broadest lock, present in the
// restricting list so ordinary process-startup writes to shared objects (the
// ones every process touches, not the sandbox's own) still succeed.
func rtEveryoneSID() (*windows.SID, error) {
	return windows.StringToSid("S-1-1-0")
}

// rtSyntheticSID returns the sandbox's own write lock: see the package
// comment above for why the NULL authority is used.
func rtSyntheticSID() (*windows.SID, error) {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(&rtSyntheticAuthority, 1,
		rtSyntheticSubAuthority, 0, 0, 0, 0, 0, 0, 0, &sid); err != nil {
		return nil, fmt.Errorf("sandbox: AllocateAndInitializeSid: %w", err)
	}
	return sid, nil
}

// rtRestrictingSIDs is the fixed restricting list [Everyone, logon,
// synthetic]: Everyone and logon make process startup and inherited IPC work,
// the synthetic SID is the only one ever granted on writable roots, so writes
// stay confined to what this engine explicitly plants (RESTRICTED-TOKEN.md
// §5, gotcha 3).
func rtRestrictingSIDs(base windows.Token) (everyone, logon, synthetic *windows.SID, err error) {
	if everyone, err = rtEveryoneSID(); err != nil {
		return nil, nil, nil, err
	}
	if logon, err = rtLogonSID(base); err != nil {
		return nil, nil, nil, err
	}
	if synthetic, err = rtSyntheticSID(); err != nil {
		return nil, nil, nil, err
	}
	return everyone, logon, synthetic, nil
}

func rtSAA(sids ...*windows.SID) []windows.SIDAndAttributes {
	// Attributes = 0 is mandatory: SE_GROUP_ENABLED on a restricting SID makes
	// CreateRestrictedToken fail with ERROR_INVALID_PARAMETER (0x57), measured
	// in restrictedtoken_windows_live_test.go.
	out := make([]windows.SIDAndAttributes, 0, len(sids))
	for _, s := range sids {
		out = append(out, windows.SIDAndAttributes{Sid: s})
	}
	return out
}

// ── token ───────────────────────────────────────────────────────────────────

// rtAdministratorsSID returns BUILTIN\Administrators (S-1-5-32-544). Its ACEs
// are what leave an *elevated* process's user profile (and `.git`, and any file
// granting Administrators) readable from the sandbox: on an elevated host
// Administrators is ENABLED, so it satisfies the normal access check on those
// objects even though the user SID is deny-only.
func rtAdministratorsSID() (*windows.SID, error) {
	return windows.StringToSid("S-1-5-32-544")
}

// rtGroupEnabled reports whether sid is present in the token *and* presently
// enabled (it actually grants). A SID that is absent, or already deny-only,
// must not go into SidsToDisable: it grants nothing, and CreateRestrictedToken
// rejects inputs the token does not carry.
func rtGroupEnabled(tok windows.Token, sid *windows.SID) (bool, error) {
	g, err := tok.GetTokenGroups()
	if err != nil {
		return false, fmt.Errorf("sandbox: GetTokenGroups: %w", err)
	}
	for _, grp := range g.AllGroups() {
		if grp.Sid.Equals(sid) {
			return grp.Attributes&windows.SE_GROUP_ENABLED != 0, nil
		}
	}
	return false, nil
}

// rtDisableList is the form-(b) SidsToDisable: the user SID (always) plus
// BUILTIN\Administrators when the base token carries it *enabled*. Disabling
// Administrators is the de-elevation step the Fase A probe
// found necessary and sufficient: on an elevated host it is the
// only change that closes the read leak (profile, `.git`, Administrators-
// granted files) while the toolchain keeps building and testing inside the
// sandbox. On an already-unelevated host Administrators is deny-only (or
// absent), so there is nothing to disable and the list stays {user}.
func rtDisableList(base windows.Token, user *windows.SID) ([]*windows.SID, error) {
	disable := []*windows.SID{user}
	admins, err := rtAdministratorsSID()
	if err != nil {
		return nil, err
	}
	enabled, err := rtGroupEnabled(base, admins)
	if err != nil {
		return nil, err
	}
	if enabled {
		disable = append(disable, admins)
	}
	return disable, nil
}

// rtNewRestrictedToken builds the form-(b) token: DISABLE_MAX_PRIVILEGE |
// WRITE_RESTRICTED, the given SIDs disabled (see rtDisableList), restricted to
// [everyone, logon, synthetic]. The caller closes the returned token.
func rtNewRestrictedToken(base windows.Token, disable []*windows.SID, everyone, logon, synthetic *windows.SID) (windows.Token, error) {
	flags := uint32(rtDisableMaxPrivilege | rtWriteRestricted)
	disableBuf := make([]windows.SIDAndAttributes, len(disable))
	for i, s := range disable {
		disableBuf[i] = windows.SIDAndAttributes{Sid: s}
	}
	restrict := rtSAA(everyone, logon, synthetic)

	var disablePtr, restrictPtr unsafe.Pointer
	if len(disableBuf) > 0 {
		disablePtr = unsafe.Pointer(&disableBuf[0])
	}
	if len(restrict) > 0 {
		restrictPtr = unsafe.Pointer(&restrict[0])
	}

	var out windows.Token
	r1, _, callErr := rtCreateRestrictedTokenProc.Call(
		uintptr(base),
		uintptr(flags),
		uintptr(len(disableBuf)), uintptr(disablePtr),
		0, 0, // no privileges to delete explicitly (DISABLE_MAX_PRIVILEGE covers it)
		uintptr(len(restrict)), uintptr(restrictPtr),
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("sandbox: CreateRestrictedToken: %w", callErr)
	}
	return out, nil
}

// rtSetTokenDefaultDacl replaces the token's Default DACL with [logon,
// synthetic]: objects the sandboxed process creates itself (the anonymous
// pipes `go` uses to talk to its own tool invocations) then carry an ACE for
// each, so the WRITE_RESTRICTED check on those objects passes without ever
// granting the user or SYSTEM on anything pre-existing.
func rtSetTokenDefaultDacl(tok windows.Token, logon, synthetic *windows.SID) error {
	entries := []windows.EXPLICIT_ACCESS{
		rtGrantAllEntry(logon),
		rtGrantAllEntry(synthetic),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("sandbox: ACLFromEntries(default dacl): %w", err)
	}
	dacl := struct{ DefaultDacl *windows.ACL }{acl}
	if err := windows.SetTokenInformation(tok, windows.TokenDefaultDacl,
		(*byte)(unsafe.Pointer(&dacl)), uint32(unsafe.Sizeof(dacl))); err != nil {
		return fmt.Errorf("sandbox: SetTokenInformation(TokenDefaultDacl): %w", err)
	}
	return nil
}

func rtGrantAllEntry(sid *windows.SID) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

// ── ACL plan (form b: grant logon on the RW paths, deny inside the root) ────

// ensureRestrictedTokenACL grants the logon SID on the workspace and the
// standard RW paths (the same grant the removed engine planted for its SID),
// but onto a SID that is *also* active in the restricting list — the
// RESTRICTED-TOKEN.md section 3 "same generic SID + mask primitives, different SID and
// strategy" reuse.
//
// Like the Fase-D engine, the workspace grant is a *single* inheritable ALLOW
// ACE on the root instead of grantTree's per-object walk (which rewrote every
// existing file on every command), and it is skipped when acesForSID already
// reports the ACE. Because that root ACE is inheritable it would leak into the
// masked workspace subtrees, so an explicit DENY is planted for the logon SID on
// every plan.Deny path inside the root (.git/.codex/.agents) via
// denyWorkspacePaths: with the user SID off the token the logon SID is the one
// that grants reads, and an explicit DENY beats any ALLOW, inherited or not.
// Deny paths outside the root need no ACE — nothing inherits into them (form
// (b), P3b).
func ensureRestrictedTokenACL(logon *windows.SID, plan aclPlan) (aclSummary, error) {
	var sum aclSummary

	if plan.Root != "" {
		if err := grantRootInherited(plan.Root, logon); err != nil {
			return sum, err
		}
		if err := denyWorkspacePaths(plan.Root, plan.Deny, logon); err != nil {
			return sum, err
		}
		sum.Granted = append(sum.Granted, plan.Root)
	}
	for _, p := range plan.AllowRW {
		if !pathExists(p) {
			sum.Skipped = append(sum.Skipped, p)
			continue
		}
		if err := grantRootInherited(p, logon); err != nil {
			sum.Skipped = append(sum.Skipped, p)
			continue
		}
		sum.Granted = append(sum.Granted, p)
	}
	sum.Masked = append(sum.Masked, plan.Deny...)
	return sum, nil
}

// ── process creation ────────────────────────────────────────────────────────

// runInRestrictedToken runs `cmd.exe /d /s /c <CmdStr>` under a form-(b)
// restricted token and returns the child's exit code. Mirrors
// the fail-closed contract: any error here means the caller
// reports failure and nothing runs unsandboxed.
func runInRestrictedToken(spec sandboxSpec, env []string) (int, error) {
	base, err := rtOpenSelfToken()
	if err != nil {
		return win32ExitError, err
	}
	defer base.Close()

	user, err := rtUserSID(base)
	if err != nil {
		return win32ExitError, err
	}
	everyone, logon, synthetic, err := rtRestrictingSIDs(base)
	if err != nil {
		return win32ExitError, err
	}

	disable, err := rtDisableList(base, user)
	if err != nil {
		return win32ExitError, err
	}

	tok, err := rtNewRestrictedToken(base, disable, everyone, logon, synthetic)
	if err != nil {
		return win32ExitError, err
	}
	defer tok.Close()

	if err := rtSetTokenDefaultDacl(tok, logon, synthetic); err != nil {
		return win32ExitError, err
	}

	plan := planSandboxACL(spec)
	if _, err := ensureRestrictedTokenACL(logon, plan); err != nil {
		return win32ExitError, err
	}

	cmdline := windowsShellCmdLine(spec.CmdStr)
	code, err := createRestrictedTokenProcess(tok, cmdline, spec.Cwd, env)
	if err != nil {
		return win32ExitError, err
	}
	return int(code), nil
}

// createRestrictedTokenProcess is CreateProcessAsUser with a plain
// STARTUPINFO: a restricted token needs no STARTUPINFOEX, no attribute list
// and no PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES — a
// restricted token is an ordinary token, that is the whole point of this
// engine (RESTRICTED-TOKEN.md §3).
//
// The console is inherited, never allocated: a WRITE_RESTRICTED token cannot
// *allocate* a console (CREATE_NO_WINDOW and CREATE_NEW_CONSOLE both die at
// startup with 0xC0000142 / STATUS_DLL_INIT_FAILED, measured in
// restrictedtoken_windows_live_test.go), so creationFlags carries neither —
// the child attaches to the caller's existing console and STARTF_USESTDHANDLES
// wires the caller's own stdio through, so the child inherits the helper's
// console and stdio directly (no ConPTY, no console allocation).
func createRestrictedTokenProcess(tok windows.Token, cmdline, cwd string, env []string) (uint32, error) {
	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, _ = windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	si.StdOutput, _ = windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	si.StdErr, _ = windows.GetStdHandle(windows.STD_ERROR_HANDLE)

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

	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT)

	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(tok, nil, cmdlinePtr, nil, nil, true, flags,
		&envBlock[0], cwdPtr, si, &pi); err != nil {
		return 0, fmt.Errorf("sandbox: CreateProcessAsUser: %w", err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, fmt.Errorf("sandbox: WaitForSingleObject: %w", err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, fmt.Errorf("sandbox: GetExitCodeProcess: %w", err)
	}
	return code, nil
}

// ── diagnostics (/sandbox, `fyc sandbox`) ────────────────────

// restrictedTokenSandboxStatus describes the sandbox state for /sandbox and
// `fyc sandbox status`: it reports the logon-SID grant plan. A restricted
// token has no persistent profile —
// there is nothing to derive between runs, only a plan to describe.
func restrictedTokenSandboxStatus(root string) SandboxInfo {
	info := SandboxInfo{
		Platform: "windows (restricted-token)",
		Enabled:  true,
		Network:  defaultWindowsSandboxNetwork,
		Root:     root,
		Note:     "no persistent profile: the token and its grants are minted fresh for each command",
	}
	rw, ro := standardAllowedDirs(root)
	plan := planSandboxACL(sandboxSpec{
		Cwd:       root,
		AllowedRW: rw,
		AllowedRO: ro,
		Network:   defaultWindowsSandboxNetwork,
	})
	info.Allow = append([]string{plan.Root}, plan.AllowRW...)
	info.AllowRO = plan.AllowRO
	info.Deny = plan.Deny

	base, err := rtOpenSelfToken()
	if err != nil {
		info.Err = "cannot inspect own token: " + err.Error()
		return info
	}
	defer base.Close()
	logon, err := rtLogonSID(base)
	if err != nil {
		info.Err = "cannot derive logon SID: " + err.Error()
		return info
	}
	info.SID = logon.String()
	info.ACL = describeACLs(logon, plan)
	return info
}

// restrictedTokenSandboxRevoke strips every ACE for the current logon SID
// from the plan's RW paths (Deny paths never carry one — see
// ensureRestrictedTokenACL). The logon SID is session-scoped: a later login
// mints a different one, so ACEs from a previous session are already inert
// and nothing further needs cleaning up for them.
func restrictedTokenSandboxRevoke(root string) (int, error) {
	base, err := rtOpenSelfToken()
	if err != nil {
		return 0, err
	}
	defer base.Close()
	logon, err := rtLogonSID(base)
	if err != nil {
		return 0, err
	}
	rw, ro := standardAllowedDirs(root)
	plan := planSandboxACL(sandboxSpec{Cwd: root, AllowedRW: rw, AllowedRO: ro})
	return revokeSandboxACL(logon, plan)
}
