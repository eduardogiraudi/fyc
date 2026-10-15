// Package updater implements `fuckyourcli status` / `fuckyourcli update`: it
// resolves a GitHub release, downloads the archive for the current platform and
// swaps the running binary in place.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultRepo is the project repository, owner/name.
	DefaultRepo = "eduardogiraudi/fyc"
	// DefaultAPIBase is the public GitHub API. Overridable for GitHub
	// Enterprise and for tests.
	DefaultAPIBase = "https://api.github.com"

	checksumsName = "checksums.txt"
	maxBinarySize = 256 << 20
)

// Options configures a release lookup.
type Options struct {
	Repo    string // owner/name (empty = DefaultRepo)
	Version string // tag with or without the "v" prefix (empty = latest)
	APIBase string // empty = DefaultAPIBase
	// TargetPath overrides the file that gets replaced. Empty means the running
	// binary: tests and "update another installation" use it.
	TargetPath string
}

// Asset is one file attached to a release.
type Asset struct {
	Name string
	ID   int64
	Size int64
	// URL is the human-facing browser_download_url. It is only used for
	// display: downloading goes through the API asset endpoint.
	URL string
}

// Release is the subset of a GitHub release we care about.
type Release struct {
	Tag    string
	Assets []Asset
}

// Status is the outcome of a check.
type Status struct {
	Current   string // running version
	Latest    string // published version, without the "v" prefix
	AssetName string
	Asset     Asset
	UpToDate  bool
	// Requested is true when the caller asked for a specific version: such a
	// request is honoured even when it is not newer than the running binary
	// (pinning and downgrading).
	Requested bool
	// ChecksumVerified is set by Update once the SHA-256 matched.
	ChecksumVerified bool

	Release *Release
}

// Check resolves the wanted release and picks the asset for this platform. It
// never touches the filesystem.
func Check(ctx context.Context, opts Options, current string) (*Status, error) {
	c := newClient(opts)
	rel, err := c.release(ctx, strings.TrimSpace(c.opts.Version))
	if err != nil {
		return nil, err
	}
	asset, ok := pickAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s/%s archive (assets: %s)",
			rel.Tag, runtime.GOOS, runtime.GOARCH, assetNames(rel.Assets))
	}
	s := &Status{
		Current:   current,
		Latest:    strings.TrimPrefix(rel.Tag, "v"),
		AssetName: asset.Name,
		Asset:     asset,
		Requested: c.opts.Version != "",
		Release:   rel,
	}
	s.UpToDate = !isNewer(s.Latest, current)
	return s, nil
}

// Update performs Check and, when a newer release exists, verifies its SHA-256
// (against the release checksums.txt) and replaces the running binary. It
// returns the status and the path that was replaced ("" when nothing changed).
func Update(ctx context.Context, opts Options, current string) (*Status, string, error) {
	s, err := Check(ctx, opts, current)
	if err != nil {
		return nil, "", err
	}
	// An explicit version is always installed, even when it is not newer.
	if s.UpToDate && !s.Requested {
		return s, "", nil
	}

	c := newClient(opts)
	sums, err := c.checksums(ctx, s.Release)
	if err != nil {
		return nil, "", err
	}
	archive, err := c.download(ctx, s.Asset)
	if err != nil {
		return nil, "", err
	}
	if len(sums) > 0 {
		want, ok := sums[s.AssetName]
		if !ok {
			return nil, "", fmt.Errorf("checksums.txt has no entry for %s", s.AssetName)
		}
		sum := sha256.Sum256(archive)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
			return nil, "", fmt.Errorf("checksum mismatch for %s", s.AssetName)
		}
		s.ChecksumVerified = true
	}

	bin, err := extractBinary(archive, runtime.GOOS)
	if err != nil {
		return nil, "", err
	}
	target := strings.TrimSpace(opts.TargetPath)
	if target == "" {
		p, err := currentBinaryPath()
		if err != nil {
			return nil, "", err
		}
		target = p
	}
	if err := replaceBinary(target, bin); err != nil {
		return nil, "", err
	}
	return s, target, nil
}

// ── client ───────────────────────────────────────────────────────────────────

type client struct {
	opts Options
	http *http.Client
}

func newClient(opts Options) *client {
	if opts.Repo == "" {
		opts.Repo = DefaultRepo
	}
	if opts.APIBase == "" {
		opts.APIBase = DefaultAPIBase
	}
	opts.Repo = strings.Trim(opts.Repo, "/")
	opts.APIBase = strings.TrimSuffix(opts.APIBase, "/")
	return &client{opts: opts, http: &http.Client{Timeout: 10 * time.Minute}}
}

func (c *client) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fuckyourcli-updater")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return c.http.Do(req)
}

// rateLimited reports whether GitHub rejected the request because the primary
// rate limit is exhausted (unauthenticated requests: 60/hour per IP). The
// X-RateLimit-Remaining check keeps unrelated 403s from getting this hint.
func rateLimited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}

func rateLimitError(repo string, resp *http.Response) error {
	return fmt.Errorf("GitHub API rate limit reached for %s (HTTP %d): unauthenticated requests are limited per IP address, try again later", repo, resp.StatusCode)
}

type releaseJSON struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		ID                 int64  `json:"id"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (c *client) release(ctx context.Context, tag string) (*Release, error) {
	endpoint := c.opts.APIBase + "/repos/" + c.opts.Repo + "/releases/latest"
	if tag != "" {
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		endpoint = c.opts.APIBase + "/repos/" + c.opts.Repo + "/releases/tags/" + url.PathEscape(tag)
	}
	resp, err := c.get(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("release not found in %s (HTTP 404)", c.opts.Repo)
	case rateLimited(resp):
		return nil, rateLimitError(c.opts.Repo, resp)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: %s", c.opts.Repo, resp.Status)
	}

	var raw releaseJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("cannot parse the release metadata: %w", err)
	}
	if raw.TagName == "" {
		return nil, fmt.Errorf("the release metadata has no tag_name")
	}
	rel := &Release{Tag: raw.TagName}
	for _, a := range raw.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, ID: a.ID, Size: a.Size, URL: a.BrowserDownloadURL})
	}
	return rel, nil
}

// download fetches a public release asset through the GitHub API.
func (c *client) download(ctx context.Context, a Asset) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/assets/%d", c.opts.APIBase, c.opts.Repo, a.ID)
	resp, err := c.get(ctx, endpoint, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if rateLimited(resp) {
		return nil, rateLimitError(c.opts.Repo, resp)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot download %s: %s", a.Name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBinarySize))
}

// checksums returns the "<sha256> <name>" map of the release checksums.txt, or
// nil (no error) when the release does not ship one.
func (c *client) checksums(ctx context.Context, rel *Release) (map[string]string, error) {
	var asset Asset
	found := false
	for _, a := range rel.Assets {
		if a.Name == checksumsName {
			asset, found = a, true
			break
		}
	}
	if !found {
		return nil, nil
	}
	data, err := c.download(ctx, asset)
	if err != nil {
		return nil, err
	}
	return parseChecksums(string(data)), nil
}

// parseChecksums reads the "<hash>  <file>" lines, tolerating the " *file" form
// of some tools.
func parseChecksums(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		out[name] = strings.ToLower(f[0])
	}
	return out
}

// ── asset selection ──────────────────────────────────────────────────────────

// pickAsset returns the archive for the given platform. Both naming conventions
// are accepted, because goreleaser's name template has used both:
// fuckyourcli_1.0.0_linux_amd64.tar.gz (Go names) and
// fuckyourcli_1.0.0_Linux_x86_64.tar.gz ("friendly" names).
func pickAsset(assets []Asset, goos, goarch string) (Asset, bool) {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	osTokens := []string{goos}
	switch goos {
	case "linux":
		osTokens = append(osTokens, "Linux")
	case "darwin":
		osTokens = append(osTokens, "Darwin")
	case "windows":
		osTokens = append(osTokens, "Windows")
	}
	archTokens := []string{goarch}
	switch goarch {
	case "amd64":
		archTokens = append(archTokens, "x86_64")
	case "arm64":
		archTokens = append(archTokens, "aarch64")
	}
	// A suffix match on the exact "_<os>_<arch>" pair avoids picking another
	// platform that merely shares one of the tokens.
	for _, a := range assets {
		if !strings.HasSuffix(a.Name, ext) {
			continue
		}
		stem := strings.TrimSuffix(a.Name, ext)
		for _, o := range osTokens {
			for _, ar := range archTokens {
				if strings.HasSuffix(stem, "_"+o+"_"+ar) {
					return a, true
				}
			}
		}
	}
	return Asset{}, false
}

func assetNames(assets []Asset) string {
	if len(assets) == 0 {
		return "none"
	}
	names := make([]string, 0, len(assets))
	for _, a := range assets {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
