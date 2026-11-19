package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// Prepare snapshots a local directory or downloads a GitHub/HTTPS ZIP source.
// The returned bytes are the exact bytes shown for approval and installed later.
func Prepare(ctx context.Context, source, ref, subdir string) (*Package, error) {
	var files map[string]File
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		if ref != "" {
			return nil, fmt.Errorf("--ref is only supported for GitHub repositories")
		}
		files, err = snapshot(source, true)
		if err != nil {
			return nil, err
		}
	} else {
		remote, stripRoot, err := sourceURL(source, ref)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", remote, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{
			Timeout: 90 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" || req.URL.User != nil {
					return fmt.Errorf("unsafe download redirect")
				}
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("plugin download: HTTP %d", resp.StatusCode)
		}
		b, err := readLimited(resp.Body, maxTotal)
		if err != nil {
			return nil, err
		}
		files, err = unzip(b, stripRoot)
		if err != nil {
			return nil, err
		}
	}
	if subdir != "" {
		subdir = strings.TrimPrefix(subdir, "./")
		if !safePath(subdir) {
			return nil, fmt.Errorf("unsafe --subdir %q", subdir)
		}
		selected := map[string]File{}
		for k, v := range files {
			if strings.HasPrefix(k, subdir+"/") {
				selected[strings.TrimPrefix(k, subdir+"/")] = v
			}
		}
		files = selected
	}
	return parse(files)
}

func sourceURL(source, ref string) (string, bool, error) {
	if !strings.Contains(source, "://") && !strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "/") {
		parts := strings.Split(source, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			source = "https://github.com/" + source
		}
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", false, fmt.Errorf("source must be a local plugin directory, owner/repo, GitHub repository URL, or HTTPS ZIP URL")
	}
	if u.Host == "github.com" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 2 {
			if u.RawQuery != "" {
				return "", false, fmt.Errorf("GitHub repository URL must not contain a query")
			}
			repo := strings.TrimSuffix(parts[1], ".git")
			if parts[0] == "" || repo == "" {
				return "", false, fmt.Errorf("invalid GitHub repository")
			}
			if ref == "" {
				ref = "HEAD"
			}
			return "https://codeload.github.com/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(repo) + "/zip/" + url.PathEscape(ref), true, nil
		}
	}
	if ref != "" {
		return "", false, fmt.Errorf("--ref is only supported for GitHub repositories")
	}
	return u.String(), false, nil
}

func unzip(b []byte, stripRoot bool) (map[string]File, error) {
	archive, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("source is not a ZIP archive: %w", err)
	}
	if len(archive.File) > maxFiles {
		return nil, fmt.Errorf("archive exceeds file limit")
	}
	files := map[string]File{}
	seen := map[string]bool{}
	rootName := ""
	total := 0
	for _, f := range archive.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !safePath(name) {
			return nil, fmt.Errorf("unsafe archive path %q", f.Name)
		}
		if stripRoot {
			first, rest, found := strings.Cut(name, "/")
			if rootName == "" {
				rootName = first
			}
			if first != rootName {
				return nil, fmt.Errorf("archive has multiple roots")
			}
			if !found {
				if f.FileInfo().IsDir() {
					continue
				}
				return nil, fmt.Errorf("archive file outside root")
			}
			name = rest
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("archive symlinks/special files are not allowed: %s", name)
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("duplicate archive path %s", name)
		}
		seen[strings.ToLower(name)] = true
		if f.UncompressedSize64 > maxFile {
			return nil, fmt.Errorf("archive file exceeds size limit")
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := readLimited(r, maxFile)
		r.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxTotal {
			return nil, fmt.Errorf("archive exceeds size limit")
		}
		if strings.Contains("/"+name+"/", "/.git/") {
			continue
		}
		files[path.Clean(name)] = File{data, f.Mode()&0o111 != 0}
	}
	return files, nil
}
