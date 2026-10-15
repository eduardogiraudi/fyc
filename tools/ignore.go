package tools

import (
	gitignore "github.com/sabhiram/go-gitignore"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charlievieth/fastwalk"
)

// ignoreFile pairs a compiled matcher with the directory it applies to.
type ignoreFile struct {
	matcher         *gitignore.GitIgnore
	scopeDir        string // directory containing the ignore file, relative to restrictedRoot
	scopeAbs        string // precomputed absolute scope directory
	normalizedScope string // precomputed scopeAbs + separator
}

// IgnoreManager holds scoped ignore rules for .gitignore, .dockerignore, and .fycignore files.
type IgnoreManager struct {
	files         []ignoreFile
	gitDir        string // precomputed: restrictedRoot + "/.git"
	gitDirWithSep string // precomputed: gitDir + separator
}

// LoadIgnoreFiles walks the restricted root, collecting all ignore files
// and compiling them into matchers. Each matcher is scoped to the directory
// where its ignore file resides, so that a .gitignore in a subdirectory
// (e.g., dist/.gitignore with "*") only applies within that subdirectory.
func LoadIgnoreFiles(restrictedRoot string) *IgnoreManager {
	gitDir := filepath.Join(restrictedRoot, ".git")
	im := &IgnoreManager{
		gitDir:        gitDir,
		gitDirWithSep: gitDir + string(filepath.Separator),
	}
	ignoreNames := []string{".gitignore", ".dockerignore", ".fycignore"}
	ignoreSet := make(map[string]struct{}, len(ignoreNames))
	for _, n := range ignoreNames {
		ignoreSet[n] = struct{}{}
	}

	var mu sync.Mutex
	conf := fastwalk.Config{Follow: false}
	fastwalk.Walk(&conf, restrictedRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := ignoreSet[d.Name()]; !ok {
			return nil
		}
		matcher, err := gitignore.CompileIgnoreFile(path)
		if err != nil {
			return nil
		}
		relPath, _ := filepath.Rel(restrictedRoot, path)
		scopeDir := filepath.Dir(relPath)
		if scopeDir == "." {
			scopeDir = ""
		}
		scopeAbs := restrictedRoot
		if scopeDir != "" {
			scopeAbs = filepath.Join(restrictedRoot, scopeDir)
		}
		entry := ignoreFile{
			matcher:         matcher,
			scopeDir:        scopeDir,
			scopeAbs:        scopeAbs,
			normalizedScope: strings.TrimSuffix(scopeAbs, string(filepath.Separator)) + string(filepath.Separator),
		}
		mu.Lock()
		im.files = append(im.files, entry)
		mu.Unlock()
		return nil
	})
	return im
}

// isIgnored checks whether a given absolute path matches any of the scoped
// ignore rules. Each rule is only applied if the path is within that rule's
// scope directory, and the path is made relative to the scope before matching.
func (im *IgnoreManager) isIgnored(absPath string, restrictedRoot string) bool {
	// Always block access to .git directories.
	// Guard against a zero-value IgnoreManager: an empty gitDirWithSep would
	// make HasPrefix match every path and deny the whole workspace.
	if im.gitDirWithSep != "" && (absPath == im.gitDir || strings.HasPrefix(absPath, im.gitDirWithSep)) {
		return true
	}
	// Always block access to ignore files themselves (.gitignore, .fycignore, .dockerignore)
	baseName := filepath.Base(absPath)
	if baseName == ".gitignore" || baseName == ".fycignore" || baseName == ".dockerignore" {
		return true
	}
	// Probe the filesystem only when a directory-only rule could match.
	var checkedDir, dir bool
	isDir := func() bool {
		if !checkedDir {
			info, err := os.Stat(absPath)
			dir = err == nil && info.IsDir()
			checkedDir = true
		}
		return dir
	}
	for _, f := range im.files {
		// Skip if path is outside this rule's scope
		if !strings.HasPrefix(absPath, f.normalizedScope) && absPath != f.scopeAbs {
			continue
		}
		// Slice the prefix directly instead of filepath.Rel to avoid allocation
		relToScope := filepath.ToSlash(absPath[len(f.scopeAbs):])
		relToScope = strings.TrimPrefix(relToScope, "/")

		if f.matcher.MatchesPath(relToScope) || (f.matcher.MatchesPath(relToScope+"/") && isDir()) {
			return true
		}
		// Also try subpath matching within the scope (e.g., "foo" should also
		// be matched by a pattern like "*" at the scope level).
		for i, c := range relToScope {
			if c == '/' {
				sub := relToScope[i+1:]
				if f.matcher.MatchesPath(sub) || (f.matcher.MatchesPath(sub+"/") && isDir()) {
					return true
				}
			}
		}
	}
	return false
}

func (im *IgnoreManager) IsIgnored(absPath string, restrictedRoot string) bool {
	return im.isIgnored(absPath, restrictedRoot)
}
