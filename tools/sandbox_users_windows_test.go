//go:build windows

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/store"
	"golang.org/x/sys/windows"
)

// Unit tests for the D1 setup that do NOT need admin:
// the pure helpers (marker/secrets JSON, password generation, quoting, path
// plans) plus the filesystem primitives (denyRead) exercised
// against temp objects with an inert, synthetic SID. The elevated orchestration
// (user/group creation, DPAPI storage) is covered by the gated live probe in
// sandbox_users_windows_live_test.go.

func sbxTestSID(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := rtSyntheticSID()
	if err != nil {
		t.Fatalf("rtSyntheticSID: %v", err)
	}
	return sid
}

func TestSbxMarkerJSONRoundTrip(t *testing.T) {
	want := sbxMarker{Version: 3, User: "FycSandbox", Group: "FycSandboxUsers", CreatedAt: "2026-09-19T00:00:00Z", SetupSID: "S-1-5-21-1"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got sbxMarker
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestSbxSecretsFileShape(t *testing.T) {
	// The on-disk secret must never carry a cleartext password: only a version
	// and the DPAPI blob.
	c := sbxCredentials{User: "FycSandbox", Group: "FycSandboxUsers", Password: "SuperSecret123", CreatedAt: "2026-09-19T00:00:00Z"}
	plain, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	f := sbxSecretsFile{Version: sandboxSetupVersion, Protected: "AAA="}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal file: %v", err)
	}
	if strings.Contains(string(raw), "SuperSecret123") || strings.Contains(string(plain), "protected") {
		t.Fatalf("secrets file shape is wrong: %s", raw)
	}
	var back sbxSecretsFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal file: %v", err)
	}
	if back.Version != sandboxSetupVersion || back.Protected != "AAA=" {
		t.Fatalf("secrets file round-trip mismatch: %+v", back)
	}
}

func TestSbxDPAPIRoundTrip(t *testing.T) {
	secret := []byte(`{"password":"pfx-9-_AZ"}`)
	blob, err := sbxDPAPIProtect(secret)
	if err != nil {
		// DPAPI needs the user's crypto device; a locked-down sandbox may not
		// expose it. The live probe covers the real host.
		t.Skipf("DPAPI unavailable in this environment: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("DPAPI produced an empty blob")
	}
	got, err := sbxDPAPIUnprotect(blob)
	if err != nil {
		t.Fatalf("CryptUnprotectData: %v", err)
	}
	if string(got) != string(secret) {
		t.Fatalf("DPAPI round-trip mismatch: got %q want %q", got, secret)
	}
}

func TestSbxRandomPasswordComplexity(t *testing.T) {
	for i := 0; i < 20; i++ {
		p, err := sbxRandomPassword(32)
		if err != nil {
			t.Fatalf("sbxRandomPassword: %v", err)
		}
		if len(p) != 32 {
			t.Fatalf("length = %d, want 32", len(p))
		}
		var upper, lower, digit bool
		for _, r := range p {
			switch {
			case r >= 'A' && r <= 'Z':
				upper = true
			case r >= 'a' && r <= 'z':
				lower = true
			case r >= '0' && r <= '9':
				digit = true
			}
		}
		if !upper || !lower || !digit {
			t.Fatalf("password %q lacks a character class (Windows complexity)", p)
		}
	}
}

func TestSbxQuoteArgs(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"sandbox", "setup"}, "sandbox setup"},
		{[]string{`a b`}, `"a b"`},
		{[]string{`he"llo`}, `"he\"llo"`},
		{[]string{""}, `""`},
	}
	for _, c := range cases {
		if got := sbxQuoteArgs(c.in); got != c.want {
			t.Errorf("sbxQuoteArgs(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSbxDenyPathsCoversSecrets(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", "")
	t.Setenv("AppData", t.TempDir())
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	ws := `C:\work\repo`
	paths := sbxDenyPaths(ws)
	lower := make(map[string]bool, len(paths))
	for _, p := range paths {
		lower[strings.ToLower(filepath.Clean(p))] = true
	}
	want := []string{
		filepath.Join(ws, ".git"), // the workspace's .git stays hidden
		filepath.Join(ws, ".codex"),
		filepath.Join(home, ".fyc"), // sandbox state + credentials
		filepath.Join(home, ".ssh"), // private keys
		filepath.Join(home, ".aws"), // cloud credentials
		filepath.Join(home, ".git-credentials"),
		store.ConfigDir(), // current API keys
	}
	for _, w := range want {
		if !lower[strings.ToLower(filepath.Clean(w))] {
			t.Errorf("deny-list is missing %s\nhave: %v", w, paths)
		}
	}
}

func TestSbxDenyPathsIncludesConfigDirectoryOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config override")
	t.Setenv("FYC_CONFIG_DIR", dir)
	for _, home := range []string{t.TempDir(), ""} {
		t.Run("home="+home, func(t *testing.T) {
			t.Setenv("USERPROFILE", home)
			paths := sbxDenyPaths(`C:\work\repo`)
			for _, p := range paths {
				if samePath(p, dir) {
					return
				}
			}
			t.Fatalf("reset paths do not include %q: %v", dir, paths)
		})
	}
}

func TestSbxDenyReadPlantAndRevoke(t *testing.T) {
	sid := sbxTestSID(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := denyRead(target, sid); err != nil {
		t.Skipf("cannot set a DACL in this environment: %v", err)
	}
	if _, deny, err := acesForSID(target, sid); err != nil || !deny {
		t.Fatalf("denyRead did not plant a DENY ACE (deny=%v err=%v)", deny, err)
	}
	if changed, err := stripSID(target, sid); err != nil || !changed {
		t.Fatalf("stripSID did not remove the ACE (changed=%v err=%v)", changed, err)
	}
	if _, deny, err := acesForSID(target, sid); err != nil || deny {
		t.Fatalf("the DENY ACE survived the strip (deny=%v err=%v)", deny, err)
	}
}

func TestSandboxSetupDryRunTouchesNothing(t *testing.T) {
	_, markerErr := os.Stat(sbxMarkerPath())
	markerBefore := markerErr == nil

	res, err := SandboxSetup(SandboxSetupOptions{DryRun: true, Workspace: `C:\work\repo`})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !res.Supported {
		t.Fatal("the dedicated-user sandbox must be supported on Windows")
	}
	if res.User != sandboxAccountName || res.Group != sandboxGroupName {
		t.Fatalf("unexpected account names: %+v", res)
	}
	if res.ReLaunched {
		t.Fatal("dry-run must not elevate")
	}
	var sawDryRun bool
	for _, n := range res.Notes {
		if strings.Contains(n, "dry-run") {
			sawDryRun = true
		}
	}
	if !sawDryRun {
		t.Fatalf("dry-run note missing: %v", res.Notes)
	}
	if _, markerErr = os.Stat(sbxMarkerPath()); (markerErr == nil) != markerBefore {
		t.Fatal("dry-run changed the marker state")
	}
}

func TestSandboxSetupStatusShape(t *testing.T) {
	st := SandboxSetupStatus(`C:\work\repo`)
	if !st.Supported {
		t.Fatal("the dedicated-user sandbox must be supported on Windows")
	}
	if st.User != sandboxAccountName || st.Group != sandboxGroupName {
		t.Fatalf("unexpected account names: %+v", st)
	}
	if st.Ready && !(st.UserExists && st.GroupExists && st.MemberOfGroup && st.SetupVersion >= sandboxSetupVersion && st.SecretsPresent) {
		t.Fatalf("Ready=true but preconditions are not all satisfied: %+v", st)
	}
}

func TestSbxProgramDataUsesFycDirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ProgramData", base)
	if got, want := sbxProgramDataDir(), filepath.Join(base, "fyc"); got != want {
		t.Fatalf("sbxProgramDataDir() = %q, want %q", got, want)
	}
	if got, want := sbxRunnerDir(), filepath.Join(base, "fyc", "runner"); got != want {
		t.Fatalf("sbxRunnerDir() = %q, want %q", got, want)
	}
}

func TestSandboxACLBlocksConfigDirectoryOverride(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	t.Setenv("FYC_CONFIG_DIR", dir)
	plan := planSandboxACL(sandboxSpec{Cwd: root, AllowedRW: []string{root}})
	for _, p := range plan.Deny {
		if samePath(p, dir) {
			return
		}
	}
	t.Fatalf("sandbox deny-list does not include %q: %v", dir, plan.Deny)
}
