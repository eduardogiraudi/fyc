package lsp

import (
	"fmt"
	"github.com/charlievieth/fastwalk"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ScanExtensions walks root and returns file extensions present in files not
// rejected by the optional ignored callback.
func ScanExtensions(root string, ignored func(absPath string) bool) map[string]struct{} {
	exts := make(map[string]struct{})
	var mu sync.Mutex
	conf := fastwalk.Config{Follow: false}
	_ = fastwalk.Walk(&conf, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ignored != nil && ignored(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if ext := fileExt(path); ext != "" {
			mu.Lock()
			exts[ext] = struct{}{}
			mu.Unlock()
		}
		return nil
	})
	return exts
}

// FilterRelevant returns only the LSPs that handle at least one extension
// present in the workspace.
func FilterRelevant(infos []LSPInfo, presentExts map[string]struct{}) []LSPInfo {
	var out []LSPInfo
	for _, info := range infos {
		for _, ext := range extensionsForLanguage(info.Language) {
			if _, ok := presentExts[ext]; ok {
				out = append(out, info)
				break
			}
		}
	}
	return out
}

// ---------- helpers used elsewhere ----------

func extractPosition(params map[string]any) (Position, error) {
	file, _ := params["file"].(string)
	if file == "" {
		return Position{}, fmt.Errorf("file is required")
	}
	line, err := toInt(params["line"])
	if err != nil {
		return Position{}, fmt.Errorf("line: %w", err)
	}
	char, err := toInt(params["character"])
	if err != nil {
		return Position{}, fmt.Errorf("character: %w", err)
	}
	return Position{File: file, Line: line, Character: char}, nil
}

func toInt(v any) (int, error) {
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case int:
		return val, nil
	case string:
		return strconv.Atoi(val)
	default:
		return 0, fmt.Errorf("expected int, got %T", v)
	}
}

func formatLocations(label string, locs []Location) string {
	if len(locs) == 0 {
		return label + ": not found."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%d):\n", label, len(locs))
	for _, l := range locs {
		fmt.Fprintf(&sb, "  %s:%d\n", l.File, l.Line)
	}
	return sb.String()
}
