// Package lsp manages LSP server processes and provides query-style access
// to the capabilities an agent actually needs.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client manages a single LSP server process.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	pending map[int64]chan response
	nextID  atomic.Int64

	rootURI string
	name    string
}

type response struct {
	Result json.RawMessage
	Error  *lspError
}

type lspError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// New starts an LSP server and initializes it for the given workspace.
func New(ctx context.Context, binary string, args []string, name, rootPath string) (*Client, error) {
	cmd := exec.CommandContext(ctx, binary, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// stderr goes to /dev/null — we don't want LSP noise on our MCP stdout
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		pending: make(map[int64]chan response),
		rootURI: pathToURI(rootPath),
		name:    name,
	}
	// Use a larger buffer: LSP responses can be large.

	go c.readLoop()

	if err := c.initialize(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize %s: %w", name, err)
	}

	return c, nil
}

// Close shuts down the LSP server gracefully.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "shutdown", nil)
	_ = c.notify("exit", nil)
	return c.cmd.Wait()
}

// ---------- agent-facing queries ----------

// Position in a source file (0-based lines and characters, as LSP specifies).
type Position struct {
	File      string `json:"file"`      // absolute path
	Line      int    `json:"line"`      // 0-based
	Character int    `json:"character"` // 0-based
}

// Location is a position range in a file.
type Location struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
}

// Definition returns the definition location(s) for the symbol at pos.
func (c *Client) Definition(ctx context.Context, pos Position) ([]Location, error) {
	_ = c.openFile(ctx, pos.File)
	params := textDocumentPositionParams(pos)
	raw, err := c.call(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// References returns all references to the symbol at pos.
func (c *Client) References(ctx context.Context, pos Position) ([]Location, error) {
	_ = c.openFile(ctx, pos.File)
	params := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(pos.File)},
		"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		"context":      map[string]any{"includeDeclaration": true},
	}
	raw, err := c.call(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// HoverResult is the text returned by hover.
type HoverResult struct {
	Contents string `json:"contents"`
}

// Hover returns documentation/type info for the symbol at pos.
func (c *Client) Hover(ctx context.Context, pos Position) (*HoverResult, error) {
	_ = c.openFile(ctx, pos.File)
	params := textDocumentPositionParams(pos)
	raw, err := c.call(ctx, "textDocument/hover", params)
	if err != nil {
		return nil, err
	}
	if raw == nil || string(raw) == "null" {
		return &HoverResult{Contents: "(no hover info)"}, nil
	}
	var h struct {
		Contents any `json:"contents"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return &HoverResult{Contents: stringifyContents(h.Contents)}, nil
}

// Diagnostic is a single diagnostic from the LSP.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity int    `json:"severity"` // 1=error 2=warn 3=info 4=hint
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
}

// Diagnostics opens a file and requests diagnostics via a workspace/diagnostic
// pull request (LSP 3.17+). Falls back to empty if not supported.
func (c *Client) Diagnostics(ctx context.Context, filePath string) ([]Diagnostic, error) {
	// Open the file so the LSP knows about it
	if err := c.openFile(ctx, filePath); err != nil {
		return nil, err
	}

	raw, err := c.call(ctx, "textDocument/diagnostic", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filePath)},
	})
	if err != nil {
		// Many LSPs don't support pull diagnostics — return empty
		return nil, nil //nolint:nilerr
	}

	var result struct {
		Items []struct {
			Range    lspRange `json:"range"`
			Severity int      `json:"severity"`
			Code     any      `json:"code"`
			Message  string   `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}

	var out []Diagnostic
	for _, item := range result.Items {
		d := Diagnostic{
			File:     filePath,
			Line:     item.Range.Start.Line,
			Severity: item.Severity,
			Message:  item.Message,
		}
		if item.Code != nil {
			d.Code = fmt.Sprintf("%v", item.Code)
		}
		out = append(out, d)
	}
	return out, nil
}

// SymbolResult is a workspace symbol.
type SymbolResult struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// WorkspaceSymbols searches for symbols matching query across the workspace.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolResult, error) {
	raw, err := c.call(ctx, "workspace/symbol", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var items []struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Location lspLoc `json:"location"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	var out []SymbolResult
	for _, i := range items {
		out = append(out, SymbolResult{
			Name: i.Name,
			Kind: symbolKindName(i.Kind),
			File: uriToPath(i.Location.URI),
			Line: i.Location.Range.Start.Line,
		})
	}
	return out, nil
}

// Rename renames the symbol at pos to newName.
// Returns a map of filePath -> list of edit ranges (as text for the agent to apply).
func (c *Client) Rename(ctx context.Context, pos Position, newName string) (map[string]string, error) {
	params := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(pos.File)},
		"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		"newName":      newName,
	}
	raw, err := c.call(ctx, "textDocument/rename", params)
	if err != nil {
		return nil, err
	}
	var workspaceEdit struct {
		Changes map[string][]struct {
			Range   lspRange `json:"range"`
			NewText string   `json:"newText"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &workspaceEdit); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for uri, edits := range workspaceEdit.Changes {
		var sb strings.Builder
		for _, e := range edits {
			fmt.Fprintf(&sb, "line %d char %d → char %d: replace with %q\n",
				e.Range.Start.Line, e.Range.Start.Character, e.Range.End.Character, e.NewText)
		}
		result[uriToPath(uri)] = sb.String()
	}
	return result, nil
}

// ---------- LSP protocol helpers ----------

func (c *Client) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	params := map[string]any{
		"processId":             nil,
		"rootUri":               c.rootURI,
		"capabilities":          clientCapabilities(),
		"initializationOptions": map[string]any{},
	}
	_, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func (c *Client) openFile(ctx context.Context, filePath string) error {
	text, _ := os.ReadFile(filePath) // best-effort; empty string is fine for LSPs that read from disk
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        pathToURI(filePath),
			"languageId": inferLanguageID(filePath),
			"version":    1,
			"text":       string(text),
		},
	})
}

func clientCapabilities() map[string]any {
	return map[string]any{
		"textDocument": map[string]any{
			"definition": map[string]any{"linkSupport": false},
			"references": map[string]any{},
			"hover":      map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
			"diagnostic": map[string]any{"dynamicRegistration": false},
			"rename":     map[string]any{"prepareSupport": false},
		},
		"workspace": map[string]any{
			"symbol": map[string]any{},
		},
	}
}

// ---------- JSON-RPC transport ----------

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspError       `json:"error,omitempty"`
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan response, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(&rpcMsg{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("lsp %s: code %d: %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.send(&rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) send(msg *rpcMsg) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err = c.stdin.Write(body)
	return err
}

func (c *Client) readLoop() {
	var contentLength int
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "Content-Length:") {
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")), "%d", &contentLength)
			continue
		}
		if line == "" && contentLength > 0 {
			buf := make([]byte, contentLength)
			if _, err := io.ReadFull(c.stdout, buf); err != nil {
				return
			}
			contentLength = 0
			c.dispatch(buf)
		}
	}
}

func (c *Client) dispatch(data []byte) {
	var msg rpcMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	if msg.ID == nil {
		// notification from server — ignore for agent use case
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[*msg.ID]
	if ok {
		delete(c.pending, *msg.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- response{Result: msg.Result, Error: msg.Error}
	}
}

// ---------- LSP type helpers ----------

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspLoc struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

func textDocumentPositionParams(pos Position) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(pos.File)},
		"position":     map[string]any{"line": pos.Line, "character": pos.Character},
	}
}

func parseLocations(raw json.RawMessage) ([]Location, error) {
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	// Can be a single Location or []Location or []LocationLink
	var single lspLoc
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{{File: uriToPath(single.URI), Line: single.Range.Start.Line, EndLine: single.Range.End.Line}}, nil
	}
	var many []lspLoc
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	out := make([]Location, 0, len(many))
	for _, l := range many {
		out = append(out, Location{File: uriToPath(l.URI), Line: l.Range.Start.Line, EndLine: l.Range.End.Line})
	}
	return out, nil
}

func stringifyContents(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if s, ok := val["value"].(string); ok {
			return s
		}
		b, _ := json.Marshal(val)
		return string(b)
	case []any:
		var parts []string
		for _, item := range val {
			parts = append(parts, stringifyContents(item))
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func pathToURI(path string) string {
	if strings.HasPrefix(path, "file://") {
		return path
	}
	// Simple: assume absolute path on unix
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + path
}

func uriToPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}

func inferLanguageID(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"):
		return "typescript"
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".jsx"):
		return "javascript"
	case strings.HasSuffix(path, ".c"), strings.HasSuffix(path, ".h"):
		return "c"
	case strings.HasSuffix(path, ".cpp"), strings.HasSuffix(path, ".cc"):
		return "cpp"
	case strings.HasSuffix(path, ".lua"):
		return "lua"
	case strings.HasSuffix(path, ".rb"):
		return "ruby"
	case strings.HasSuffix(path, ".java"):
		return "java"
	case strings.HasSuffix(path, ".kt"):
		return "kotlin"
	case strings.HasSuffix(path, ".sh"):
		return "shellscript"
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		return "yaml"
	case strings.HasSuffix(path, ".json"):
		return "json"
	case strings.HasSuffix(path, ".md"):
		return "markdown"
	default:
		return "plaintext"
	}
}

var symbolKindNames = map[int]string{
	1: "File", 2: "Module", 3: "Namespace", 4: "Package",
	5: "Class", 6: "Method", 7: "Property", 8: "Field",
	9: "Constructor", 10: "Enum", 11: "Interface", 12: "Function",
	13: "Variable", 14: "Constant", 15: "String", 16: "Number",
	17: "Boolean", 18: "Array", 19: "Object", 20: "Key",
	21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event",
	25: "Operator", 26: "TypeParameter",
}

func symbolKindName(k int) string {
	if s, ok := symbolKindNames[k]; ok {
		return s
	}
	return strconv.Itoa(k)
}
