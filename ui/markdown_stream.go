package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"golang.org/x/term"
)

// MarkdownStreamRenderer renders markdown incrementally during LLM streaming.
//
// Algorithm (inspired by Will McGugan's Textual streaming markdown work):
//
//  Markdown splits cleanly into top-level blocks (paragraph, heading, code
//  fence, list, table…). All blocks except the *last* are finalized: they
//  will never change as new tokens arrive. Only the last (in-progress) block
//  is mutable.
//
//  Therefore:
//   1. Accumulate every raw token in fullBuf.
//   2. Find the boundary between the last *complete* block and the
//      in-progress one = just after the last double-newline that is NOT
//      inside a code fence.
//   3. Everything up to that boundary is rendered ONCE with glamour and
//      printed, replacing the raw lines via ANSI cursor-up + erase.
//   4. The in-progress tail is printed RAW through a *visual ring buffer*:
//      only the last min(tail, termHeight-2) lines are kept on screen.
//      This bounds rawLines within the visible viewport, so eraseRawLines
//      always works even when the block is taller than the terminal.
//   5. Finalize() renders the remaining tail with glamour.
//
// Glamour is called at most once per completed top-level block, never
// per-token. Output never duplicates.

type MarkdownStreamRenderer struct {
	mu sync.Mutex

	fullBuf         strings.Builder // every raw byte received
	rendered        int             // byte offset up to which we have glamour-rendered
	rawLines        int             // visual lines currently showing on screen (ring buffer)
	lastRaw         string          // full raw tail as of last refresh (for delta computation)
	lastVisibleTail string          // lastNLines(lastRaw, maxVis) as of last refresh

	mdRenderer *MarkdownRenderer
	termHeight int
}

var customStyle = []byte(`{
  "document": {
    "color": "#e2e0d9",
    "background_color": "",
    "margin": 1
  },
  "paragraph": {
    "color": "#e2e0d9",
    "margin": 0
  },
  "heading": {
    "color": "#fc8403",
    "bold": true,
    "block_prefix": "\n",
    "block_suffix": "\n"
  },
  "h1": { "color": "#fc8403", "bold": true, "prefix": "# " },
  "h2": { "color": "#e8a040", "bold": true, "prefix": "## " },
  "h3": { "color": "#d4a374", "bold": true, "prefix": "### " },
  "strong": { "color": "#fc8403", "bold": true },
  "emph":   { "color": "#d4a374", "italic": true },
  "hr": {
    "color": "#fc8403",
    "format": "\n──────────────────────────────\n"
  },
  "item": {
    "color": "#e2e0d9",
    "block_prefix": "• ",
    "block_suffix": ""
  },
  "list_item": {
    "color": "#e2e0d9",
    "block_prefix": " "
  },
  "enumeration": {
    "color": "#e2e0d9",
    "block_prefix": ". "
  },
  "list": { "margin": 0 },
  "code": {
    "color": "#fc8403",
    "background_color": "#1e1c18",
    "prefix": " ",
    "suffix": " "
  },
  "code_block": {
      "color": "#cdd6f4",
      "background_color": "#1e1e2e",
      "margin": 1,
      "theme": "monokai",
      "chroma": {
        "text":              { "color": "#cdd6f4" },
        "error":             { "color": "#f38ba8" },
        "comment":           { "color": "#6c7086", "italic": true },
        "comment_preproc":   { "color": "#f5c2e7" }, 
        "keyword":           { "color": "#fc8403", "bold": true },
        "keyword_reserved":  { "color": "#fc8403", "bold": true },
        "keyword_namespace": { "color": "#fc8403" },
        "keyword_type":      { "color": "#89b4fa" },
        "operator":          { "color": "#94e2d5" }, 
        "punctuation":       { "color": "#a6adc8" }, 
        "name":              { "color": "#cdd6f4" },
        "name_builtin":      { "color": "#89b4fa" },
        "name_tag":          { "color": "#fc8403" },
        "name_attribute":    { "color": "#89b4fa" },
        "name_class":        { "color": "#89b4fa", "bold": true },
        "name_constant":     { "color": "#f9e2af" }, 
        "name_decorator":    { "color": "#fc8403" },
        "name_exception":    { "color": "#f38ba8" },
        "name_function":     { "color": "#a6e3a1" },
        "name_other":        { "color": "#cdd6f4" },
        "literal":           { "color": "#f9e2af" }, 
        "literal_date":      { "color": "#a6e3a1" },
        "literal_string":    { "color": "#a6e3a1" },
        "literal_number":    { "color": "#f9e2af" } 
      }
  },
  "table": {
    "color": "#e2e0d9",
    "center_separator": "┼",
    "column_separator": "│",
    "row_separator": "─"
  },
  "definition_term":        { "color": "#fc8403", "bold": true },
  "definition_description": { "color": "#d4a374" },
  "blockquote": {
    "color": "#a09880",
    "indent": 1,
    "indent_token": "│ "
  },
  "link":       { "color": "#6ab0f5" },
  "link_text":  { "color": "#6ab0f5", "underline": true },
  "image_text": { "color": "#888780" },
  "strikethrough": { "color": "#5f5e5a", "crossed_out": true }
}`)

func NewCompactRenderer(width int) *glamour.TermRenderer {
	r, _ := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(customStyle),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter("terminal256"),
	)
	return r
}

func NewMarkdownStreamRenderer() *MarkdownStreamRenderer {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	return &MarkdownStreamRenderer{
		mdRenderer: NewMarkdownRenderer(width),
		termHeight: height,
	}
}

// Write receives the next raw token from the LLM and updates the display.
func (m *MarkdownStreamRenderer) Write(token string) string {
	if token == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fullBuf.WriteString(token)
	m.refresh()
	return token
}

// Finalize renders any remaining in-progress tail with glamour.
// Call once after the stream ends.
func (m *MarkdownStreamRenderer) Finalize() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	full := m.fullBuf.String()
	tail := full[m.rendered:]
	if tail == "" {
		return ""
	}
	m.eraseRawLines()
	out := m.mdRenderer.Render(tail)
	fmt.Print(out)
	m.rendered = len(full)
	m.rawLines = 0
	m.lastRaw = ""
	m.lastVisibleTail = ""
	return out
}

// SetWidth updates the render width, e.g. after a terminal resize. The
// renderer is created with whatever width was current when it was first used,
// so a resize without this call would keep wrapping every line at the old
// width and the lines would overflow the terminal.
func (m *MarkdownStreamRenderer) SetWidth(width int) {
	if width <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mdRenderer.WithWidth(width)
}

// Clear resets the renderer for a new response.
func (m *MarkdownStreamRenderer) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fullBuf.Reset()
	m.rendered = 0
	m.rawLines = 0
	m.lastRaw = ""
	m.lastVisibleTail = ""
}

func (m *MarkdownStreamRenderer) GetBuffer() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fullBuf.String()
}

// GetRendered kept for API compatibility.
func (m *MarkdownStreamRenderer) GetRendered() string {
	return m.GetBuffer()
}

// ── core logic ────────────────────────────────────────────────────────────────

func (m *MarkdownStreamRenderer) refresh() {
	// Always update terminal dimensions.
	currWidth, currHeight, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil {
		if currWidth > 0 && currWidth != m.mdRenderer.Width() {
			m.mdRenderer.WithWidth(currWidth)
		}
		if currHeight > 0 {
			m.termHeight = currHeight
		}
	}

	full := m.fullBuf.String()
	unrendered := full[m.rendered:]

	boundary := LastCompletedBlockBoundary(unrendered)

	if boundary > 0 {
		completedRaw := unrendered[:boundary]

		// Clear current raw lines (always within viewport thanks to the ring buffer).
		m.eraseRawLines()

		fmt.Print(m.mdRenderer.Render(completedRaw))
		m.rendered += boundary
		m.rawLines = 0
		m.lastRaw = ""
	}

	// ── Visual ring buffer ────────────────────────────────────────────────────
	//
	// The problem: if the raw tail is taller than the terminal, the cursor cannot
	// move up enough to clear it (ESC[NA stops at line 0 of the
	// viewport). Solution: always show only the last maxVis lines of the
	// tail. rawLines remains ≤ maxVis, so eraseRawLines always works.
	//
	// Fast path  → the tail grew at the end: print only new characters.
	// Slow path  → the tail "scrolled" (a line left the window):
	//              clear and redraw the window.

	rawTail := full[m.rendered:]
	if rawTail == "" {
		return
	}

	maxVis := m.termHeight - 2 // 2-line margin for the prompt
	if maxVis < 5 {
		maxVis = 5
	}

	visibleTail := lastNLines(rawTail, maxVis)
	prevVisibleTail := m.lastVisibleTail

	if strings.HasPrefix(visibleTail, prevVisibleTail) {
		// Fast path: the window hasn't moved, just append new characters.
		newChars := visibleTail[len(prevVisibleTail):]
		if newChars != "" {
			fmt.Print(newChars)
		}
	} else {
		// Slow path: a line has scrolled out of the top of the ring buffer.
		// Clear the current window and redraw the updated one.
		m.eraseRawLines()
		fmt.Print(visibleTail)
	}

	m.lastRaw = rawTail                                        // keep the full tail for the next delta
	m.lastVisibleTail = visibleTail                            // save the visible window for the next comparison
	m.rawLines = countLines(visibleTail, m.mdRenderer.Width()) // count only visible lines
}

// lastNLines returns the suffix of s that contains at most n newline-delimited
// lines. It is the "window" function of the visual ring buffer.
//
// Edge cases:
//   - n <= 0  → ""
//   - s has fewer than n lines → s unchanged
//   - s ends with "\n" → the trailing empty segment is counted as a line
func lastNLines(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	// strings.Split always yields ≥1 segment, even for "".
	segs := strings.Split(s, "\n")
	if len(segs) <= n {
		return s
	}
	return strings.Join(segs[len(segs)-n:], "\n")
}

// ansiEscape matches ANSI SGR color/style sequences so they can be stripped
// before computing visual line lengths.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// countLines returns how many lines ABOVE the cursor the string s occupies,
// i.e. the number of ESC[A moves needed to reach the start of s.
//
// The cursor is always ON the last line of output (not below it), so:
//
//	"hello"        → 0  (single line, cursor on it)
//	"hello\n"      → 1  (cursor moved to new empty line; "hello" is 1 above)
//	"hello\nworld" → 1  (cursor on "world"; "hello" is 1 above)
//	"a\nb\nc\n"    → 3
//
// Lines longer than width wrap and count as multiple visual lines.
func countLines(s string, width int) int {
	if s == "" || width <= 0 {
		return 0
	}

	segments := strings.Split(s, "\n")
	totalVisualLines := 0

	for _, seg := range segments {
		visualLen := len([]rune(stripANSI(seg)))
		if visualLen == 0 {
			totalVisualLines += 1
		} else {
			totalVisualLines += (visualLen + width - 1) / width
		}
	}

	// Return the lines "above" the cursor.
	// If the text occupies 3 lines, the cursor is on the third, so we must move up by 2.
	return totalVisualLines - 1
}

// eraseRawLines moves the cursor up rawLines lines and clears to end of screen,
// effectively removing everything that was printed as raw tail.
func (m *MarkdownStreamRenderer) eraseRawLines() {
	if m.rawLines < 0 {
		m.rawLines = 0
		return
	}
	if m.rawLines == 0 {
		ClearLine()
		m.lastVisibleTail = ""
		return
	}
	EraseLines(m.rawLines)
	m.rawLines = 0
	m.lastVisibleTail = ""
}

// lastCompletedBlockBoundary returns the byte offset just after the last
// top-level block boundary (blank line outside a code fence) in s.
// Returns 0 if no complete block boundary found.
func LastCompletedBlockBoundary(s string) int {
	inFence := false
	last := 0
	pos := 0

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Toggle code fence state.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}

		lineEnd := pos + len(line) + 1 // +1 for the consumed \n
		if lineEnd > len(s) {
			lineEnd = len(s)
		}

		// Blank line outside fence = block boundary.
		if !inFence && trimmed == "" && i > 0 {
			last = lineEnd
		}

		pos = lineEnd
	}

	return last
}

func (m *MarkdownStreamRenderer) RenderMarkdown(s string) string {
	return m.mdRenderer.Render(s)
}

// ── global singleton ──────────────────────────────────────────────────────────

var (
	globalStreamRenderer *MarkdownStreamRenderer
	streamRendererMu     sync.Mutex
)

func GetStreamRenderer() *MarkdownStreamRenderer {
	streamRendererMu.Lock()
	defer streamRendererMu.Unlock()
	if globalStreamRenderer == nil {
		globalStreamRenderer = NewMarkdownStreamRenderer()
	}
	return globalStreamRenderer
}
