package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/charlievieth/fastwalk"
	"github.com/grafana/regexp"
)

// GrepMatch represents a single line match in a file.
type GrepMatch struct {
	Path    string // relative path from root
	Line    int    // 1-based line number
	Content string // the matching line (trimmed of newline)
}

// GrepOptions controls grep behaviour.
type GrepOptions struct {
	// Pattern is the regexp to search for.
	Pattern string

	// Root is the absolute path of the project directory to search in.
	// All file access is sandboxed to this directory via os.Root.
	Root string

	// File, if non-empty, restricts the search to this literal path relative to Root.
	// Include/exclude globs still apply; no directory walk is performed.
	File string

	// Workers is the number of parallel file-reading goroutines.
	// 0 means runtime.NumCPU().
	Workers int

	// CaseInsensitive wraps the pattern in (?i) if true.
	CaseInsensitive bool

	// IncludeGlob, if non-empty, only matches files whose names match this glob
	// (e.g. "*.go"). Supports brace expansion (e.g. "*.{js,jsx}").
	IncludeGlob string

	// ExcludeGlob, if non-empty, skips files whose names match this glob.
	// Supports brace expansion (e.g. "*.{test,spec}.go").
	ExcludeGlob string

	// IncludeBinary, if true, also searches files that look binary (i.e. they
	// contain a NUL byte within their first bytes). The default (false) skips
	// them, mirroring ripgrep.
	IncludeBinary bool

	// MaxFileSize skips files larger than this many bytes. 0 = no limit.
	MaxFileSize int64

	// MmapThreshold: files larger than this (bytes) use mmap-based search.
	// 0 means default (1 MB).
	MmapThreshold int64

	// IgnoreManager optionally provides pre-loaded ignore rules (e.g. from
	// .gitignore, .fycignore). If nil, a new one is loaded from Root.
	IgnoreManager *IgnoreManager

	// OnMatch is called for every match. It is called from the output
	// serialiser goroutine, so it is NOT called concurrently — no locking needed.
	// If nil, matches are collected and returned by GrepRun.
	OnMatch func(GrepMatch)
}

// GrepRun executes the grep and returns all matches (or nil if OnMatch is set).
func GrepRun(opts GrepOptions) ([]GrepMatch, error) {
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
	}

	// compile regexp
	pattern := opts.Pattern
	if opts.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	// open sandboxed root
	root, err := os.OpenRoot(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("cannot open root %q: %w", opts.Root, err)
	}
	defer root.Close()

	// load or reuse ignore manager
	ig := opts.IgnoreManager
	if ig == nil {
		ig = LoadIgnoreFiles(opts.Root)
	}

	// pre-expand globs once so the walk closure does zero allocations per file
	includeGlobs := expandGlobBraces(opts.IncludeGlob)
	excludeGlobs := expandGlobBraces(opts.ExcludeGlob)

	if opts.File != "" {
		if ig.isIgnored(filepath.Join(opts.Root, opts.File), opts.Root) ||
			!matchGlobs(includeGlobs, opts.File, opts.IncludeGlob == "") ||
			(opts.ExcludeGlob != "" && matchGlobs(excludeGlobs, opts.File, false)) {
			return nil, nil
		}
		info, err := root.Stat(opts.File)
		if err != nil {
			return nil, err
		}
		if opts.MaxFileSize > 0 && info.Size() > opts.MaxFileSize {
			return nil, nil
		}
		matches, err := grepFile(root, opts.File, re, opts)
		if opts.OnMatch != nil {
			for _, m := range matches {
				opts.OnMatch(m)
			}
			return nil, err
		}
		return matches, err
	}

	// channels
	pathCh := make(chan string, opts.Workers*32)
	matchCh := make(chan []GrepMatch, opts.Workers*8)

	// launch workers
	var workerWg sync.WaitGroup
	workerWg.Add(opts.Workers)
	for i := 0; i < opts.Workers; i++ {
		go func() {
			defer workerWg.Done()
			grepFileWorker(root, re, opts, pathCh, matchCh)
		}()
	}

	// close matchCh when all workers are done
	go func() {
		workerWg.Wait()
		close(matchCh)
	}()

	// output serialiser
	var allMatches []GrepMatch
	var outputWg sync.WaitGroup
	outputWg.Add(1)
	go func() {
		defer outputWg.Done()
		for matches := range matchCh {
			for _, m := range matches {
				if opts.OnMatch != nil {
					opts.OnMatch(m)
				} else {
					allMatches = append(allMatches, m)
				}
			}
		}
	}()

	// walk using fastwalk
	walkConf := fastwalk.Config{Follow: false}
	walkErr := fastwalk.Walk(&walkConf, opts.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ig.isIgnored(path, opts.Root) {
				return filepath.SkipDir
			}
			return nil
		}
		// Build relative path
		rel, err := filepath.Rel(opts.Root, path)
		if err != nil {
			return nil
		}
		// Check ignore rules
		if ig.isIgnored(path, opts.Root) {
			return nil
		}
		// Apply include/exclude globs against the relative path so that
		// patterns like "providers/*.go" work, not just bare filenames.
		if !matchGlobs(includeGlobs, rel, opts.IncludeGlob == "") {
			return nil
		}
		// Exclude matches against the relative path or the file name, so both
		// "providers/*.go" and bare patterns like "*_test.go" work (identical
		// matching to IncludeGlob).
		if opts.ExcludeGlob != "" && matchGlobs(excludeGlobs, rel, false) {
			return nil
		}
		// Check max file size
		if opts.MaxFileSize > 0 {
			info, err := d.Info()
			if err == nil && info.Size() > opts.MaxFileSize {
				return nil
			}
		}
		pathCh <- rel
		return nil
	})
	close(pathCh)

	outputWg.Wait()

	if walkErr != nil {
		return allMatches, fmt.Errorf("walk error: %w", walkErr)
	}
	return allMatches, nil
}

// ── file worker ──────────────────────────────────────────────────────────────

func grepFileWorker(root *os.Root, re *regexp.Regexp, opts GrepOptions, paths <-chan string, out chan<- []GrepMatch) {
	for rel := range paths {
		matches, err := grepFile(root, rel, re, opts)
		if err != nil {
			continue
		}
		if len(matches) > 0 {
			out <- matches
		}
	}
}

func grepFile(root *os.Root, rel string, re *regexp.Regexp, opts GrepOptions) ([]GrepMatch, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Skip files that look binary unless the caller explicitly opted in: this
	// mirrors ripgrep and avoids dumping binary blobs into the results.
	if !opts.IncludeBinary && isBinary(f) {
		return nil, nil
	}

	// Large files are scanned line-by-line with a bigger buffer instead of
	// being read fully into memory.
	mmapThreshold := opts.MmapThreshold
	if mmapThreshold == 0 {
		mmapThreshold = 1 << 20 // 1 MB default
	}
	if fi, _ := f.Stat(); fi != nil && fi.Size() > mmapThreshold {
		return grepFileLarge(f, rel, re)
	}

	var matches []GrepMatch
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if re.Match(line) {
			matches = append(matches, GrepMatch{
				Path:    rel,
				Line:    lineNum,
				Content: string(line),
			})
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return matches, err
	}
	return matches, nil
}

// grepFileLarge scans large files line-by-line using a buffered scanner to
// avoid loading the entire file into memory.
func grepFileLarge(f io.Reader, rel string, re *regexp.Regexp) ([]GrepMatch, error) {
	var matches []GrepMatch
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1*1024*1024), 16*1024*1024) // 1 MB buf, 16 MB max line

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if re.Match(scanner.Bytes()) {
			matches = append(matches, GrepMatch{
				Path:    rel,
				Line:    lineNum,
				Content: scanner.Text(),
			})
		}
	}
	return matches, scanner.Err()
}

// binarySniffBytes is how many leading bytes are inspected to decide whether a
// file is binary: a NUL byte within this prefix marks it as binary — the same
// heuristic used by ripgrep and git.
const binarySniffBytes = 8192

// isBinary reports whether f starts with a NUL byte within its first
// binarySniffBytes bytes. It rewinds f to the beginning so the caller can read
// the whole file afterwards; if the rewind fails it reports true so the caller
// skips the file rather than reading it from a non-zero offset.
func isBinary(f *os.File) bool {
	buf := make([]byte, binarySniffBytes)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return false
	}
	looksBinary := bytes.IndexByte(buf[:n], 0) >= 0
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return true
	}
	return looksBinary
}

// ── glob helpers ─────────────────────────────────────────────────────────────

// expandGlobBraces expands brace groups in a glob pattern into a slice of
// simple patterns that filepath.Match can handle.
// e.g. "*.{js,jsx}"        → ["*.js", "*.jsx"]
//
//	"src/{a,b}.{ts,tsx}" → ["src/a.ts", "src/a.tsx", "src/b.ts", "src/b.tsx"]
//
// An empty glob returns [""] so callers can distinguish "no filter" from
// "filter that matches nothing".
func expandGlobBraces(glob string) []string {
	start := strings.Index(glob, "{")
	if start == -1 {
		return []string{glob}
	}
	end := strings.Index(glob[start:], "}")
	if end == -1 {
		// malformed brace: treat literally
		return []string{glob}
	}
	end += start

	prefix := glob[:start]
	suffix := glob[end+1:]
	parts := strings.Split(glob[start+1:end], ",")

	var result []string
	for _, p := range parts {
		// recurse to handle multiple brace groups
		result = append(result, expandGlobBraces(prefix+p+suffix)...)
	}
	return result
}

// matchGlobs reports whether name matches any of the pre-expanded glob patterns.
// emptyMeansAll: when true (IncludeGlob was ""), every file is accepted.
func matchGlobs(globs []string, name string, emptyMeansAll bool) bool {
	if emptyMeansAll {
		return true
	}
	base := filepath.Base(name)
	for _, g := range globs {
		if ok, _ := filepath.Match(g, name); ok {
			return true
		}
		// Fallback: match against basename so "*.go" matches "pkg/foo.go"
		if ok, _ := filepath.Match(g, base); ok {
			return true
		}
	}
	return false
}
