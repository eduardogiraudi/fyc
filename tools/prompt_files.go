package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"fuckyourcli/internal/mentions"
	"fuckyourcli/types"
)

const MaxPromptFileBytes = 256 << 10

func promptFilePath(root *os.Root, path, restrictedRoot string, ignore *IgnoreManager) (string, error) {
	rel, err := validatePathInRoot(path, restrictedRoot, ignore)
	if err != nil {
		return "", err
	}
	// Do not let aliases bypass ignored directories or disclose symlink targets.
	for cur := rel; cur != "."; cur = filepath.Dir(cur) {
		if filepath.Base(cur) == ".git" {
			return "", fmt.Errorf("access to .git is denied")
		}
		if _, err := validatePathInRoot(cur, restrictedRoot, ignore); err != nil {
			return "", err
		}
		info, err := root.Lstat(cur)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink attachments are not supported")
		}
	}
	return rel, nil
}

// PromptFile uses the same root confinement and image limits as filesystem tools.
func PromptFile(path, restrictedRoot string, ignore *IgnoreManager) (types.ToolResult, error) {
	if ignore == nil {
		ignore = LoadIgnoreFiles(restrictedRoot)
	}
	root, err := getRoot(restrictedRoot)
	if err != nil {
		return types.ToolResult{}, err
	}
	defer root.Close()
	rel, err := promptFilePath(root, path, restrictedRoot, ignore)
	if err != nil {
		return types.ToolResult{}, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return types.ToolResult{}, err
	}
	if !info.Mode().IsRegular() {
		return types.ToolResult{}, fmt.Errorf("only regular files can be attached")
	}
	if _, ok := mediaMIME[strings.ToLower(filepath.Ext(path))]; ok {
		result := executeReadMediaTool(map[string]any{"path": path}, restrictedRoot, ignore)
		if types.ToolResultFailed(result.Text) {
			return types.ToolResult{}, fmt.Errorf("%s", result.Text)
		}
		return result, nil
	}
	f, err := root.Open(rel)
	if err != nil {
		return types.ToolResult{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxPromptFileBytes+1))
	if err != nil {
		return types.ToolResult{}, err
	}
	if len(data) > MaxPromptFileBytes {
		return types.ToolResult{}, fmt.Errorf("text file exceeds 256 KiB")
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return types.ToolResult{}, fmt.Errorf("not a UTF-8 text file or supported image")
	}
	return types.ToolResult{Text: string(data)}, nil
}

// PromptFileIn attaches a file that may live in any allowed root. The path is
// resolved against the workspace first and then handed to PromptFile as a name
// inside the owning root, so @/abs/path works for an extra root too.
func PromptFileIn(path string, ws Workspace, ignore *IgnoreManager) (types.ToolResult, error) {
	if ignore == nil {
		ignore = LoadWorkspaceIgnore(ws.Roots())
	}
	owner, rel, err := ws.resolve(path, ignore)
	if err != nil {
		return types.ToolResult{}, err
	}
	return PromptFile(rel, owner, ignore)
}

// PromptFileSuggestions browses one directory, with bounded work per keystroke.
func PromptFileSuggestions(query, restrictedRoot string, ignore *IgnoreManager) ([]string, error) {
	if len([]rune(query)) > mentions.MaxPathRunes {
		return nil, fmt.Errorf("path too long")
	}
	if ignore == nil {
		ignore = LoadIgnoreFiles(restrictedRoot)
	}
	dir, base := filepath.Split(filepath.FromSlash(query))
	if dir == "" {
		dir = "."
	}
	root, err := getRoot(restrictedRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := promptFilePath(root, dir, restrictedRoot, ignore)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var paths []string
	for scanned := 0; scanned < 4096; scanned += 128 {
		entries, readErr := f.ReadDir(128)
		for _, e := range entries {
			path := filepath.Join(rel, e.Name())
			if !e.Type().IsRegular() && !e.IsDir() || strings.ContainsAny(e.Name(), "\"\r\n`") {
				continue
			}
			if !strings.Contains(strings.ToLower(e.Name()), strings.ToLower(base)) {
				continue
			}
			if _, err := promptFilePath(root, path, restrictedRoot, ignore); err != nil {
				continue
			}
			path = filepath.ToSlash(path)
			if e.IsDir() {
				path += "/"
			}
			paths = append(paths, path)
			if len(paths) == 200 {
				sort.Strings(paths)
				return paths, nil
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Strings(paths)
	return paths, nil
}
