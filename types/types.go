package types

import (
	"context"
	"strings"
	"sync"
)

// ToolProperty represents a tool property
type ToolProperty struct {
	Type        string                  `json:"type"`
	Description string                  `json:"description"`
	Items       *ToolProperty           `json:"items,omitempty"`
	Properties  map[string]ToolProperty `json:"properties,omitempty"`
	Required    []string                `json:"required,omitempty"`
}

// ToSchema recursively converts ToolProperty into a JSON Schema compatible map.
func (p *ToolProperty) ToSchema() map[string]interface{} {
	schema := map[string]interface{}{
		"type": p.Type,
	}
	if p.Description != "" {
		schema["description"] = p.Description
	}
	if p.Items != nil {
		schema["items"] = p.Items.ToSchema()
	}
	if p.Properties != nil {
		props := make(map[string]interface{})
		for k, v := range p.Properties {
			props[k] = v.ToSchema()
		}
		schema["properties"] = props
	}
	if len(p.Required) > 0 {
		schema["required"] = p.Required
	}
	return schema
}

// ToolParameters defines the parameters of a tool
type ToolParameters struct {
	Type       string                  `json:"type"`
	Properties map[string]ToolProperty `json:"properties"`
	Required   []string                `json:"required"`
}

// ToolFunction defines a function/tool
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

// Tool represents an available tool
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolCallFunction represents a function call
type ToolCallFunction struct {
	Name         string                 `json:"name"`
	Arguments    map[string]interface{} `json:"arguments"`
	ArgumentsRaw string                 `json:"-"` // original string, not serialized
}

// ToolCall represents a tool call
type ToolCall struct {
	Function         ToolCallFunction `json:"function"`
	ID               string           `json:"id,omitempty"`
	Type             string           `json:"type,omitempty"`
	ThoughtSignature string           `json:"thought_signature,omitempty"` // NEW
}

// Message represents a message in the chat
type Message struct {
	Role               string `json:"role"`
	Content            string `json:"content"`
	ReasoningContent   string `json:"reasoning_content"`
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
	// ReasoningItem is the opaque, provider-specific reasoning state to replay
	// verbatim on the next turn. On the OpenAI Responses API it is the full
	// reasoning item (id + encrypted_content), needed to keep reasoning
	// continuity when store is false.
	ReasoningItem string `json:"reasoning_item,omitempty"`
	// ReasoningModel is the model that produced ReasoningItem. On the Responses
	// API the encrypted state is model-bound, so a hot model switch must not
	// replay an item the new model cannot read. Empty means "unknown producer"
	// (sessions written by earlier builds): those items are replayed as before.
	ReasoningModel string     `json:"reasoning_model,omitempty"`
	Name           string     `json:"name,omitempty"`
	ToolCalls      []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID     string     `json:"tool_call_id,omitempty"`
	// Images holds optional binary attachments (base64) for this message.
	// Used by tool results that produce screenshots (computer use).
	Images []ImageAttachment `json:"images,omitempty"`
}

// ImageAttachment is a base64-encoded image attached to a message (e.g. a
// computer-use screenshot or a file read with read_media). Data must not
// include the "data:" URL prefix.
type ImageAttachment struct {
	MIMEType string `json:"mime_type"` // e.g. "image/jpeg"
	Data     string `json:"data"`      // base64 (StdEncoding)
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	// Name is an optional display label used by providers that carry one
	// (e.g. Gemini inlineData.displayName). Empty means "no name".
	Name string `json:"name,omitempty"`
}

// ToolResult is the rich result of a tool execution: a text payload for the
// UI/history plus optional images that only image-capable providers receive.
type ToolResult struct {
	Text   string
	Images []ImageAttachment
}

// ToolResultFailed reports whether a tool's textual result denotes a failure.
// It relies on the shared convention used across the tools package: the first
// non-empty line starts with a known error prefix ("error:", "<word> error:",
// "failed", "sandbox:", "command failed"). Only the first line is inspected so
// that legitimate output (a file that merely mentions "failed", a log line) is
// never mistaken for a tool error.
func ToolResultFailed(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return isFailureLine(trimmed)
	}
	return false
}

func isFailureLine(line string) bool {
	lower := strings.ToLower(line)
	switch {
	case strings.HasPrefix(lower, "error:"),
		strings.HasPrefix(lower, "error "),
		strings.HasPrefix(lower, "failed"),
		strings.HasPrefix(lower, "sandbox:"),
		strings.HasPrefix(lower, "command failed"):
		return true
	}
	// "<word> error:" prefixes (MCP error:, lsp error:, copy error:, ...).
	if i := strings.Index(lower, " error:"); i > 0 {
		prefix := lower[:i]
		if prefix != "" && !strings.ContainsAny(prefix, " \t") {
			return true
		}
	}
	return false
}

// StreamResult from the API
type StreamResult struct {
	Content            string
	ReasoningContent   string
	ReasoningSignature string
	ReasoningItem      string
	ToolCalls          []ToolCall
	PromptTokens       int
	CompletionTokens   int
	FinishReason       string
	CachedTokens       int
	// Interrupted is true if generation was interrupted via context cancel
	Interrupted bool
}

// MCPServerConfig represents the configuration of an MCP server.
// Transport: "stdio" (default), "sse", "streamableHttp"
// For "sse" and "streamableHttp", Endpoint is the MCP server URL.
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport,omitempty"` // "stdio", "sse", "streamableHttp"
	Endpoint  string            `json:"endpoint,omitempty"`  // URL for HTTP transports
	Headers   map[string]string `json:"headers,omitempty"`   // custom HTTP headers
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
}

// ToolCallDisplay holds a single tool call entry for live UI rendering.
type ToolCallDisplay struct {
	Label  string
	Result string
	Status string // "running", "done", "error"
}

// SubagentBuffer is a thread-safe OutputSink that captures the live activity
// of a single sub-agent for real-time UI rendering.
type SubagentBuffer struct {
	mu          sync.Mutex
	ID          string
	Label       string
	Text        strings.Builder
	Reasoning   strings.Builder
	ToolCalls   []ToolCallDisplay
	Spinner     string
	ToolResult  string
	Done        bool
	FinalResult string
}

// DiffLineKind classifies a single line of a diff. It is intentionally
// UI-agnostic (the TUI aliases it in package ui) so the interaction/broker
// layer can carry diff payloads without depending on the terminal UI.
type DiffLineKind int

const (
	DiffContext    DiffLineKind = iota // context line (' ')
	DiffAdded                          // addition line ('+')
	DiffRemoved                        // deletion line ('-')
	DiffHunkHeader                     // @@ hunk header
)

// DiffLine represents one line of a diff with its semantic kind. The JSON tags
// make the type usable as an AG-UI wire payload (human-in-the-loop approvals
// carry the diff so the client can render it instead of the TUI viewer).
type DiffLine struct {
	Kind    DiffLineKind `json:"kind"`
	Content string       `json:"content"`
	LineNum int          `json:"lineNum,omitempty"` // 1-based (0 = unknown, e.g. hunk headers)
}

// DetailLineKind classifies a single line of an approval's "See details"
// payload. Like DiffLineKind it stays UI-agnostic: the interaction layer
// carries it and the terminal UI decides how to paint it.
type DetailLineKind int

const (
	DetailText    DetailLineKind = iota // free-form text (no styling)
	DetailSection                       // a section heading
	DetailCommand                       // a shell command line (shell-highlighted)
	DetailPath                          // a filesystem path
	DetailEnvKey                        // an environment variable name
)

// DetailLine is one line of the "See details" payload with a semantic kind, so
// the viewer can colour it (and run the shell lexer over the command section)
// instead of dumping a wall of plain text. It is an internal contract: the
// AG-UI adapter flattens it to []string (DetailLinesText), so the wire payload
// stays exactly the string array clients already know.
type DetailLine struct {
	Kind DetailLineKind
	Text string
}

// DetailLinesText flattens typed detail lines to the plain strings the AG-UI
// wire carries. A client that ignores the kinds behaves exactly as before.
func DetailLinesText(lines []DetailLine) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// Provider interface represents an AI provider
type Provider interface {
	// StreamChat accepts a context: if cancelled during generation,
	// it interrupts the stream and returns what has been received so far (Interrupted=true).
	StreamChat(ctx context.Context, messages []Message, tools []Tool, sink OutputSink) (*StreamResult, error)
}

// OutputSink encapsulates all UI output operations during streaming.
// Providers call these methods instead of writing directly to stdout.
type OutputSink interface {
	// Text: markdown token arriving from the LLM
	WriteText(token string)

	// Reasoning: thought/reasoning content
	WriteReasoning(delta string)

	// Spinner: tool call in progress
	StartSpinner(label string)
	StopSpinner()

	// DiffCraft: real-time replace_anchors stats
	WriteDiffCraft(add, del int)
	FinishDiffCraft()

	// Tool output: synthetic result of a tool executed in the chat loop
	WriteToolOutput(label, res string)

	// Generic events
	WriteInterrupt()
	WriteNewline()

	// Stats: provider info and token count
	WriteStats(providerInfo, tokenStats string)

	// Generation lifecycle
	Reset()
	Finalize()

	// SelectOption shows an option selector to the user.
	SelectOption(title string, options []string) int

	// ReadLine reads a line of input from the user.
	ReadLine() (string, bool)

	// Subagent lifecycle — progress reporting during spawn_subagents.
	StartSubagent(id, label string)
	UpdateSubagent(id, status string)
	FinishSubagent(id, label, result string)
	StartSubagentBatch(count int)
	FinishSubagentBatch()
	// StartSubagentPanel passes the live SubagentPanel to the chat model for rendering.
	StartSubagentPanel(panel interface{})
}

// ToolEventSink is an optional extension of OutputSink implemented by sinks
// that can report structured tool-call lifecycle events (AG-UI style). The
// interactive TUI sink does not implement it and keeps using WriteToolOutput;
// the AG-UI SSE sink does, so handleToolCalls can emit TOOL_CALL_* events
// without any breaking change to the OutputSink contract.
type ToolEventSink interface {
	ToolCallStarted(id, name string, args map[string]interface{})
	ToolCallFinished(id, name, result string)
}

// DiffCraftSink is an optional extension of OutputSink for sinks that can show
// the target of a live patch (replace_anchors) operation. When a sink does not
// implement it, providers fall back to WriteDiffCraft(add, del). The label is
// optional on purpose: the file path is not always known yet while the tool
// arguments are still streaming in, so it may be a generic "patching file".
type DiffCraftSink interface {
	WriteDiffCraftFor(label string, add, del int)
}

// StatusLineSink is an optional extension of OutputSink for sinks that can
// render a custom line on the status bar (the output of the configured
// status-bar hook). Sinks that do not implement it simply never show the line;
// the AG-UI and silent sinks ignore it on purpose.
type StatusLineSink interface {
	WriteStatusLine(text string)
}
