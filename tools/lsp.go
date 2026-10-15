package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"fuckyourcli/config"
	"fuckyourcli/lsp"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var lspManagers sync.Map // map[restrictedRoot]*lazyLSP

// LSPOptions builds the discovery options from the persisted configuration, so
// the user's search paths, per-server overrides and disabled list reach every
// discovery call site (initial start and the manager heartbeat).
func LSPOptions() lsp.Options {
	cfg := config.Get()
	return lsp.Options{
		SearchPaths: cfg.LSP.SearchPaths,
		Binaries:    cfg.LSP.Binaries,
		Disabled:    cfg.LSP.Disabled,
	}
}

type lazyLSP struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	manager *lsp.Manager
}

func getLSPManager(restrictedRoot string, ignore *IgnoreManager) (*lsp.Manager, error) {
	value, _ := lspManagers.LoadOrStore(restrictedRoot, &lazyLSP{})
	lazy := value.(*lazyLSP)

	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	if lazy.manager != nil {
		return lazy.manager, nil
	}

	ignored := func(absPath string) bool {
		return ignore != nil && ignore.IsIgnored(absPath, restrictedRoot)
	}
	opts := LSPOptions()
	presentExts := lsp.ScanExtensions(restrictedRoot, ignored)
	infos := lsp.FilterRelevant(lsp.Discover(opts), presentExts)
	if len(infos) == 0 {
		return nil, fmt.Errorf("no relevant LSP servers discovered for this workspace")
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager, err := lsp.NewManager(ctx, restrictedRoot, infos, opts)
	if err != nil {
		cancel()
		return nil, err
	}
	lazy.ctx = ctx
	lazy.cancel = cancel
	lazy.manager = manager
	return manager, nil
}

func closeAllLSPManagers() {
	lspManagers.Range(func(key, value any) bool {
		lazy := value.(*lazyLSP)
		lazy.mu.Lock()
		if lazy.manager != nil {
			lazy.manager.Close()
			lazy.manager = nil
		}
		if lazy.cancel != nil {
			lazy.cancel()
			lazy.cancel = nil
		}
		lazy.mu.Unlock()
		lspManagers.Delete(key)
		return true
	})
}

func lspRequestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func lspPositionFromArgs(args map[string]interface{}, restrictedRoot string, ignore *IgnoreManager) (lsp.Position, error) {
	fileArg, _ := args["file"].(string)
	if fileArg == "" {
		return lsp.Position{}, fmt.Errorf("file argument required")
	}
	line, err := numberArg(args, "line")
	if err != nil {
		return lsp.Position{}, err
	}
	character, err := numberArg(args, "character")
	if err != nil {
		return lsp.Position{}, err
	}
	file, err := lspAbsPath(fileArg, restrictedRoot, ignore)
	if err != nil {
		return lsp.Position{}, err
	}
	return lsp.Position{File: file, Line: line, Character: character}, nil
}

func lspAbsPath(path string, restrictedRoot string, ignore *IgnoreManager) (string, error) {
	rel, err := validatePathInRoot(path, restrictedRoot, ignore)
	if err != nil {
		return "", err
	}
	// The absolute path is handed to an external language server, which reads it
	// with the plain OS: a symlink must not re-root the request outside the
	// workspace (os.Root is not in the loop here).
	return realPathWithinRoot(filepath.Join(restrictedRoot, rel), restrictedRoot)
}

func numberArg(args map[string]interface{}, name string) (int, error) {
	v, ok := args[name]
	if !ok {
		return 0, fmt.Errorf("%s argument required", name)
	}
	switch n := v.(type) {
	case float64:
		return int(n), nil
	case int:
		return n, nil
	case string:
		var out int
		_, err := fmt.Sscanf(n, "%d", &out)
		if err != nil {
			return 0, fmt.Errorf("%s must be a number", name)
		}
		return out, nil
	default:
		return 0, fmt.Errorf("%s must be a number", name)
	}
}

func formatJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

func formatActiveLSPs(items []string) string {
	if len(items) == 0 {
		return "no active LSP servers"
	}
	return strings.Join(items, "\n")
}
