//go:build windows

package tools

// D1: the elevated, one-time setup of the dedicated-user
// sandbox. It is the only part of Fase D that requires admin — the explicit
// price of the phase.
//
// What it does, idempotently (a version marker short-circuits a re-run):
//
//  1. creates the local account FycSandbox and the group FycSandboxUsers
//     (netapi32: NetUserAdd/NetLocalGroupAdd/NetLocalGroupAddMembers) and puts
//     the account in BUILTIN\Users so it may log on locally;
//  2. stores its password with DPAPI (CryptProtectData) in
//     %USERPROFILE%\.fyc\.sandbox-secrets\sandbox_users.json, with a DACL that
//     DENIES read to FycSandboxUsers — the sandbox identity cannot read its own
//     credentials;
//  3. copies the binary into %ProgramData%\fyc\runner with a DACL that
//     lets FycSandboxUsers only read+execute it (the sandbox runs this copy as
//     its runner). Beyond that it plants nothing on the filesystem: no read-ACL
//     (the legacy system trees are already readable through BUILTIN\Users, and
//     the profile is not granted — a broad grant would need a slow per-object
//     walk);
//  4. plants no deny-list either: with no broad grant there is nothing to shadow.
//     The per-command deny-list lives in the runtime (section 5);
//  5. writes the version marker and a log
//     (%USERPROFILE%\.fyc\.sandbox\sandbox.log).
//
// Firewall/WFP (section 8.6) is deliberately NOT here: it is D3, separable, and
// additive on the same SID (section 19).
//
// The username is neutral ("FycSandbox"): fyc uses a single account, no
// offline/online pair (section 19).
//
// Fail-closed: any error aborts the setup; nothing is left half-applied silently
// (the marker is written only at the very end, so a crash leaves the setup
// "not done" and a re-run retries).

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	"fuckyourcli/store"
	"golang.org/x/sys/windows"
)

// ── constants ───────────────────────────────────────────────────────────────

const (
	// sandboxAccountName is the single, neutrally named local account the
	// sandbox runs as (no Offline/Online pair).
	sandboxAccountName = "FycSandbox"
	// sandboxGroupName is the group the deny-list and the credential DACL are
	// pinned to (section 19: DENYs live on the group so they survive an account
	// change).
	sandboxGroupName = "FycSandboxUsers"
	// sandboxSetupVersion is the marker version. Bumping it makes an existing
	// install re-run the setup (additive migration, section 19).
	sandboxSetupVersion = 1

	sbxUserPrivUser       = 1      // USER_PRIV_USER
	sbxUfScript           = 0x0001 // UF_SCRIPT
	sbxUfDontExpirePasswd = 0x10000

	sbxSeeMaskNoCloseProcess = 0x00000040 // SEE_MASK_NOCLOSEPROCESS
	sbxSwShowNormal          = 1

	// Net* status codes (lmerr.h).
	sbxNerrSuccess          = 0
	sbxNerrGroupNotFound    = 2220
	sbxNerrUserNotFound     = 2221
	sbxNerrResourceNotFound = 2222
	sbxNerrMemberNotFound   = 2226
	sbxNerrGroupExists      = 2223
	sbxNerrUserExists       = 2224

	// ERROR_ALREADY_MEMBER: adding an existing member is a no-op for us.
	sbxErrorAlreadyMember = 1378

	// sbxErrorAliasExists is what NetLocalGroupAdd actually reports (1379,
	// ERROR_ALIAS_EXISTS) when the local group already exists: local groups are
	// aliases, so it does NOT return NERR_GroupExists (2223). Both are accepted
	// below so an idempotent re-run does not fail on an existing group.
	sbxErrorAliasExists = 1379
)

// ── win32 plumbing ──────────────────────────────────────────────────────────

var (
	sbxAdvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	sbxNetapi32 = windows.NewLazySystemDLL("netapi32.dll")
	sbxShell32  = windows.NewLazySystemDLL("shell32.dll")

	// Raw LookupAccountNameW, mirroring the probe: the two-pass sizing is
	// explicit and measured, no dependence on the x/sys wrapper's buffer dance.
	sbxLookupAccountNameW = sbxAdvapi32.NewProc("LookupAccountNameW")

	// Net* — not exposed by x/sys/windows.
	sbxProcNetUserAdd          = sbxNetapi32.NewProc("NetUserAdd")
	sbxProcNetUserDel          = sbxNetapi32.NewProc("NetUserDel")
	sbxNetUserSetInfo          = sbxNetapi32.NewProc("NetUserSetInfo")
	sbxProcNetLocalGroupAdd    = sbxNetapi32.NewProc("NetLocalGroupAdd")
	sbxNetLocalGroupDel        = sbxNetapi32.NewProc("NetLocalGroupDel")
	sbxNetLocalGroupAddMembers = sbxNetapi32.NewProc("NetLocalGroupAddMembers")
	sbxNetLocalGroupDelMembers = sbxNetapi32.NewProc("NetLocalGroupDelMembers")
	sbxNetUserGetLocalGroups   = sbxNetapi32.NewProc("NetUserGetLocalGroups")
	sbxNetApiBufferFree        = sbxNetapi32.NewProc("NetApiBufferFree")

	sbxShellExecuteExW = sbxShell32.NewProc("ShellExecuteExW")
)

// netapi32 structs (lmshare.h / lmaccess.h).
type sbxUserInfo1 struct {
	Name        *uint16
	Password    *uint16
	PasswordAge uint32
	Privilege   uint32
	HomeDir     *uint16
	Comment     *uint16
	Flags       uint32
	ScriptPath  *uint16
}

type sbxUserInfo1003 struct{ Password *uint16 }
type sbxUserInfo1008 struct{ Flags uint32 }
type sbxLocalGroupInfo0 struct{ Name *uint16 }
type sbxLocalGroupMembersInfo3 struct{ DomainAndName *uint16 }
type sbxLocalGroupUsersInfo0 struct{ Name *uint16 }

// sbxShellExecuteInfo mirrors SHELLEXECUTEINFOW. Field order and sizes match the
// C struct on amd64 (the implicit padding after nShow and dwHotKey is inserted
// by the Go compiler).
type sbxShellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

// ── paths ───────────────────────────────────────────────────────────────────

func sbxFycDir() string      { return filepath.Join(homeDir(), ".fyc") }
func sbxStateDir() string    { return filepath.Join(sbxFycDir(), ".sandbox") }
func sbxSecretsDir() string  { return filepath.Join(sbxFycDir(), ".sandbox-secrets") }
func sbxMarkerPath() string  { return filepath.Join(sbxStateDir(), "setup.json") }
func sbxLogPath() string     { return filepath.Join(sbxStateDir(), "sandbox.log") }
func sbxSecretsPath() string { return filepath.Join(sbxSecretsDir(), "sandbox_users.json") }

// sbxUsersRoot is %SystemDrive%\Users.
func sbxUsersRoot() string {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	return drive + `\Users`
}

// sbxSandboxProfileDir is where Windows creates the sandbox account's profile.
func sbxSandboxProfileDir() string { return filepath.Join(sbxUsersRoot(), sandboxAccountName) }

// ── runner binary (ProgramData copy) ────────────────────────────────────────
//
// The sandbox identity must be able to *read and execute* the binary it runs
// as `__sandbox-runner`. The installed binary lives under the real user's
// %LOCALAPPDATA%, which is default-deny for that foreign identity — so setup
// copies it to %ProgramData%\fyc\runner and pins a DACL that lets
// FycSandboxUsers read+execute but never write. The launcher runs that copy and
// checks the marker's hash so a binary updated after setup is caught instead of
// silently falling back.

// sbxProgramDataDir is %ProgramData%\fyc: outside every user profile,
// so it survives the default-deny rule and is reachable by the sandbox user.
func sbxProgramDataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "fyc")
}

// sbxRunnerDir holds the copy of the binary the sandbox runs as its runner.
func sbxRunnerDir() string { return filepath.Join(sbxProgramDataDir(), "runner") }

// sbxRunnerExePath is the runner copy for the current binary's name (the copy
// keeps the same base name, so it re-enters this package as `__sandbox-runner`).
func sbxRunnerExePath() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return filepath.Join(sbxRunnerDir(), filepath.Base(exe)), true
}

// sbxFileSHA256 hashes a file (sha256, lowercase hex).
func sbxFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sbxCopyFile copies src to dst byte for byte.
func sbxCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sbxProtectRunnerDir gives the runner folder a fresh, protected DACL: SYSTEM
// and Administrators full, FycSandboxUsers and BUILTIN\Users read+execute —
// nobody write. PROTECTED stops the folder inheriting `Users: write` from
// ProgramData. BUILTIN\Users is required so the *launcher* can stat the runner
// copy: it runs as the real user with a split (unelevated) token, in which
// Administrators is deny-only, so the Administrators ACE does not cover it and
// runnerBinaryForLaunch's existence check would wrongly report the runner as
// missing.
func sbxProtectRunnerDir(dir string, groupSID *windows.SID) error {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("system SID: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("administrators SID: %w", err)
	}
	// Resolved by SID string (the name is localized); read+execute only.
	users, err := windows.StringToSid("S-1-5-32-545")
	if err != nil {
		return fmt.Errorf("users SID: %w", err)
	}
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: mask,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(system, windows.GENERIC_ALL),
		entry(admins, windows.GENERIC_ALL),
		entry(groupSID, accessMaskReadExec),
		entry(users, accessMaskReadExec),
	}, nil)
	if err != nil {
		return fmt.Errorf("sandbox: ACLFromEntries(%s): %w", dir, err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, info, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("sandbox: SetNamedSecurityInfo(%s): %w", dir, err)
	}
	return nil
}

// sbxInstallRunnerBinary recreates the ProgramData runner folder, hardens its
// DACL and copies the running binary into it. It returns the copy's path and
// sha256, which the caller stores in the marker.
func sbxInstallRunnerBinary(log *sbxLogger, groupSID *windows.SID) (string, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve own executable: %w", err)
	}
	dir := sbxRunnerDir()
	// Recreate from scratch so the copied file inherits the fresh DACL (a
	// leftover file would keep the previous one).
	if pathExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return "", "", fmt.Errorf("clear runner dir: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create runner dir: %w", err)
	}
	if err := sbxProtectRunnerDir(dir, groupSID); err != nil {
		return "", "", err
	}
	dst := filepath.Join(dir, filepath.Base(exe))
	if err := sbxCopyFile(exe, dst); err != nil {
		return "", "", fmt.Errorf("copy runner binary: %w", err)
	}
	sum, err := sbxFileSHA256(dst)
	if err != nil {
		return "", "", fmt.Errorf("hash runner binary: %w", err)
	}
	log.logf("runner binary installed: %s (sha256 %s)", dst, sum)
	return dst, sum, nil
}

// ── logging ─────────────────────────────────────────────────────────────────

// sbxLogger writes the same line to the setup log and to an optional sink (the
// CLI's stdout). It is safe for concurrent use (the read-ACL walk is parallel).
type sbxLogger struct {
	mu   sync.Mutex
	f    *os.File
	sink func(string)
}

func sbxNewLogger(path string, sink func(string)) *sbxLogger {
	lg := &sbxLogger{sink: sink}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				lg.f = f
			}
		}
	}
	return lg
}

func (l *sbxLogger) logf(format string, a ...any) {
	if l == nil {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, a...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		fmt.Fprintln(l.f, line)
	}
	if l.sink != nil {
		l.sink(line)
	}
}

// Close is idempotent so reset can close the log before deleting it.
func (l *sbxLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// ── DPAPI ───────────────────────────────────────────────────────────────────

func sbxDPAPIProtect(plain []byte) ([]byte, error) {
	var in windows.DataBlob
	if len(plain) > 0 {
		in = windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	b := make([]byte, out.Size)
	copy(b, unsafe.Slice(out.Data, out.Size))
	return b, nil
}

func sbxDPAPIUnprotect(blob []byte) ([]byte, error) {
	var in windows.DataBlob
	if len(blob) > 0 {
		in = windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	b := make([]byte, out.Size)
	copy(b, unsafe.Slice(out.Data, out.Size))
	return b, nil
}

// ── marker and credentials ──────────────────────────────────────────────────

type sbxMarker struct {
	Version   int    `json:"version"`
	User      string `json:"user"`
	Group     string `json:"group"`
	CreatedAt string `json:"createdAt"`
	SetupSID  string `json:"setupSid,omitempty"`
	// RunnerPath/RunnerHash record the ProgramData copy of the binary the sandbox
	// runs as its runner (see sbxInstallRunnerBinary). The launcher refuses to
	// start when the running binary no longer matches RunnerHash.
	RunnerPath string `json:"runnerPath,omitempty"`
	RunnerHash string `json:"runnerHash,omitempty"`
}

type sbxCredentials struct {
	User      string `json:"user"`
	Group     string `json:"group"`
	Password  string `json:"password"`
	CreatedAt string `json:"createdAt"`
}

// sbxSecretsFile is the on-disk shape: the credentials JSON protected with DPAPI
// (base64 of the opaque blob), so the plaintext never touches the disk.
type sbxSecretsFile struct {
	Version   int    `json:"version"`
	Protected string `json:"protected"`
}

func sbxReadMarker() (sbxMarker, bool) {
	var m sbxMarker
	raw, err := os.ReadFile(sbxMarkerPath())
	if err != nil {
		return m, false
	}
	if json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	return m, true
}

func sbxWriteMarker(m sbxMarker) error {
	if err := os.MkdirAll(sbxStateDir(), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sbxMarkerPath(), raw, 0o600)
}

func sbxLoadCredentials() (sbxCredentials, bool) {
	var c sbxCredentials
	raw, err := os.ReadFile(sbxSecretsPath())
	if err != nil {
		return c, false
	}
	var f sbxSecretsFile
	if json.Unmarshal(raw, &f) != nil {
		return c, false
	}
	blob, err := base64.StdEncoding.DecodeString(f.Protected)
	if err != nil {
		return c, false
	}
	plain, err := sbxDPAPIUnprotect(blob)
	if err != nil {
		return c, false
	}
	if json.Unmarshal(plain, &c) != nil || c.Password == "" {
		return c, false
	}
	return c, true
}

// sbxStoreCredentials writes the DPAPI-protected credentials and pins a DENY on
// the sandbox group so the sandbox identity cannot read its own password
func sbxStoreCredentials(c sbxCredentials, denySID *windows.SID) error {
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	blob, err := sbxDPAPIProtect(plain)
	if err != nil {
		return err
	}
	f := sbxSecretsFile{Version: sandboxSetupVersion, Protected: base64.StdEncoding.EncodeToString(blob)}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sbxSecretsDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(sbxSecretsPath(), raw, 0o600); err != nil {
		return err
	}
	if denySID != nil {
		_ = setACE(sbxSecretsDir(), denySID, windows.DENY_ACCESS, accessMaskReadExec, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
		_ = setACE(sbxSecretsPath(), denySID, windows.DENY_ACCESS, accessMaskReadExec, windows.NO_INHERITANCE)
	}
	return nil
}

// ── SID / account helpers ───────────────────────────────────────────────────

// sbxIsElevated reports whether the token is (presently) elevated.
func sbxIsElevated(tok windows.Token) bool {
	var elevated uint32
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &n); err != nil {
		return false
	}
	return elevated != 0
}

// sbxLookupSID resolves a local account or group name to its SID (two-pass
// LookupAccountNameW, mirroring the probe).
func sbxLookupSID(name string) (*windows.SID, error) {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var sidSize, domSize, use uint32
	r, _, callErr := sbxLookupAccountNameW.Call(0, uintptr(unsafe.Pointer(nameP)), 0,
		uintptr(unsafe.Pointer(&sidSize)), 0, uintptr(unsafe.Pointer(&domSize)), uintptr(unsafe.Pointer(&use)))
	if r == 0 {
		if le, ok := callErr.(syscall.Errno); !ok || le != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, fmt.Errorf("LookupAccountNameW(%s): %w", name, callErr)
		}
	}
	if sidSize == 0 {
		return nil, fmt.Errorf("LookupAccountNameW(%s): empty SID", name)
	}
	sidBuf := make([]byte, sidSize)
	domBuf := make([]uint16, domSize+1)
	if r, _, callErr := sbxLookupAccountNameW.Call(0, uintptr(unsafe.Pointer(nameP)),
		uintptr(unsafe.Pointer(&sidBuf[0])), uintptr(unsafe.Pointer(&sidSize)),
		uintptr(unsafe.Pointer(&domBuf[0])), uintptr(unsafe.Pointer(&domSize)),
		uintptr(unsafe.Pointer(&use))); r == 0 {
		return nil, fmt.Errorf("LookupAccountNameW(%s): %w", name, callErr)
	}
	return (*windows.SID)(unsafe.Pointer(&sidBuf[0])).Copy()
}

// sbxWellKnownName resolves the localized name of a well-known SID (the "Users"
// group name is language-dependent, so it is never hard-coded).
func sbxWellKnownName(sidStr string) (string, error) {
	sid, err := windows.StringToSid(sidStr)
	if err != nil {
		return "", err
	}
	name := make([]uint16, 256)
	dom := make([]uint16, 256)
	nameLen, domLen := uint32(len(name)), uint32(len(dom))
	var use uint32
	if err := windows.LookupAccountSid(nil, sid, &name[0], &nameLen, &dom[0], &domLen, &use); err != nil {
		return "", fmt.Errorf("LookupAccountSid(%s): %w", sidStr, err)
	}
	return windows.UTF16ToString(name[:nameLen]), nil
}

// sbxUserInGroup reports whether the local user is a member of the local group.
func sbxUserInGroup(user, group string) bool {
	userP, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return false
	}
	var buf *byte
	var entriesRead, totalEntries uint32
	// prefmaxlen = 0xFFFFFFFF: no preferred limit (avoids ERROR_MORE_DATA).
	r, _, _ := sbxNetUserGetLocalGroups.Call(0, uintptr(unsafe.Pointer(userP)), 0, 0,
		uintptr(unsafe.Pointer(&buf)), 0xFFFFFFFF,
		uintptr(unsafe.Pointer(&entriesRead)), uintptr(unsafe.Pointer(&totalEntries)))
	if uint32(r) != 0 || buf == nil {
		return false
	}
	defer sbxNetApiBufferFree.Call(uintptr(unsafe.Pointer(buf)))
	want := strings.ToLower(group)
	infos := unsafe.Slice((*sbxLocalGroupUsersInfo0)(unsafe.Pointer(buf)), entriesRead)
	for i := range infos {
		if strings.ToLower(windows.UTF16PtrToString(infos[i].Name)) == want {
			return true
		}
	}
	return false
}

// ── netapi32 wrappers ───────────────────────────────────────────────────────

func sbxNetUserAdd(name, pass string) (uint32, error) {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	passP, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		return 0, err
	}
	ui := sbxUserInfo1{
		Name:      nameP,
		Password:  passP,
		Privilege: sbxUserPrivUser,
		Flags:     sbxUfScript | sbxUfDontExpirePasswd,
	}
	var parm uint32
	r, _, _ := sbxProcNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&ui)), uintptr(unsafe.Pointer(&parm)))
	runtime.KeepAlive(ui)
	if r != 0 {
		return uint32(r), fmt.Errorf("NetUserAdd(%s): %w", name, syscall.Errno(r))
	}
	return 0, nil
}

func sbxNetUserDel(name string) error {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	r, _, _ := sbxProcNetUserDel.Call(0, uintptr(unsafe.Pointer(nameP)))
	if r != 0 {
		if uint32(r) == sbxNerrUserNotFound {
			return nil
		}
		return fmt.Errorf("NetUserDel(%s): %w", name, syscall.Errno(r))
	}
	return nil
}

func sbxSetUserPassword(name, pass string) error {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	passP, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		return err
	}
	ui := sbxUserInfo1003{Password: passP}
	var parm uint32
	r, _, _ := sbxNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(nameP)), 1003,
		uintptr(unsafe.Pointer(&ui)), uintptr(unsafe.Pointer(&parm)))
	runtime.KeepAlive(ui)
	if r != 0 {
		return fmt.Errorf("NetUserSetInfo(1003) %s: %w", name, syscall.Errno(r))
	}
	return nil
}

func sbxSetUserFlags(name string, flags uint32) error {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	ui := sbxUserInfo1008{Flags: flags}
	var parm uint32
	r, _, _ := sbxNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(nameP)), 1008,
		uintptr(unsafe.Pointer(&ui)), uintptr(unsafe.Pointer(&parm)))
	runtime.KeepAlive(ui)
	if r != 0 {
		return fmt.Errorf("NetUserSetInfo(1008) %s: %w", name, syscall.Errno(r))
	}
	return nil
}

func sbxNetLocalGroupAdd(name string) (uint32, error) {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	gi := sbxLocalGroupInfo0{Name: nameP}
	var parm uint32
	r, _, _ := sbxProcNetLocalGroupAdd.Call(0, 0, uintptr(unsafe.Pointer(&gi)), uintptr(unsafe.Pointer(&parm)))
	runtime.KeepAlive(gi)
	if r != 0 {
		return uint32(r), fmt.Errorf("NetLocalGroupAdd(%s): %w", name, syscall.Errno(r))
	}
	return 0, nil
}

func sbxNetLocalGroupDelByName(name string) error {
	nameP, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	r, _, _ := sbxNetLocalGroupDel.Call(0, uintptr(unsafe.Pointer(nameP)))
	if r != 0 {
		if uint32(r) == sbxNerrGroupNotFound {
			return nil
		}
		return fmt.Errorf("NetLocalGroupDel(%s): %w", name, syscall.Errno(r))
	}
	return nil
}

// sbxAddMember adds a user to a local group as DOMAIN\Name (level 3).
func sbxAddMember(group, member string) error {
	groupP, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return err
	}
	memberP, err := windows.UTF16PtrFromString(member)
	if err != nil {
		return err
	}
	mi := sbxLocalGroupMembersInfo3{DomainAndName: memberP} // LOCALGROUP_MEMBERS_INFO_3
	r, _, _ := sbxNetLocalGroupAddMembers.Call(0, uintptr(unsafe.Pointer(groupP)), 3,
		uintptr(unsafe.Pointer(&mi)), 1)
	runtime.KeepAlive(mi)
	if r != 0 {
		if uint32(r) == sbxErrorAlreadyMember {
			return nil
		}
		return fmt.Errorf("NetLocalGroupAddMembers(%s,%s): %w", group, member, syscall.Errno(r))
	}
	return nil
}

// sbxDelMember removes a user from a local group; "not a member" and "no such
// group/user" are success (the state we want is "not a member").
func sbxDelMember(group, member string) error {
	groupP, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return err
	}
	memberP, err := windows.UTF16PtrFromString(member)
	if err != nil {
		return err
	}
	mi := sbxLocalGroupMembersInfo3{DomainAndName: memberP}
	r, _, _ := sbxNetLocalGroupDelMembers.Call(0, uintptr(unsafe.Pointer(groupP)), 3,
		uintptr(unsafe.Pointer(&mi)), 1)
	runtime.KeepAlive(mi)
	if r != 0 {
		switch uint32(r) {
		case sbxNerrMemberNotFound, sbxNerrGroupNotFound, sbxNerrUserNotFound, sbxNerrResourceNotFound:
			return nil
		}
		return fmt.Errorf("NetLocalGroupDelMembers(%s,%s): %w", group, member, syscall.Errno(r))
	}
	return nil
}

// ── password ────────────────────────────────────────────────────────────────

// sbxRandomPassword returns an n-char password from a local-policy-friendly
// alphabet, forcing one upper, one lower and one digit so Windows complexity
// (>=3 classes) is satisfied regardless of the host policy.
func sbxRandomPassword(n int) (string, error) {
	if n < 4 {
		n = 4
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	const (
		upper  = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower  = "abcdefghijkmnpqrstuvwxyz"
		digits = "23456789"
		alnum  = upper + lower + digits
	)
	out := make([]byte, n)
	out[0] = upper[int(b[0])%len(upper)]
	out[1] = lower[int(b[1])%len(lower)]
	out[2] = digits[int(b[2])%len(digits)]
	for i := 3; i < n; i++ {
		out[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(out), nil
}

// ── the deny-list  ────────────────────────────────────

// sbxDenyPaths lists current config and legacy sensitive paths for reset to strip
// sandbox ACEs. The old config location remains so previous DENYs are removed too.
func sbxDenyPaths(workspace string) []string {
	home := homeDir()
	if home == "" {
		return []string{store.ConfigDir()}
	}
	roaming := filepath.Join(home, "AppData", "Roaming")
	local := filepath.Join(home, "AppData", "Local")
	out := []string{
		store.ConfigDir(),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".git-credentials"),
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".fyc"), // sandbox state, credentials, log
		filepath.Join(home, ".fyc_history"),
		filepath.Join(roaming, "Microsoft", "Protect"), // DPAPI keys
		filepath.Join(roaming, "Microsoft", "Crypto"),
		filepath.Join(local, "Microsoft", "Crypto"),
		filepath.Join(local, "Google", "Chrome", "User Data", "Default", "Login Data"),
		filepath.Join(local, "Microsoft", "Edge", "User Data", "Default", "Login Data"),
		filepath.Join(roaming, "Mozilla", "Firefox", "Profiles"),
	}
	if workspace != "" {
		out = append(out, workspaceSensitivePaths(workspace)...)
		out = append(out,
			filepath.Join(workspace, ".codex"),
			filepath.Join(workspace, ".agents"),
		)
	}
	return dedupePaths(out)
}

// ── reset helpers (legacy cleanup) ──────────────────────────────────────────

// sbxSystemReadRoots is the legacy set of system trees a previous setup tried to
// grant. Kept *only* for reset, which must strip the ACE
// an older build planted on C:\ProgramData (Administrators there hold Full, so
// WRITE_DAC works). Nothing plants on these paths any more.
func sbxSystemReadRoots() []string {
	return nonEmpty([]string{
		os.Getenv("SystemRoot"),
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("ProgramData"),
	})
}

// sbxStripSIDFromRoots removes every ACE for sid from the trees a previous setup
// may have written — the profile, the legacy system roots, the deny paths and the
// secrets dir — the filesystem half of reset.
func sbxStripSIDFromRoots(sid *windows.SID, workspace string) int {
	// The profile is walked (a legacy install planted a per-object ACE there); the
	// system roots only got a single root ACE each, and the deny paths a single
	// DENY each. This no longer reuses the setup's read-ACL list (there is none): a
	// root we no longer plant must still be cleaned up.
	var walkRoots, leafRoots []string
	if home := homeDir(); home != "" {
		walkRoots = append(walkRoots, home)
	}
	leafRoots = append(leafRoots, sbxSystemReadRoots()...)
	walkRoots = append(walkRoots, sbxDenyPaths(workspace)...)
	walkRoots = append(walkRoots, sbxSecretsDir())

	n := 0
	stripLeaf := func(p string) {
		if !pathExists(p) {
			return
		}
		allow, deny, err := acesForSID(p, sid)
		if err != nil || (!allow && !deny) {
			return // nothing planted (fast path)
		}
		if changed, _ := stripSID(p, sid); changed {
			n++
		}
	}
	stripTree := func(p string) {
		if !pathExists(p) {
			return
		}
		if allow, deny, err := acesForSID(p, sid); err == nil && !allow && !deny {
			return
		}
		_ = filepath.WalkDir(p, func(sub string, _ os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if changed, _ := stripSID(sub, sid); changed {
				n++
			}
			return nil
		})
	}
	for _, p := range dedupePaths(leafRoots) {
		stripLeaf(p)
	}
	for _, p := range dedupePaths(walkRoots) {
		stripTree(p)
	}
	return n
}

// ── elevation ───────────────────────────────────────────────────────────────

// sbxElevateSelf re-launches this executable, elevated, with args (UAC "runas")
// and waits for it to finish. A cancelled prompt is reported as an error.
func sbxElevateSelf(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(sbxQuoteArgs(args))
	if err != nil {
		return err
	}
	sei := sbxShellExecuteInfo{
		fMask:        sbxSeeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        sbxSwShowNormal,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	r, _, callErr := sbxShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r == 0 {
		if le, ok := callErr.(syscall.Errno); ok && le == windows.ERROR_CANCELLED {
			return fmt.Errorf("elevation cancelled by the user")
		}
		return fmt.Errorf("ShellExecuteExW(runas): %w", callErr)
	}
	if sei.hProcess == 0 {
		return fmt.Errorf("elevation launched but no process handle was returned")
	}
	defer windows.CloseHandle(sei.hProcess)
	if _, err := windows.WaitForSingleObject(sei.hProcess, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("elevated setup exited with code %d", code)
	}
	return nil
}

// sbxQuoteArgs renders argv for the elevated command line (only single tokens are
// used in practice; quoting keeps it correct anyway).
func sbxQuoteArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"") {
			parts[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// sbxSetupArgs forwards the setup flags to the elevated copy so an invocation
// with --force/--workspace keeps its intent across the UAC prompt.
func sbxSetupArgs(opts SandboxSetupOptions) []string {
	args := []string{"sandbox", "setup"}
	if opts.Workspace != "" {
		args = append(args, "--workspace", opts.Workspace)
	}
	if opts.Force {
		args = append(args, "--force")
	}
	return args
}

// ── orchestration ───────────────────────────────────────────────────────────

func sbxNewResult() SandboxSetupResult {
	return SandboxSetupResult{
		Platform:    "windows (restricted-token, dedicated user)",
		Supported:   true,
		User:        sandboxAccountName,
		Group:       sandboxGroupName,
		Version:     sandboxSetupVersion,
		SecretsPath: sbxSecretsPath(),
		LogPath:     sbxLogPath(),
	}
}

// sandboxSetup performs (or plans) the D1 setup. Idempotent: an up-to-date
// marker short-circuits a re-run unless Force is set.
func sandboxSetup(opts SandboxSetupOptions) (SandboxSetupResult, error) {
	res := sbxNewResult()

	elevated := false
	if base, err := rtOpenSelfToken(); err == nil {
		elevated = sbxIsElevated(base)
		base.Close()
	}
	res.Elevated = elevated

	// Idempotency: a marker at the current version *and* a runner copy that still
	// matches this binary. A stale or missing runner must NOT short-circuit: the
	// launcher is fail-closed on it, so `sh` stays broken until the setup re-runs
	// (elevated) to refresh the ProgramData copy. Without this check, re-running
	// install.ps1 after a rebuild left the marker pointing at the old runner and
	// only `sandbox reset` + `sandbox setup` recovered.
	if !opts.Force {
		if m, ok := sbxReadMarker(); ok && m.Version >= sandboxSetupVersion && runnerUpToDate(m) {
			res.AlreadyDone = true
			res.Notes = append(res.Notes, fmt.Sprintf("already set up (marker v%d)", m.Version))
			return res, nil
		}
	}

	if opts.DryRun {
		res.Notes = append(res.Notes, "dry-run: no user, group, ACE or file was created")
		return res, nil
	}

	if !elevated {
		if err := sbxElevateSelf(sbxSetupArgs(opts)...); err != nil {
			return res, err
		}
		res.ReLaunched = true
		if m, ok := sbxReadMarker(); ok && m.Version >= sandboxSetupVersion && runnerUpToDate(m) {
			res.AlreadyDone = true
		}
		res.Notes = append(res.Notes, "the work ran in the elevated copy (see the setup log)")
		return res, nil
	}

	// Elevated: do the work.
	log := sbxNewLogger(sbxLogPath(), opts.Log)
	defer log.Close()
	log.logf("=== fyc sandbox setup v%d (user %s) ===", sandboxSetupVersion, sandboxAccountName)

	creds, err := sbxSetupAccounts(log)
	if err != nil {
		return res, err
	}

	groupSID, err := sbxLookupSID(sandboxGroupName)
	if err != nil || groupSID == nil {
		return res, fmt.Errorf("cannot resolve the group SID %s: %w", sandboxGroupName, err)
	}

	if err := sbxStoreCredentials(creds, groupSID); err != nil {
		return res, fmt.Errorf("store credentials: %w", err)
	}
	log.logf("credentials protected with DPAPI at %s", sbxSecretsPath())

	// Copy the binary the sandbox runs as its runner into ProgramData, where the
	// foreign identity can read+execute it. No read-ACL or deny-list is planted:
	// the profile itself stays default-deny
	runnerPath, runnerHash, err := sbxInstallRunnerBinary(log, groupSID)
	if err != nil {
		return res, fmt.Errorf("install runner binary: %w", err)
	}

	marker := sbxMarker{
		Version:    sandboxSetupVersion,
		User:       sandboxAccountName,
		Group:      sandboxGroupName,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		RunnerPath: runnerPath,
		RunnerHash: runnerHash,
	}
	if base, err := rtOpenSelfToken(); err == nil {
		if u, err := rtUserSID(base); err == nil && u != nil {
			marker.SetupSID = u.String()
		}
		base.Close()
	}
	if err := sbxWriteMarker(marker); err != nil {
		return res, fmt.Errorf("write marker: %w", err)
	}
	log.logf("marker written (v%d) — setup complete", sandboxSetupVersion)
	res.Notes = append(res.Notes, "setup complete")
	return res, nil
}

// sbxSetupAccounts creates the account and the group (idempotently), sets the
// password, and wires the memberships. It returns the credentials to store.
func sbxSetupAccounts(log *sbxLogger) (sbxCredentials, error) {
	var pass string
	if c, ok := sbxLoadCredentials(); ok && c.Password != "" {
		log.logf("credentials: reusing the stored password for %s", sandboxAccountName)
		pass = c.Password
	} else {
		p, err := sbxRandomPassword(32)
		if err != nil {
			return sbxCredentials{}, err
		}
		pass = p
	}

	status, err := sbxNetUserAdd(sandboxAccountName, pass)
	switch {
	case err == nil:
		log.logf("created local user %s", sandboxAccountName)
	case status == sbxNerrUserExists:
		log.logf("local user %s already exists: resetting the password", sandboxAccountName)
		if err := sbxSetUserPassword(sandboxAccountName, pass); err != nil {
			return sbxCredentials{}, err
		}
	default:
		return sbxCredentials{}, err
	}
	if err := sbxSetUserFlags(sandboxAccountName, sbxUfScript|sbxUfDontExpirePasswd); err != nil {
		log.logf("set user flags: %v", err)
	}

	status, err = sbxNetLocalGroupAdd(sandboxGroupName)
	switch {
	case err == nil:
		log.logf("created local group %s", sandboxGroupName)
	case status == sbxNerrGroupExists || status == sbxErrorAliasExists:
		log.logf("local group %s already exists", sandboxGroupName)
	default:
		return sbxCredentials{}, err
	}

	if err := sbxAddMember(sandboxGroupName, sandboxAccountName); err != nil {
		log.logf("add %s to %s: %v", sandboxAccountName, sandboxGroupName, err)
	}
	if usersName, err := sbxWellKnownName("S-1-5-32-545"); err == nil {
		if err := sbxAddMember(usersName, sandboxAccountName); err != nil {
			log.logf("add %s to %s: %v", sandboxAccountName, usersName, err)
		}
	} else {
		log.logf("resolve BUILTIN\\Users: %v", err)
	}

	return sbxCredentials{
		User:      sandboxAccountName,
		Group:     sandboxGroupName,
		Password:  pass,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// sandboxUsersStatus reports the D1 state without changing anything.
func sandboxUsersStatus(workspace string) SandboxUsersStatus {
	st := SandboxUsersStatus{
		Platform:    "windows (restricted-token, dedicated user)",
		Supported:   true,
		User:        sandboxAccountName,
		Group:       sandboxGroupName,
		SecretsPath: sbxSecretsPath(),
		LogPath:     sbxLogPath(),
	}
	if base, err := rtOpenSelfToken(); err == nil {
		st.Elevated = sbxIsElevated(base)
		base.Close()
	} else {
		st.Err = err.Error()
	}
	if sid, err := sbxLookupSID(sandboxAccountName); err == nil && sid != nil {
		st.UserExists = true
	}
	if sid, err := sbxLookupSID(sandboxGroupName); err == nil && sid != nil {
		st.GroupExists = true
	}
	if st.UserExists && st.GroupExists {
		st.MemberOfGroup = sbxUserInGroup(sandboxAccountName, sandboxGroupName)
	}
	if m, ok := sbxReadMarker(); ok {
		st.SetupVersion = m.Version
	}
	if _, ok := sbxLoadCredentials(); ok {
		st.SecretsPresent = true
	}
	st.Ready = st.UserExists && st.GroupExists && st.MemberOfGroup &&
		st.SetupVersion >= sandboxSetupVersion && st.SecretsPresent
	return st
}

// sandboxUsersReset undoes everything the setup planted: the account, the group,
// the credentials, the marker, the log, the dedicated profile and the
// read-ACL/deny ACEs
func sandboxUsersReset() (SandboxUsersResetResult, error) {
	res := SandboxUsersResetResult{Platform: "windows (restricted-token, dedicated user)", Supported: true}
	log := sbxNewLogger(sbxLogPath(), nil)
	log.logf("=== fyc sandbox reset ===")

	// Resolve the group SID *before* deleting the group, then strip its ACEs.
	if groupSID, err := sbxLookupSID(sandboxGroupName); err == nil && groupSID != nil {
		res.StrippedACLs = sbxStripSIDFromRoots(groupSID, "")
		log.logf("stripped %d ACEs for %s", res.StrippedACLs, sandboxGroupName)
	}

	// Membership first (best-effort), then the account and the group.
	if usersName, err := sbxWellKnownName("S-1-5-32-545"); err == nil {
		_ = sbxDelMember(usersName, sandboxAccountName)
	}
	_ = sbxDelMember(sandboxGroupName, sandboxAccountName)

	if err := sbxNetUserDel(sandboxAccountName); err != nil {
		log.logf("delete user: %v", err)
	} else {
		res.UserDeleted = true
	}
	if err := sbxNetLocalGroupDelByName(sandboxGroupName); err != nil {
		log.logf("delete group: %v", err)
	} else {
		res.GroupDeleted = true
	}

	// The dedicated profile is owned by the sandbox account, not the real user.
	if prof := sbxSandboxProfileDir(); pathExists(prof) {
		if err := os.RemoveAll(prof); err == nil {
			res.RemovedFiles = append(res.RemovedFiles, prof)
		} else {
			log.logf("remove profile %s: %v", prof, err)
		}
	}

	// The ProgramData runner folder: the copy of the binary the setup installed.
	if dir := sbxRunnerDir(); pathExists(dir) {
		if err := os.RemoveAll(dir); err == nil {
			res.RemovedFiles = append(res.RemovedFiles, dir)
		} else {
			log.logf("remove runner dir %s: %v", dir, err)
		}
	}
	// Drop the now-empty %ProgramData%\fyc parent, best-effort.
	_ = os.Remove(sbxProgramDataDir())

	log.logf("=== reset complete ===")
	log.Close() // close before deleting the log file

	for _, f := range []string{sbxSecretsPath(), sbxMarkerPath(), sbxLogPath()} {
		if err := os.Remove(f); err == nil {
			res.RemovedFiles = append(res.RemovedFiles, f)
		}
	}
	return res, nil
}
