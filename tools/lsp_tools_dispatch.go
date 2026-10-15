package tools

import (
	"fmt"
)

// ─── LSP tool dispatch ───────────────────────────────────────────────────────
//
// Helper functions used here are already defined in tools/lsp.go:
//
//	getLSPManager(restrictedRoot, ignore) (*lsp.Manager, error)
//	lspPositionFromArgs(args, restrictedRoot, ignore) (lsp.Position, error)
//	lspRequestContext() (context.Context, context.CancelFunc)
//	lspAbsPath(fileArg, restrictedRoot, ignore) (string, error)
//	formatJSON(v any) string
//	formatActiveLSPs(items []string) string
//	numberArg(args, name) (int, error)

// executeLSPActiveServers handles the "lsp_active_servers" case.
func executeLSPActiveServers(restrictedRoot string, globalIgnore *IgnoreManager) string {
	mgr, err := getLSPManager(restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	return formatActiveLSPs(mgr.ActiveLSPs())
}

// executeLSPHover handles the "lsp_hover" case.
func executeLSPHover(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	pos, err := lspPositionFromArgs(args, restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	mgr, err := getLSPManager(restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	ctx, cancel := lspRequestContext()
	defer cancel()
	res, err := mgr.Hover(ctx, pos)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	return res.Contents
}

// executeLSPDefinition handles the "lsp_definition" case.
func executeLSPDefinition(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	pos, err := lspPositionFromArgs(args, restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	mgr, err := getLSPManager(restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	ctx, cancel := lspRequestContext()
	defer cancel()
	locs, err := mgr.Definition(ctx, pos)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	return formatJSON(locs)
}

// executeLSPReferences handles the "lsp_references" case.
func executeLSPReferences(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	pos, err := lspPositionFromArgs(args, restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	mgr, err := getLSPManager(restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	ctx, cancel := lspRequestContext()
	defer cancel()
	locs, err := mgr.References(ctx, pos)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	return formatJSON(locs)
}

// executeLSPDiagnostics handles the "lsp_diagnostics" case.
func executeLSPDiagnostics(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	fileArg, _ := args["file"].(string)
	if fileArg == "" {
		return "lsp error: file argument required"
	}
	file, err := lspAbsPath(fileArg, restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	mgr, err := getLSPManager(restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	ctx, cancel := lspRequestContext()
	defer cancel()
	diags, err := mgr.Diagnostics(ctx, file)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	return formatJSON(diags)
}

// executeLSPWorkspaceSymbols handles the "lsp_workspace_symbols" case.
func executeLSPWorkspaceSymbols(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) string {
	lspName, _ := args["lsp_name"].(string)
	query, _ := args["query"].(string)
	if lspName == "" || query == "" {
		return "lsp error: lsp_name and query arguments required"
	}
	mgr, err := getLSPManager(restrictedRoot, globalIgnore)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	ctx, cancel := lspRequestContext()
	defer cancel()
	symbols, err := mgr.WorkspaceSymbols(ctx, lspName, query)
	if err != nil {
		return fmt.Sprintf("lsp error: %v", err)
	}
	return formatJSON(symbols)
}
