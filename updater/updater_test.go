package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	got := parseChecksums("aaa  one.tar.gz\nbbb *two.zip\n\nnonsense\n")
	if got["one.tar.gz"] != "aaa" || got["two.zip"] != "bbb" {
		t.Fatalf("unexpected map: %v", got)
	}
	if _, ok := got["nonsense"]; ok {
		t.Fatal("a line without a digest must be ignored")
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.1.1", "0.1.0", true},
		{"0.1.0", "0.1.0", false},
		{"1.0.0", "0.9.9", true},
		{"0.2.0", "0.10.0", false}, // numeric, not lexical
		{"0.1.0", "dev", true},     // a dev build always gets the release
		{"0.1.0", "", true},
		{"0.1.0-rc1", "0.1.0", false},
		{"0.1.0", "0.1.0-rc1", true},
		{"garbage", "0.1.0", false},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestPickAsset(t *testing.T) {
	assets := []Asset{
		{Name: "checksums.txt", ID: 1},
		{Name: "fuckyourcli_0.1.1_linux_amd64.tar.gz", ID: 2},
		{Name: "fuckyourcli_0.1.1_linux_arm64.tar.gz", ID: 3},
		{Name: "fuckyourcli_0.1.1_darwin_amd64.tar.gz", ID: 4},
		{Name: "fuckyourcli_0.1.1_darwin_arm64.tar.gz", ID: 5},
		{Name: "fuckyourcli_0.1.1_windows_amd64.zip", ID: 6},
		{Name: "fuckyourcli_0.1.1_Linux_x86_64.tar.gz", ID: 7}, // "friendly" names
		{Name: "fuckyourcli_0.1.1_Darwin_arm64.tar.gz", ID: 8},
	}
	cases := []struct {
		goos, goarch string
		wantID       int64
	}{
		{"linux", "amd64", 2},
		{"linux", "arm64", 3},
		{"darwin", "amd64", 4},
		{"windows", "amd64", 6},
		{"windows", "arm64", 0}, // no arm64 zip in the fixture
		{"plan9", "amd64", 0},
	}
	for _, c := range cases {
		a, ok := pickAsset(assets, c.goos, c.goarch)
		if c.wantID == 0 {
			if ok {
				t.Errorf("pickAsset(%s/%s) unexpectedly matched %s", c.goos, c.goarch, a.Name)
			}
			continue
		}
		if !ok || a.ID != c.wantID {
			t.Errorf("pickAsset(%s/%s) = %+v ok=%v, want id %d", c.goos, c.goarch, a, ok, c.wantID)
		}
	}

	// "friendly" names only: still resolvable.
	friendly := []Asset{{Name: "fuckyourcli_0.1.1_Linux_x86_64.tar.gz", ID: 9}}
	if a, ok := pickAsset(friendly, "linux", "amd64"); !ok || a.ID != 9 {
		t.Fatalf("friendly naming not matched: %+v ok=%v", a, ok)
	}
}

func TestCheckReportsAvailableUpdate(t *testing.T) {
	assetName, _, _ := hostAsset()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("public release request must not authenticate: Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("metadata Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.1","assets":[{"name":"` + assetName + `","id":2,"browser_download_url":"https://example/x"}]}`))
	}))
	defer srv.Close()

	s, err := Check(context.Background(), Options{Repo: "o/r", APIBase: srv.URL}, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if s.UpToDate || s.Latest != "0.1.1" || s.AssetName != assetName {
		t.Fatalf("unexpected status: %+v", s)
	}
}

func TestCheckReleaseNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	for _, version := range []string{"", "0.1.0"} {
		t.Run("version="+version, func(t *testing.T) {
			_, err := Check(context.Background(), Options{Repo: "o/r", APIBase: srv.URL, Version: version}, "0.1.0")
			if err == nil || err.Error() != "release not found in o/r (HTTP 404)" {
				t.Fatalf("expected a release-not-found error, got %v", err)
			}
		})
	}
}

func TestCheckRateLimited(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(status)
			}))
			defer srv.Close()

			_, err := Check(context.Background(), Options{Repo: "o/r", APIBase: srv.URL}, "0.1.0")
			if err == nil || !strings.Contains(err.Error(), "rate limit") {
				t.Fatalf("expected a rate-limit error, got %v", err)
			}
		})
	}
}

// A 403 that is not a rate limit must keep the generic error, without the hint.
func TestCheckForbiddenIsNotRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := Check(context.Background(), Options{Repo: "o/r", APIBase: srv.URL}, "0.1.0")
	if err == nil || strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected the generic error, got %v", err)
	}
}

func TestDownloadRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newClient(Options{Repo: "o/r", APIBase: srv.URL})
	_, err := c.download(context.Background(), Asset{Name: "x", ID: 2})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected a rate-limit error, got %v", err)
	}
}

// TestUpdateEndToEnd drives the whole path — release metadata, asset download
// through the API, checksum verification, extraction and replacement — against
// a fake GitHub.
func TestUpdateEndToEnd(t *testing.T) {
	assetName, binName, asZip := hostAsset()

	payload := []byte("#!/bin/sh\necho new\n")
	archive := makeArchive(t, binName, payload, asZip)
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"

	var sawAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("public release request must not authenticate: Authorization = %q", got)
		}
		expectedAccept := "application/octet-stream"
		if r.URL.Path == "/repos/o/r/releases/latest" {
			expectedAccept = "application/vnd.github+json"
		}
		if got := r.Header.Get("Accept"); got != expectedAccept {
			t.Errorf("%s: Accept = %q, want %q", r.URL.Path, got, expectedAccept)
		}
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.1","assets":[` +
				`{"name":"checksums.txt","id":1},{"name":"` + assetName + `","id":2}]}`))
		case "/repos/o/r/releases/assets/1":
			_, _ = w.Write([]byte(checksums))
		case "/repos/o/r/releases/assets/2":
			sawAccept = r.Header.Get("Accept")
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, binName)
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := Options{Repo: "o/r", APIBase: srv.URL, TargetPath: target}
	s, path, err := Update(context.Background(), opts, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !s.ChecksumVerified {
		t.Error("the checksum was not verified")
	}
	if path != target {
		t.Fatalf("replaced %q, want %q", path, target)
	}
	if sawAccept != "application/octet-stream" {
		t.Errorf("asset download Accept = %q, want application/octet-stream", sawAccept)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("binary = %q, want %q", got, payload)
	}
	// Windows has no executable bit: os.Chmod only toggles the read-only flag,
	// so the mode check is meaningful on Unix only.
	if fi, err := os.Stat(target); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}

	// A second run is a no-op: the installed version is already the latest.
	s2, path2, err := Update(context.Background(), opts, "0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !s2.UpToDate || path2 != "" {
		t.Fatalf("expected an up-to-date no-op, got %+v path=%q", s2, path2)
	}
}

func TestUpdateRejectsChecksumMismatch(t *testing.T) {
	assetName, binName, asZip := hostAsset()
	archive := makeArchive(t, binName, []byte("payload"), asZip)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.1","assets":[` +
				`{"name":"checksums.txt","id":1},{"name":"` + assetName + `","id":2}]}`))
		case "/repos/o/r/releases/assets/1":
			_, _ = w.Write([]byte("deadbeef0000000000000000000000000000000000000000000000000000000000  " + assetName + "\n"))
		case "/repos/o/r/releases/assets/2":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), binName)
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := Update(context.Background(), Options{Repo: "o/r", APIBase: srv.URL, TargetPath: target}, "0.1.0")
	if err == nil {
		t.Fatal("a tampered archive must be rejected")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("the target was modified despite the mismatch: %q", got)
	}
}

// An explicitly requested version is installed even when it is not newer than
// the running binary: pinning/downgrading must not be swallowed by the
// "already up to date" shortcut.
func TestUpdateRequestedVersionIsNotSkipped(t *testing.T) {
	assetName, binName, asZip := hostAsset()
	payload := []byte("pinned")
	archive := makeArchive(t, binName, payload, asZip)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/tags/v0.1.0":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.0","assets":[{"name":"` + assetName + `","id":2}]}`))
		case "/repos/o/r/releases/assets/2":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), binName)
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Running 0.1.1, asking for the older 0.1.0.
	opts := Options{Repo: "o/r", APIBase: srv.URL, Version: "0.1.0", TargetPath: target}
	s, path, err := Update(context.Background(), opts, "0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Requested {
		t.Error("the status should be flagged as an explicit request")
	}
	if path != target {
		t.Fatalf("the requested version was not installed (path=%q)", path)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, payload) {
		t.Fatalf("binary = %q, want %q", got, payload)
	}
}

func TestExtractBinaryTarGz(t *testing.T) {
	archive := makeArchive(t, "fuckyourcli", []byte("bin"), false)
	got, err := extractBinary(archive, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bin" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractBinaryZip(t *testing.T) {
	archive := makeArchive(t, "fuckyourcli.exe", []byte("bin"), true)
	got, err := extractBinary(archive, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bin" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractBinaryMissing(t *testing.T) {
	archive := makeArchive(t, "README.md", []byte("nope"), false)
	if _, err := extractBinary(archive, "linux"); err == nil {
		t.Fatal("an archive without the binary must fail")
	}
}

// hostAsset returns the release asset name, the binary name inside it and
// whether the archive is a zip, for the platform running the test: the fixtures
// must match wherever CI runs (linux, macOS, Windows).
func hostAsset() (assetName, binName string, asZip bool) {
	binName, ext := "fuckyourcli", ".tar.gz"
	if runtime.GOOS == "windows" {
		binName, ext, asZip = "fuckyourcli.exe", ".zip", true
	}
	return "fuckyourcli_0.1.1_" + runtime.GOOS + "_" + runtime.GOARCH + ext, binName, asZip
}

func makeArchive(t *testing.T, name string, content []byte, asZip bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if asZip {
		zw := zip.NewWriter(&buf)
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
