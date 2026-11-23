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
	fyc             bool
	scopeDir        string // directory containing the ignore file, relative to restrictedRoot
	scopeAbs        string // precomputed absolute scope directory (resolved spelling)
	normalizedScope string // precomputed scopeAbs + separator
	// altScope/altNormalized keep the spelling the root was given with when it
	// differs from the resolved one. Paths reach isIgnored in either spelling
	// (a caller's RUNNER~1, the workspace's runneradmin), and a rule that only
	// knows one of them silently stops protecting anything.
	altScope      string
	altNormalized string
}

// scopeFor returns the spelling of the rule's scope that contains absPath.
func (f ignoreFile) scopeFor(absPath string) (string, bool) {
	if absPath == f.scopeAbs || strings.HasPrefix(absPath, f.normalizedScope) {
		return f.scopeAbs, true
	}
	if f.altScope != f.scopeAbs && (absPath == f.altScope || strings.HasPrefix(absPath, f.altNormalized)) {
		return f.altScope, true
	}
	return "", false
}

// IgnoreManager holds scoped .gitignore and .fycignore rules.
type IgnoreManager struct {
	files         []ignoreFile
	gitDir        string // precomputed: primary root + "/.git"
	gitDirWithSep string // precomputed: gitDir + separator
	// extraGitDirs holds "<root>/.git" for every additional allowed root (/cd):
	// repository metadata stays unreachable there too.
	extraGitDirs []string
}

// LoadIgnoreFiles collects both ignore families. A .fycignore replaces all
// applicable git rules within its subtree, even when the fyc file is empty.
func LoadIgnoreFiles(restrictedRoot string) *IgnoreManager {
	return LoadWorkspaceIgnore([]string{restrictedRoot})
}

// LoadWorkspaceIgnore collects both ignore families for every allowed root. Each
// rule keeps its absolute scope, so the rules of one root never apply to
// another; the .git guard covers all of them.
func LoadWorkspaceIgnore(roots []string) *IgnoreManager {
	im := &IgnoreManager{}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		// Rules are matched against paths the workspace already resolved, so the
		// scopes must be spelled the same way: a short Windows name (RUNNER~1) or
		// an unresolved macOS /var would otherwise never match anything and every
		// ignore rule would silently stop applying. The caller's spelling is kept
		// as the alternative, because tests and a few callers still walk the tree
		// from the path they were given.
		given, err := filepath.Abs(root)
		if err != nil {
			given = root
		}
		root = resolveRoot(root)
		if root == "" {
			continue
		}
		altGit := filepath.Join(given, ".git")
		gitDir := filepath.Join(root, ".git")
		if im.gitDir == "" {
			im.gitDir = gitDir
			im.gitDirWithSep = gitDir + string(filepath.Separator)
		} else {
			im.extraGitDirs = append(im.extraGitDirs, gitDir)
		}
		// The same .git under the caller's spelling: a path built from the root as
		// it was given must stay blocked too.
		if altGit != gitDir {
			im.extraGitDirs = append(im.extraGitDirs, altGit)
		}
		im.collect(root, given)
	}
	return im
}

// collect walks one root and appends the ignore files it finds. given is the
// root as the caller spelled it, kept as the alternative scope spelling.
func (im *IgnoreManager) collect(root, given string) {
	var mu sync.Mutex
	conf := fastwalk.Config{Follow: false}
	fastwalk.Walk(&conf, root, func(path string, d fs.DirEntry, err error) error {
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
		if d.Name() != ".gitignore" && d.Name() != ".fycignore" {
			return nil
		}
		matcher, err := gitignore.CompileIgnoreFile(path)
		if err != nil {
			return nil
		}
		relPath, _ := filepath.Rel(root, path)
		scopeDir := filepath.Dir(relPath)
		if scopeDir == "." {
			scopeDir = ""
		}
		scopeAbs := root
		altScope := given
		if scopeDir != "" {
			scopeAbs = filepath.Join(root, scopeDir)
			altScope = filepath.Join(given, scopeDir)
		}
		entry := ignoreFile{
			matcher:         matcher,
			fyc:             d.Name() == ".fycignore",
			scopeDir:        scopeDir,
			scopeAbs:        scopeAbs,
			normalizedScope: strings.TrimSuffix(scopeAbs, string(filepath.Separator)) + string(filepath.Separator),
			altScope:        altScope,
			altNormalized:   strings.TrimSuffix(altScope, string(filepath.Separator)) + string(filepath.Separator),
		}
		mu.Lock()
		im.files = append(im.files, entry)
		mu.Unlock()
		return nil
	})
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
	// The additional roots allowed with /cd keep their repository metadata
	// unreachable too, exactly like the primary root.
	for _, gitDir := range im.extraGitDirs {
		if absPath == gitDir || strings.HasPrefix(absPath, gitDir+string(filepath.Separator)) {
			return true
		}
	}
	// Always block access to the rule files themselves.
	baseName := filepath.Base(absPath)
	if baseName == ".gitignore" || baseName == ".fycignore" {
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
	// A local fyc file replaces inherited git rules, not just a neighbouring file.
	useFYC := false
	for _, f := range im.files {
		if !f.fyc {
			continue
		}
		if _, ok := f.scopeFor(absPath); ok {
			useFYC = true
			break
		}
	}
	// Keep scans able to reach overrides inside directories excluded by git.
	containsFYC := false
	if !useFYC {
		prefix := strings.TrimSuffix(absPath, string(filepath.Separator)) + string(filepath.Separator)
		for _, f := range im.files {
			if !f.fyc {
				continue
			}
			if strings.HasPrefix(f.scopeAbs, prefix) ||
				(f.altScope != f.scopeAbs && strings.HasPrefix(f.altScope, prefix)) {
				containsFYC = true
				break
			}
		}
	}
	for _, f := range im.files {
		if !f.fyc && (useFYC || (containsFYC && isDir())) {
			continue
		}
		// Skip if path is outside this rule's scope, trying both spellings.
		scope, ok := f.scopeFor(absPath)
		if !ok {
			continue
		}
		// Slice the prefix directly instead of filepath.Rel to avoid allocation
		relToScope := filepath.ToSlash(absPath[len(scope):])
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
