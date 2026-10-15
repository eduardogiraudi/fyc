// Package codexversion resolves the upstream Codex compatibility version over HTTP.
package codexversion

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BuildVersion can be supplied through -X fuckyourcli/codexversion.BuildVersion.
// Plain go builds use the tracked VERSION file instead.
var BuildVersion string

//go:embed VERSION
var embeddedVersion string

// Bundled returns the build-time fallback, independent of fyc's release version.
func Bundled() string {
	if Valid(BuildVersion) {
		return BuildVersion
	}
	return strings.TrimSpace(embeddedVersion)
}

// Valid accepts only canonical stable release versions, never prereleases or local builds.
func Valid(version string) bool {
	_, ok := components(version)
	return ok
}

func components(version string) ([3]uint64, bool) {
	var result [3]uint64
	parts := strings.Split(version, ".")
	if len(parts) != len(result) {
		return result, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return result, false
			}
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, result != [3]uint64{}
}

// Newer returns the newer valid stable version; an invalid candidate is ignored.
func Newer(current, candidate string) string {
	a, aOK := components(current)
	b, bOK := components(candidate)
	if !bOK {
		return current
	}
	if !aOK {
		return candidate
	}
	for i := range a {
		if b[i] > a[i] {
			return candidate
		}
		if b[i] < a[i] {
			return current
		}
	}
	return current
}

const (
	NPMURL           = "https://registry.npmjs.org/@openai/codex/latest"
	GitHubURL        = "https://api.github.com/repos/openai/codex/releases/latest"
	FetchTimeout     = 4 * time.Second
	maxResponseBytes = 1 << 20
)

// Fetcher has injectable HTTP dependencies so tests never need the public internet.
// Its zero value uses the official endpoints and a four-second overall deadline.
type Fetcher struct {
	Client    *http.Client
	NPMURL    string
	GitHubURL string
	Timeout   time.Duration
}

// Latest fetches the stable release from npm, then GitHub if npm fails.
// No credentials, external commands, or package managers are used.
func (f Fetcher) Latest(ctx context.Context) (string, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = FetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	npmURL, githubURL := f.NPMURL, f.GitHubURL
	if npmURL == "" {
		npmURL = NPMURL
	}
	if githubURL == "" {
		githubURL = GitHubURL
	}
	// Reserve half the budget for the second source even if the first hangs.
	npmCtx, npmCancel := context.WithTimeout(ctx, timeout/2)
	data, npmErr := f.get(npmCtx, npmURL)
	npmCancel()
	if npmErr == nil {
		var info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		npmErr = json.Unmarshal(data, &info)
		if npmErr == nil && info.Name == "@openai/codex" && Valid(info.Version) {
			return info.Version, nil
		}
		if npmErr == nil {
			npmErr = errors.New("invalid npm Codex stable version")
		}
	}
	data, githubErr := f.get(ctx, githubURL)
	if githubErr == nil {
		var info struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		githubErr = json.Unmarshal(data, &info)
		version, ok := strings.CutPrefix(info.Tag, "rust-v")
		if githubErr == nil && ok && !info.Draft && !info.Prerelease && Valid(version) {
			return version, nil
		}
		if githubErr == nil {
			githubErr = errors.New("invalid GitHub Codex stable release")
		}
	}
	return "", fmt.Errorf("Codex version lookup failed: npm: %v; GitHub: %w", npmErr, githubErr)
}

func (f Fetcher) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fuckyourcli-codex-version")
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("version response too large")
	}
	return data, nil
}
