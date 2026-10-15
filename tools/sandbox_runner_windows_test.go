//go:build windows

package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The D2 launcher/runner split has one live-only path (CreateProcessWithLogonW),
// but its two pure helpers can be pinned without an elevated host and a real
// sandbox account: the runner's argument parsing and the pipe naming.

func TestParseRunnerArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		in, out string
	}{
		{"both", []string{`--pipe-in=\\.\pipe\a`, `--pipe-out=\\.\pipe\b`}, `\\.\pipe\a`, `\\.\pipe\b`},
		{"order swapped", []string{"--pipe-out=x", "--pipe-in=y"}, "y", "x"},
		{"only pipe-in", []string{"--pipe-in=y"}, "y", ""},
		{"no args", nil, "", ""},
		{"unrelated flags", []string{"--foo=bar"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, out := parseRunnerArgs(c.args)
			if in != c.in || out != c.out {
				t.Fatalf("parseRunnerArgs = (%q, %q), want (%q, %q)", in, out, c.in, c.out)
			}
		})
	}
}

func TestRunnerPipeNames(t *testing.T) {
	in1, out1, err := runnerPipeNames()
	if err != nil {
		t.Fatalf("runnerPipeNames: %v", err)
	}
	in2, out2, err := runnerPipeNames()
	if err != nil {
		t.Fatalf("runnerPipeNames: %v", err)
	}
	if !strings.HasPrefix(in1, `\\.\pipe\fyc-sbx-in-`) {
		t.Errorf("pipe-in name %q lacks the expected prefix", in1)
	}
	if !strings.HasPrefix(out1, `\\.\pipe\fyc-sbx-out-`) {
		t.Errorf("pipe-out name %q lacks the expected prefix", out1)
	}
	if in1 == in2 || out1 == out2 {
		t.Error("pipe names must be unique per call (random suffix)")
	}
	if in1 == out1 {
		t.Error("pipe-in and pipe-out must differ")
	}
}

// TestACLGrantIsFastOnBigTree pins the whole point of the single-root ACE: the
// ACL grant must NOT walk the workspace. A tree with a few thousand files is what
// a real repo looks like; the grantTree both engines used to call rewrote one ACE
// per file there, which on the host measured as "sh is slow on repos". The cold
// bound is deliberately loose (CI disks vary) — the logged duration is the real
// signal, the assertion catches a reintroduced O(files) walk; the warm path then
// checks that a second command on the same workspace rewrites nothing.
//
// It is skipped when the environment cannot set a DACL (e.g. this dev box is
// itself already inside the sandbox), like the other ACL unit tests.
func TestACLGrantIsFastOnBigTree(t *testing.T) {
	const files = 4000
	root := t.TempDir()

	// A deep sentinel proves the grant is inheritable: nothing ever touches it
	// directly, so it only carries an ACE if it was inherited from the root.
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", deep, err)
	}
	for i := 0; i < files; i++ {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", i%40))
		if i%97 == 0 {
			dir = deep
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.go", i)), []byte("package p\n"), 0o644); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}
	// The masked subtrees the plan denies, so the DENY assertions have targets.
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	for _, d := range []string{".codex", ".agents"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	sentinel := filepath.Join(deep, "f0000.go")

	// Inert SIDs only: the synthetic lock, a second NULL-authority SID standing in
	// for the sandbox user, and a third for the restricted-token engine's logon
	// SID — the test never touches a real identity.
	synth, err := rtSyntheticSID()
	if err != nil {
		t.Fatalf("synthetic SID: %v", err)
	}
	user, err := rtTestInertSID(0x465944) // "FYD"
	if err != nil {
		t.Fatalf("inert user SID: %v", err)
	}
	logon, err := rtTestInertSID(0x465945) // "FYE"
	if err != nil {
		t.Fatalf("inert logon SID: %v", err)
	}

	cases := []struct {
		name  string
		sids  []*windows.SID
		grant func(aclPlan) (aclSummary, error)
	}{
		{"dedicated-user", []*windows.SID{synth, user},
			func(p aclPlan) (aclSummary, error) { return ensureDedicatedUserACL(user, synth, p) }},
		{"restricted-token", []*windows.SID{logon},
			func(p aclPlan) (aclSummary, error) { return ensureRestrictedTokenACL(logon, p) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planSandboxACL(sandboxSpec{Cwd: root})

			start := time.Now()
			sum, err := tc.grant(plan)
			cold := time.Since(start)
			if err != nil {
				t.Skipf("cannot set DACLs in this environment: %v", err)
			}
			t.Cleanup(func() {
				for _, sid := range tc.sids {
					stripTreeSID(t, root, sid)
				}
			})
			t.Logf("%s cold grant on %d files: %s (granted %d, masked %d, skipped %d)",
				tc.name, files, cold.Round(time.Microsecond), len(sum.Granted), len(sum.Masked), len(sum.Skipped))
			limit := 2 * time.Second
			if os.Getenv("CI") != "" {
				limit = 10 * time.Second
			}
			if cold > limit {
				t.Errorf("%s cold grant took %s on %d files (limit %s) — it is walking the tree again", tc.name, cold, files, limit)
			}

			// Correctness: the deep sentinel inherited the ALLOW, and the masked
			// workspace paths carry an explicit DENY for every SID of the case.
			for _, sid := range tc.sids {
				if allow, _, err := acesForSID(sentinel, sid); err != nil || !allow {
					t.Errorf("sentinel did not inherit the ALLOW for %s (allow=%v err=%v)", sid, allow, err)
				}
				for _, name := range []string{".git", ".codex", ".agents"} {
					p := filepath.Join(root, name)
					if _, deny, err := acesForSID(p, sid); err != nil || !deny {
						t.Errorf("%s: no explicit DENY for %s (deny=%v err=%v)", p, sid, deny, err)
					}
				}
			}

			// Warm path (the idempotency claim): a second command on the same
			// workspace must rewrite nothing — acesForSID finds the ACEs and the
			// grant functions skip.
			beforeRoot, beforeGit := daclFingerprint(t, root), daclFingerprint(t, gitDir)
			start = time.Now()
			if _, err := tc.grant(plan); err != nil {
				t.Fatalf("%s warm grant: %v", tc.name, err)
			}
			warm := time.Since(start)
			t.Logf("%s warm grant: %s", tc.name, warm.Round(time.Microsecond))
			if after := daclFingerprint(t, root); after != beforeRoot {
				t.Errorf("%s: warm grant rewrote the root DACL", tc.name)
			}
			if after := daclFingerprint(t, gitDir); after != beforeGit {
				t.Errorf("%s: warm grant rewrote the .git DACL", tc.name)
			}
		})
	}
}

// rtTestInertSID builds a second NULL-authority SID (inert on any host) so a
// test can stand in for the sandbox user without touching a real identity.
func rtTestInertSID(marker uint32) (*windows.SID, error) {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(&rtSyntheticAuthority, 1, marker, 0, 0, 0, 0, 0, 0, 0, &sid); err != nil {
		return nil, err
	}
	return sid, nil
}

// stripTreeSID removes every ACE for sid below root (test cleanup).
func stripTreeSID(t *testing.T, root string, sid *windows.SID) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		_, _ = stripSID(p, sid)
		return nil
	})
}

// daclFingerprint hashes an object's ACE list (type + flags + mask + SID bytes),
// so a test can assert that an operation left the DACL untouched.
func daclFingerprint(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%s): %v", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return "nil"
	}
	h := sha256.New()
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("GetAce(%s): %v", path, err)
		}
		if ace.Header.AceSize == 0 {
			break
		}
		h.Write(unsafe.Slice((*byte)(unsafe.Pointer(ace)), uintptr(ace.Header.AceSize)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestRunnerUpToDate pins the predicate the setup guard uses to decide
// idempotency: a runner copy is up to date only when it exists AND its recorded
// hash equals this binary's. A stale hash — what a rebuild after `sandbox setup`
// leaves behind — must report false, or the setup short-circuits and leaves `sh`
// broken (the bug that used to force `sandbox reset` + `sandbox setup`).
func TestRunnerUpToDate(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	self, err := sbxFileSHA256(exe)
	if err != nil {
		t.Fatalf("hash own executable: %v", err)
	}

	dir := t.TempDir()
	copyPath := filepath.Join(dir, filepath.Base(exe))
	if err := os.WriteFile(copyPath, []byte("runner copy placeholder"), 0o644); err != nil {
		t.Fatalf("write runner copy: %v", err)
	}

	cases := []struct {
		name string
		m    sbxMarker
		want bool
	}{
		{"matching hash", sbxMarker{RunnerPath: copyPath, RunnerHash: self}, true},
		{"stale hash", sbxMarker{RunnerPath: copyPath, RunnerHash: "deadbeef"}, false},
		{"hash is case-insensitive", sbxMarker{RunnerPath: copyPath, RunnerHash: strings.ToUpper(self)}, true},
		{"missing file", sbxMarker{RunnerPath: filepath.Join(dir, "gone.exe"), RunnerHash: self}, false},
		{"empty path", sbxMarker{RunnerHash: self}, false},
		{"empty hash", sbxMarker{RunnerPath: copyPath}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runnerUpToDate(c.m); got != c.want {
				t.Fatalf("runnerUpToDate(%+v) = %v, want %v", c.m, got, c.want)
			}
		})
	}
}

func TestSandboxRunnerExitStatus(t *testing.T) {
	err := sandboxRunnerExitError(7)
	if got := shCommandStatus(err, nil, shDefaultTimeout); !strings.HasPrefix(got, "command failed: exit code 7") {
		t.Fatal(got)
	}
}

func TestWindowsProcessReadAfterClose(t *testing.T) {
	for _, runner := range []bool{false, true} {
		t.Run(fmt.Sprint(runner), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			var proc sandboxProcess = &windowsSandboxProcess{r: r}
			if runner {
				proc = &windowsRunnerProcess{out: r}
			}
			proc.Close()
			if _, err := proc.Read(make([]byte, 1)); err == nil {
				t.Fatal("closed process stream should report EOF or a read error")
			}
		})
	}
}
