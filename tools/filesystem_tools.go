package tools

import (
	"context"
	"fmt"
	"github.com/charlievieth/fastwalk"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// executeLSTool handles the "ls" case.
func executeLSTool(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	dirFile, err := root.Open(relPath)
	if err != nil {
		return err.Error()
	}
	defer dirFile.Close()

	entries, err := dirFile.ReadDir(0)
	if err != nil {
		return err.Error()
	}

	var out []string
	for _, e := range entries {
		childRel := filepath.Join(relPath, e.Name())
		if globalIgnore != nil {
			childAbs := filepath.Join(restrictedRoot, childRel)
			if globalIgnore.isIgnored(childAbs, restrictedRoot) {
				continue
			}
		}
		out = append(out, e.Name())
	}
	return strings.Join(out, "\n")
}

// executeReadTool handles the "read" case.
func executeReadTool(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok {
		return "error: path argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	f, err := root.Open(relPath)
	if err != nil {
		return err.Error()
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

// executeWriteTool handles the "write" case.
func executeWriteTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok {
		return "error: path argument required"
	}
	content, ok := args["content"].(string)
	if !ok {
		return "error: content argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	safeAbsPath := filepath.Join(restrictedRoot, relPath)

	// Check if file already exists
	existingFile, err := root.Open(relPath)
	if err == nil {
		// Overwriting existing file — show diff viewer
		oldData, _ := io.ReadAll(existingFile)
		existingFile.Close()
		allowed, suggestion := askUpdatePermission(ctx, safeAbsPath, string(oldData), content)
		if !allowed {
			if suggestion != "" {
				return "error: write denied by the user: " + suggestion
			}
			return "error: write denied by the user"
		}
	} else {
		// New file — use original permission logic (AllowFileWrite/AskFileWriteEach)
		allowed, suggestion := askWritePermission(ctx, safeAbsPath)
		if !allowed {
			if suggestion != "" {
				return "error: write denied by the user: " + suggestion
			}
			return "error: write denied by the user"
		}
	}

	f, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err.Error()
	}
	_, err = f.Write([]byte(content))
	f.Close()
	if err != nil {
		return err.Error()
	}
	return "written"
}

// executeDeleteTool handles the "delete" case.
func executeDeleteTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok {
		return "error: path argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	safeAbsPath := filepath.Join(restrictedRoot, relPath)
	allowed, suggestion := askDeletePermission(ctx, safeAbsPath)
	if !allowed {
		if suggestion != "" {
			return "error: delete cancelled by the user: " + suggestion
		}
		return "error: delete cancelled by the user"
	}
	_ = suggestion

	err = deletePathInRoot(root, relPath)
	if err != nil {
		return err.Error()
	}
	return "deleted"
}

// executeCopyTool handles the "copy" case.
func executeCopyTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	source, ok := args["source"].(string)
	if !ok || source == "" {
		return "error: source argument required"
	}
	dest, ok := args["destination"].(string)
	if !ok || dest == "" {
		return "error: destination argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	srcRel, err := validatePathInRoot(source, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}
	dstRel, err := validatePathInRoot(dest, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	// Check if source exists
	if _, err := root.Lstat(srcRel); err != nil {
		return fmt.Sprintf("error: source does not exist: %v", err)
	}

	safeSrcAbs := filepath.Join(restrictedRoot, srcRel)
	safeDstAbs := filepath.Join(restrictedRoot, dstRel)
	allowed, suggestion := askCopyPermission(ctx, safeSrcAbs, safeDstAbs)
	if !allowed {
		if suggestion != "" {
			return "error: copy denied: " + suggestion
		}
		return "error: copy denied"
	}
	_ = suggestion

	if err := copyPathInRoot(root, srcRel, dstRel); err != nil {
		return fmt.Sprintf("copy error: %v", err)
	}
	return "copied"
}

// executeRenameTool handles the "rename" case.
func executeRenameTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return "error: path argument required"
	}
	newName, ok := args["new_name"].(string)
	if !ok || newName == "" {
		return "error: new_name argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	oldRel, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	// Compute new path: same parent directory, just the new basename
	parentDir := filepath.Dir(oldRel)
	newRel := filepath.Join(parentDir, newName)

	// Validate the new path as well
	_, err = validatePathInRoot(newRel, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	safeOldAbs := filepath.Join(restrictedRoot, oldRel)
	safeNewAbs := filepath.Join(restrictedRoot, newRel)
	allowed, suggestion := askRenamePermission(ctx, safeOldAbs, safeNewAbs)
	if !allowed {
		if suggestion != "" {
			return "error: rename cancelled by the user: " + suggestion
		}
		return "error: rename cancelled by the user"
	}
	_ = suggestion

	if err := renameInRoot(root, oldRel, newRel); err != nil {
		return fmt.Sprintf("rename error: %v", err)
	}
	return "renamed"
}

// executeMoveTool handles the "move" case.
func executeMoveTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	source, ok := args["source"].(string)
	if !ok || source == "" {
		return "error: source argument required"
	}
	dest, ok := args["destination"].(string)
	if !ok || dest == "" {
		return "error: destination argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	srcRel, err := validatePathInRoot(source, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}
	dstRel, err := validatePathInRoot(dest, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	safeSrcAbs := filepath.Join(restrictedRoot, srcRel)
	safeDstAbs := filepath.Join(restrictedRoot, dstRel)
	allowed, suggestion := askMovePermission(ctx, safeSrcAbs, safeDstAbs)
	if !allowed {
		if suggestion != "" {
			return "error: move cancelled by the user: " + suggestion
		}
		return "error: move cancelled by the user"
	}
	_ = suggestion

	if err := renameInRoot(root, srcRel, dstRel); err != nil {
		return fmt.Sprintf("move error: %v", err)
	}
	return "moved"
}

// executeMakeDirTool handles the "make_dir" case.
func executeMakeDirTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok {
		return "error: path argument required"
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	safeAbsPath := filepath.Join(restrictedRoot, relPath)
	allowed, suggestion := askMakeDirPermission(ctx, safeAbsPath)
	if !allowed {
		if suggestion != "" {
			return "error: make_dir cancelled by the user: " + suggestion
		}
		return "error: make_dir cancelled by the user"
	}
	_ = suggestion

	err = root.MkdirAll(relPath, 0755)
	if err != nil {
		return err.Error()
	}
	return "created"
}

// executeGrepTool handles the "grep" case.
func executeGrepTool(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return "error: pattern argument required"
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	caseInsensitive, _ := args["case_insensitive"].(bool)
	includeGlob, _ := args["include_glob"].(string)
	excludeGlob, _ := args["exclude_glob"].(string)
	includeBinary, _ := args["include_binary"].(bool)
	maxFileSize, _ := args["max_file_size"].(float64)
	workers, _ := args["workers"].(float64)

	// Validate the search path against the restricted root
	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()

	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}

	// The search root is handed back to the plain filesystem (fastwalk plus
	// os.OpenRoot), so resolve symlinks and refuse anything that lands outside
	// the workspace: a symlinked path would otherwise re-root the whole search.
	absRoot, err := realPathWithinRoot(filepath.Join(restrictedRoot, relPath), restrictedRoot)
	if err != nil {
		return fmt.Sprintf("grep error: %v", err)
	}
	if fi, stErr := os.Stat(absRoot); stErr == nil && !fi.IsDir() {
		if includeGlob == "" {
			includeGlob = filepath.Base(absRoot)
		}
		absRoot = filepath.Dir(absRoot)
	}

	matches, err := GrepRun(GrepOptions{
		Pattern:         pattern,
		Root:            absRoot,
		CaseInsensitive: caseInsensitive,
		IncludeGlob:     includeGlob,
		ExcludeGlob:     excludeGlob,
		IncludeBinary:   includeBinary,
		MaxFileSize:     int64(maxFileSize),
		Workers:         int(workers),
		IgnoreManager:   globalIgnore,
	})
	if err != nil {
		return fmt.Sprintf("grep error: %v", err)
	}

	if len(matches) == 0 {
		return "no matches found"
	}

	var lines []string
	for _, m := range matches {
		lines = append(lines, fmt.Sprintf("%s:%d: %s", m.Path, m.Line, m.Content))
	}
	return strings.Join(lines, "\n")
}

// executeGetAnchorsTool handles the "get_anchors" case.
func executeGetAnchorsTool(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok {
		return "error: path argument required"
	}
	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()
	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}
	globalAnchorState.opMu.Lock()
	defer globalAnchorState.opMu.Unlock()
	data, err := root.ReadFile(relPath)
	if err != nil {
		return err.Error()
	}
	absPath := filepath.Join(restrictedRoot, relPath)
	globalAnchorState.EnsureContent(absPath, string(data))
	annotated, err := globalAnchorState.GetAnnotated(absPath)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return annotated
}

// executeReplaceAnchorsTool handles the "replace_anchors" case.
func executeReplaceAnchorsTool(ctx context.Context, args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	path, ok := args["path"].(string)
	if !ok {
		return "error: path argument required"
	}
	startAnchor, ok := args["start_anchor"].(string)
	if !ok || startAnchor == "" {
		return "error: start_anchor argument required"
	}
	endAnchor, ok := args["end_anchor"].(string)
	if !ok || endAnchor == "" {
		return "error: end_anchor argument required"
	}
	// replacement is optional (empty string = delete the range)
	replacement, _ := args["replacement"].(string)
	// Tolerate the model passing annotated lines (e.g. "Agate:a3§content") in the
	// replacement — strip the Word:hash§ prefixes so only plain source is written.
	replacement = stripAnchors(replacement)

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return fmt.Errorf("error opening root: %w", err).Error()
	}
	defer root.Close()
	relPath, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return err.Error()
	}
	absPath := filepath.Join(restrictedRoot, relPath)

	var oldLines, newLines []string
	var oldContent string
	err = func() error {
		globalAnchorState.opMu.Lock()
		defer globalAnchorState.opMu.Unlock()
		data, err := root.ReadFile(relPath)
		if err != nil {
			return fmt.Errorf("cannot read current file: %w — no changes written; call get_anchors again", err)
		}
		oldContent = string(data)
		if err := globalAnchorState.validateContent(absPath, oldContent); err != nil {
			return err
		}
		oldLines, newLines, err = globalAnchorState.PreviewEdit(absPath, startAnchor, endAnchor, replacement)
		return err
	}()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if slicesEqual(oldLines, newLines) {
		return "no changes needed — target content is identical"
	}
	newContent := JoinLines(newLines)

	allowed, suggestion := askUpdatePermission(ctx, absPath, oldContent, newContent)
	if !allowed {
		if suggestion != "" {
			return "error: update cancelled by the user: " + suggestion
		}
		return "error: update cancelled by the user"
	}

	globalAnchorState.opMu.Lock()
	defer globalAnchorState.opMu.Unlock()
	// Open without CREATE/TRUNC: edits or deletion during approval must not be lost.
	wf, err := root.OpenFile(relPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Sprintf("error: cannot open current file: %v — no changes written; call get_anchors again", err)
	}
	defer wf.Close()
	data, err := io.ReadAll(wf)
	if err != nil {
		return fmt.Sprintf("error: cannot read current file: %v — no changes written; call get_anchors again", err)
	}
	// Revalidate both disk and anchors: /clear or another patch may have run meanwhile.
	if string(data) != oldContent {
		globalAnchorState.mu.Lock()
		delete(globalAnchorState.files, absPath)
		globalAnchorState.mu.Unlock()
		return "error: file changed on disk while awaiting approval — no changes written; call get_anchors again and reconsider the patch using the current content"
	}
	if err := globalAnchorState.validateContent(absPath, string(data)); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if _, _, err := globalAnchorState.PreviewEdit(absPath, startAnchor, endAnchor, replacement); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	openedInfo, err := wf.Stat()
	if err != nil {
		return err.Error()
	}
	pathInfo, err := root.Stat(relPath)
	if err != nil || !os.SameFile(openedInfo, pathInfo) {
		return "error: file replaced or removed before writing — no changes written; call get_anchors again"
	}
	if err := ctx.Err(); err != nil {
		return fmt.Sprintf("error: %v — no changes written", err)
	}
	if _, err := wf.WriteAt([]byte(newContent), 0); err != nil {
		return err.Error()
	}
	if err := wf.Truncate(int64(len(newContent))); err != nil {
		return err.Error()
	}
	if err := wf.Close(); err != nil {
		return err.Error()
	}

	_, insertedOnly := globalAnchorState.CommitEdit(absPath, oldLines, newLines)
	if insertedOnly == "" {
		return "updated (lines deleted, no new anchors)"
	}
	return "updated\nnew anchors:\n" + insertedOnly
}

// executeContextSnapshotTool handles the "context_snapshot" case.
func executeContextSnapshotTool(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	root, err := getRoot(restrictedRoot)
	if err != nil {
		return err.Error()
	}
	defer root.Close()

	snapshotPath, _ := args["path"].(string)
	walkRoot := restrictedRoot
	if snapshotPath != "" {
		relPath, err := validatePathInRoot(snapshotPath, restrictedRoot, globalIgnore)
		if err != nil {
			return err.Error()
		}
		resolved, err := realPathWithinRoot(filepath.Join(restrictedRoot, relPath), restrictedRoot)
		if err != nil {
			return err.Error()
		}
		walkRoot = resolved
	}
	const maxSnapshotSize = 30000

	supported := map[string]bool{
		".go": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
		".ts": true, ".tsx": true, ".mts": true, ".cts": true,
		".py": true, ".rs": true,
		".java": true, ".cpp": true, ".c": true, ".h": true,
		".cs": true, ".php": true, ".rb": true, ".swift": true,
		".kt": true, ".kts": true, ".sh": true, ".bash": true,
		".css": true, ".scss": true, ".less": true,
		".html": true, ".htm": true,
		".yaml": true, ".yml": true, ".toml": true, ".sql": true,
	}

	var out []string
	var totalSize int
	var mu sync.Mutex
	var stopped bool

	fwConf := &fastwalk.Config{Follow: false}
	errStopWalk := fmt.Errorf("stop walk")

	fastwalk.Walk(fwConf, walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || stopped {
			return nil
		}
		if d.IsDir() {
			if globalIgnore != nil && globalIgnore.isIgnored(path, restrictedRoot) {
				return filepath.SkipDir
			}
			return nil
		}
		// Never follow symlinked entries: their content is read with the plain OS
		// inside extractSymbolsFromFile, so a link pointing outside the workspace
		// would leak symbols of an out-of-root file.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > 1<<20 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == "" || !supported[ext] {
			return nil
		}
		if globalIgnore != nil && globalIgnore.isIgnored(path, restrictedRoot) {
			return nil
		}
		symbols := extractSymbolsFromFile(path)
		if symbols == "" {
			return nil
		}
		rel, _ := filepath.Rel(restrictedRoot, path)
		entry := "\nFILE: " + rel + "\n" + symbols

		mu.Lock()
		if totalSize+len(entry) > maxSnapshotSize {
			mu.Unlock()
			stopped = true
			return errStopWalk
		}
		totalSize += len(entry)
		out = append(out, entry)
		mu.Unlock()
		return nil
	})

	result := strings.Join(out, "\n")
	if len(result) > maxSnapshotSize {
		result = result[:maxSnapshotSize] + "\n... [snapshot truncated — project too large]"
	}
	return result
}

// ─── helpers in the same package ─────────────────────────────────────────────
// getRoot, validatePathInRoot, deletePathInRoot, copyPathInRoot, renameInRoot
// are defined in path.go.
// SplitLines, JoinLines, stripAnchors, slicesEqual are defined in anchor_state.go.
// globalAnchorState is a *AnchorStateManager in anchor_state.go.
// GrepRun, GrepOptions are defined in grep.go.
// askUpdatePermission, askWritePermission, askDeletePermission, askCopyPermission,
// askRenamePermission, askMovePermission, askMakeDirPermission are in permissions.go.
// crawl.Crawl is in the crawl package.
