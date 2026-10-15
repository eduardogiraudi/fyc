//go:build windows

package tools

// Shared filesystem/ACL layer for the Windows command sandbox: the DACL
// primitives (setACE/stripSID/acesForSID), the allow/deny plan (planSandboxACL)
// and the diagnostics dispatchers. It was carved out of the former AppContainer
// engine when that engine was removed: the surviving
// restricted-token engine (restricted_token_windows.go) reuses every piece here
// unchanged — only the SID it grants on the read-write paths differs (the logon
// SID instead of the AppContainer SID).
//
// Everything here is syscall-only (no cgo, no external binary) and runs in the
// trusted helper process launched by buildSandboxCmd (self-re-exec); the
// *untrusted* command runs in the sandboxed child created by the engine.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"fuckyourcli/store"
	"golang.org/x/sys/windows"
)

// ── constants ───────────────────────────────────────────────────────────────

const (
	// FILE_DELETE_CHILD is missing from x/sys; needed so the sandbox can delete
	// and rename files inside the granted directories.
	fileDeleteChild = 0x00000040

	aclRevision = 2

	// win32ExitError is the helper's exit code when the sandbox itself fails
	// (fail-closed: the command never runs unsandboxed).
	win32ExitError = 126
)

// accessMaskReadWrite is the ACE granted on the AllowedRW paths: read, write,
// execute, delete and delete-child (the edit tools rename over files).
const accessMaskReadWrite = windows.ACCESS_MASK(
	windows.FILE_GENERIC_READ |
		windows.FILE_GENERIC_WRITE |
		windows.FILE_GENERIC_EXECUTE |
		windows.DELETE |
		fileDeleteChild)

// accessMaskReadExec is the read-only grant the D1 setup plants on the paths
// the sandbox may *read*: read + execute, no write. It
// is also the mask of the explicit DENY of section 5 — read+execute on a
// *directory* denies traversal too, so a denied subtree is unreachable even if
// a child carries an allow ACE.
const accessMaskReadExec = windows.ACCESS_MASK(
	windows.FILE_GENERIC_READ |
		windows.FILE_GENERIC_EXECUTE)

var (
	advapi32 = windows.NewLazySystemDLL("advapi32.dll")
	userenv  = windows.NewLazySystemDLL("userenv.dll")

	procInitializeAcl = advapi32.NewProc("InitializeAcl")
	procAddAce        = advapi32.NewProc("AddAce")

	// DeriveAppContainerSidFromAppContainerName is used *only* to clean up the
	// ACEs a previous build planted for the removed AppContainer engine;
	// it never creates a profile.
	procDeriveAppContainerSid = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

// ── ACL plan ────────────────────────────────────────────────────────────────

// aclPlan is the filesystem side of the sandbox: where the granted SID gets
// access, where our ACEs must be *absent* (default-deny) and which paths only
// help the diagnostics.
type aclPlan struct {
	// Root is the workspace: it is granted read-write — a single inheritable
	// ACE on the root in the dedicated-user engine (ensureDedicatedUserACL) or
	// grantTree's per-object walk in the restricted-token engine, whose files
	// must be reachable one by one.
	Root string
	// AllowRW are the remaining read-write paths: they get one inheritable ACE
	// on the root only; the toolchain caches are large and pre-existing entries
	// are recreated on first use.
	AllowRW []string
	// AllowRO are left untouched: %WINDIR%/%ProgramFiles% already carry an ACE
	// for "ALL APPLICATION PACKAGES", which the lowbox access check honours.
	AllowRO []string
	// Deny are the masked paths. Paths inside Root get an explicit DENY for the
	// granted SIDs (the inheritable root ALLOW would otherwise leak into them);
	// paths outside Root are left ACE-free (default-deny).
	Deny []string
}

// planSandboxACL builds the ACL plan from the sandbox spec.
func planSandboxACL(spec sandboxSpec) aclPlan {
	plan := aclPlan{Root: filepath.Clean(spec.Cwd), AllowRO: dedupePaths(spec.AllowedRO)}
	for _, p := range dedupePaths(spec.AllowedRW) {
		if samePath(p, plan.Root) {
			continue
		}
		plan.AllowRW = append(plan.AllowRW, p)
	}
	for _, p := range sensitivePaths(homeDir()) {
		plan.Deny = append(plan.Deny, filepath.FromSlash(p))
	}
	plan.Deny = append(plan.Deny, workspaceSensitivePaths(plan.Root)...)
	plan.Deny = append(plan.Deny,
		filepath.Join(plan.Root, ".codex"),
		filepath.Join(plan.Root, ".agents"),
	)
	plan.Deny = append(plan.Deny, store.ConfigDir())
	if home := homeDir(); home != "" {
		plan.Deny = append(plan.Deny,
			filepath.Join(home, ".fyc_history"),
		)
	}
	plan.Deny = dedupePaths(plan.Deny)
	return plan
}

// aclSummary reports what an ensure*ACL call actually did (diagnostics).
type aclSummary struct {
	Granted []string
	Masked  []string
	Skipped []string
}

// grantTree grants full read-write access on root and on every existing object
// below it, skipping the denied subtrees (their DACLs stay ACE-free).
func grantTree(root string, sid *windows.SID, deny []string) error {
	if err := setACE(root, sid, windows.GRANT_ACCESS, accessMaskReadWrite, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT); err != nil {
		return err
	}
	denied := make(map[string]bool, len(deny))
	for _, d := range deny {
		denied[filepath.Clean(d)] = true
	}
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: nothing to grant
		}
		p = filepath.Clean(p)
		if samePath(p, root) {
			return nil
		}
		if denied[p] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := setACE(p, sid, windows.GRANT_ACCESS, accessMaskReadWrite, windows.NO_INHERITANCE); err != nil {
			return nil
		}
		return nil
	})
}

// ── ACL primitives ──────────────────────────────────────────────────────────

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func dedupePaths(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p == "" {
			continue
		}
		c := filepath.Clean(p)
		key := strings.ToLower(c)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// objectDACL returns the DACL of a filesystem object. A nil DACL means "no DACL
// present" (fully permissive), which callers treat as "no ACE for us".
func objectDACL(path string) (*windows.ACL, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
			return nil, nil
		}
		return nil, err
	}
	return dacl, nil
}

// acesForSID reports whether the object's DACL contains an allow or a deny ACE
// for sid. Used both for idempotency and for diagnostics.
func acesForSID(path string, sid *windows.SID) (allow bool, deny bool, err error) {
	dacl, err := objectDACL(path)
	if err != nil {
		return false, false, err
	}
	if dacl == nil {
		return false, false, nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return allow, deny, err
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.IsValid() || !aceSID.Equals(sid) {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			allow = true
		case windows.ACCESS_DENIED_ACE_TYPE:
			deny = true
		}
	}
	return allow, deny, nil
}

// setACE merges one access entry into the object's DACL.
func setACE(path string, sid *windows.SID, mode windows.ACCESS_MODE, mask windows.ACCESS_MASK, inheritance uint32) error {
	dacl, err := objectDACL(path)
	if err != nil {
		return err
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: mask,
		AccessMode:        mode,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, dacl)
	if err != nil {
		return fmt.Errorf("sandbox: ACLFromEntries(%s): %w", path, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("sandbox: SetNamedSecurityInfo(%s): %w", path, err)
	}
	return nil
}

// denyRead plants an explicit, non-inheritable DENY of read+execute access for
// sid on a single object: a
// DENY ACE beats any ALLOW (inherited or explicit) and SetNamedSecurityInfo
// keeps the ACEs in canonical order (deny first), so the effect is guaranteed.
// It is the helper the D1 setup uses for its deny-list and the one the Fase-D
// runtime uses to mask the workspace's sensitive subtrees (denyWorkspacePaths);
// the restricted-token engine plants its denies through grantTree's skip walk.
func denyRead(path string, sid *windows.SID) error {
	return setACE(path, sid, windows.DENY_ACCESS, accessMaskReadExec, windows.NO_INHERITANCE)
}

// stripSID rebuilds the object's DACL without any ACE for sid and protects it
// from further inheritance, reporting whether the DACL actually changed.
// REVOKE_ACCESS is not enough: it does not remove ACEs that came in through
// inheritance (measured), and an inherited ACE is exactly what a granted parent
// leaves behind.
func stripSID(path string, sid *windows.SID) (bool, error) {
	dacl, err := objectDACL(path)
	if err != nil {
		return false, err
	}
	if dacl == nil || dacl.AceCount == 0 {
		return false, nil
	}
	var kept [][]byte
	total := uintptr(0)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceSize == 0 { // malformed ACE: stop, never corrupt a DACL
			return false, nil
		}
		size := uintptr(ace.Header.AceSize)
		if aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); aceSID.IsValid() && aceSID.Equals(sid) {
			continue
		}
		raw := make([]byte, size)
		copy(raw, unsafe.Slice((*byte)(unsafe.Pointer(ace)), size))
		kept = append(kept, raw)
		total += size
	}
	if len(kept) == int(dacl.AceCount) {
		return false, nil // nothing to strip
	}
	// ACL header + ACEs + alignment slack AddAce may need.
	size := uintptr(unsafe.Sizeof(windows.ACL{})) + total + uintptr(len(kept))*16 + 16
	buf := make([]byte, size)
	if r, _, e := procInitializeAcl.Call(uintptr(unsafe.Pointer(&buf[0])), size, aclRevision); r == 0 {
		return false, fmt.Errorf("sandbox: InitializeAcl(%s): %w", path, e)
	}
	for _, raw := range kept {
		if r, _, e := procAddAce.Call(uintptr(unsafe.Pointer(&buf[0])), aclRevision, 0xFFFFFFFF, uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw))); r == 0 {
			return false, fmt.Errorf("sandbox: AddAce(%s): %w", path, e)
		}
	}
	acl := (*windows.ACL)(unsafe.Pointer(&buf[0]))
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, acl, nil); err != nil {
		return false, fmt.Errorf("sandbox: SetNamedSecurityInfo(%s): %w", path, err)
	}
	return true, nil
}

// revokeSandboxACL removes every trace of sid from the plan's paths (the
// user-facing "/sandbox acl revoke").
func revokeSandboxACL(sid *windows.SID, plan aclPlan) (int, error) {
	// The granted trees can be huge (%LOCALAPPDATA%): only walk a tree whose root
	// still carries one of our ACEs, so the common "nothing planted" case stays
	// O(number of paths). The masked paths are always walked — they are few and
	// small, and their deny ACE is the last thing we want to leave behind.
	granted := append([]string{plan.Root}, plan.AllowRW...)

	removed := 0
	var firstErr error
	walk := func(p string) {
		if err := filepath.WalkDir(p, func(sub string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			changed, err := stripSID(sub, sid)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return nil
			}
			if changed {
				removed++
			}
			return nil
		}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, p := range granted {
		if !pathExists(p) {
			continue
		}
		if allow, deny, err := acesForSID(p, sid); err == nil && !allow && !deny {
			continue
		}
		walk(p)
	}
	for _, p := range plan.Deny {
		if pathExists(p) {
			walk(p)
		}
	}
	return removed, firstErr
}

// buildEnvBlock renders "KEY=VALUE" pairs as a CREATE_UNICODE_ENVIRONMENT block
// (sorted, double-NUL terminated). The slice stays alive for the duration of
// the CreateProcessAsUser call.
func buildEnvBlock(env []string) ([]uint16, error) {
	sorted := append([]string(nil), env...)
	sort.Strings(sorted)
	var out []uint16
	for _, e := range sorted {
		u, err := windows.UTF16FromString(e)
		if err != nil {
			continue // env entries with NUL bytes cannot be represented
		}
		out = append(out, u...)
	}
	out = append(out, 0)
	return out, nil
}

// describeACLs summarises the ACEs currently planted for each planned path.
func describeACLs(sid *windows.SID, plan aclPlan) []string {
	var out []string
	label := func(p, kind string) string {
		allow, deny, err := acesForSID(p, sid)
		switch {
		case err != nil:
			return fmt.Sprintf("%s %s: %v", kind, p, err)
		case allow && deny:
			return fmt.Sprintf("%s %s: allow+deny", kind, p)
		case allow:
			return fmt.Sprintf("%s %s: allow", kind, p)
		case deny:
			return fmt.Sprintf("%s %s: deny", kind, p)
		}
		return fmt.Sprintf("%s %s: (no ACE)", kind, p)
	}
	if plan.Root != "" {
		out = append(out, label(plan.Root, "rw"))
	}
	for _, p := range plan.AllowRW {
		if pathExists(p) {
			out = append(out, label(p, "rw"))
		}
	}
	for _, p := range plan.Deny {
		if pathExists(p) {
			out = append(out, label(p, "deny"))
		}
	}
	return out
}

// ── legacy AppContainer cleanup  ───────────────────────────

// legacyAppContainerName is the profile name the removed AppContainer engine
// used ("fyc-sandbox"). The engine is gone, but the ACEs it planted on the
// workspace (and the DENY on .git) survive, so reset has to clean them up.
const legacyAppContainerName = "fyc-sandbox"

// legacyAppContainerSID derives the SID of the removed engine's profile. It
// only reads the (deterministic) SID — it never creates a profile — and returns
// nil when no such profile exists, so the cleanup is a no-op on hosts that
// never ran the AppContainer engine.
func legacyAppContainerSID() *windows.SID {
	p, err := windows.UTF16PtrFromString(legacyAppContainerName)
	if err != nil {
		return nil
	}
	var sid *windows.SID
	r1, _, _ := procDeriveAppContainerSid.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&sid)))
	if uint32(r1) != 0 || sid == nil {
		return nil
	}
	return sid
}

// ── diagnostics dispatchers (consumed by sandbox.go, kept build-tag free) ────

// The Windows sandbox has a single engine — the restricted token
//  — so these delegate straight to it.

func sandboxStatus(root string) SandboxInfo { return restrictedTokenSandboxStatus(root) }

func sandboxACLRevoke(root string) (int, error) { return restrictedTokenSandboxRevoke(root) }

// sandboxReset is the restricted-token revoke plus the one-time removal of the
// ACEs a previous build planted for the removed AppContainer engine:
// the code is gone, but its ACEs stay on the workspace and
// on .git.
func sandboxReset(root string) (int, error) {
	n, err := restrictedTokenSandboxRevoke(root)
	if legacy := legacyAppContainerSID(); legacy != nil {
		rw, ro := standardAllowedDirs(root)
		plan := planSandboxACL(sandboxSpec{Cwd: root, AllowedRW: rw, AllowedRO: ro})
		if m, rerr := revokeSandboxACL(legacy, plan); rerr == nil {
			n += m
		}
	}
	return n, err
}
