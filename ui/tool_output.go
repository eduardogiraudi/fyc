package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"fuckyourcli/types"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// toolOutputLines builds the display lines for a tool call: the (wrapped)
// label, then the result, hard-capped to maxRows. Both renderers — the Tea
// chat and the plain terminal fallback — go through it, so the display can
// never emit a line wider than the terminal.
func toolOutputLines(label, res string, width, maxRows int) []string {
	if width <= 0 {
		width = 80
	}
	if maxRows <= 0 {
		maxRows = 2
	}
	const prefixCols = 4
	contentWidth := width - prefixCols
	if contentWidth < 20 {
		contentWidth = 20
	}

	failed := types.ToolResultFailed(res)

	var out []string
	if strings.TrimSpace(label) != "" {
		icon, labelStyle := ToolStyle.Render("☉"), InfoStyle
		if failed {
			icon, labelStyle = ErrorStyle.Render("✗"), ErrorStyle
		}
		// The label is untrusted too ("ran '<cmd>'", paths from the model), so
		// it is wrapped instead of being printed verbatim.
		label = expandTabs(stripControlSequences(label), tabStopWidth)
		for i, l := range wrapPlainByANSIWidth(label, width-2) {
			if i > 0 {
				out = append(out, fmt.Sprintf("  %s", labelStyle.Render(l)))
				continue
			}
			out = append(out, fmt.Sprintf("%s %s", icon, labelStyle.Render(l)))
		}
	}

	// Tool results are untrusted payload: drop every escape/control sequence
	// (an OSC or an unterminated CSI would be swallowed by the terminal
	// together with the UI drawn after it) and expand tabs, so that the width
	// we measure is the width the terminal prints.
	res = stripControlSequences(res)
	if strings.TrimSpace(res) == "" {
		if len(out) == 0 {
			return nil
		}
		out = append(out, fmt.Sprintf("%s %s", GrayStyle.Render("╰"), GrayStyle.Render("(no output)")))
		return clampLines(out, width)
	}
	formattedRes := res
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(res), "", "  "); err == nil {
		formattedRes = prettyJSON.String()
	}
	var visualLines []string
	for _, line := range strings.Split(formattedRes, "\n") {
		if line == "" {
			visualLines = append(visualLines, "")
			continue
		}
		visualLines = append(visualLines, wrapPlainByANSIWidth(expandTabs(line, tabStopWidth), contentWidth)...)
	}
	for i, line := range visualLines {
		if i >= maxRows {
			remaining := len(visualLines) - i
			out = append(out, fmt.Sprintf("%s %s", GrayStyle.Render("╰"), GrayStyle.Render(fmt.Sprintf("… %d more lines hidden", remaining))))
			break
		}
		prefix := "│"
		if i == len(visualLines)-1 {
			prefix = "╰"
		}
		out = append(out, fmt.Sprintf("%s %s", GrayStyle.Render(prefix), BlueStyle.Render(line)))
	}
	if len(out) == 0 {
		return nil
	}
	return clampLines(out, width)
}

// clampLines is the last line of defence: whatever the callers above produced,
// nothing leaves this file wider than the terminal or with a style left open.
func clampLines(lines []string, width int) []string {
	for i, l := range lines {
		lines[i] = clampLine(l, width)
	}
	return lines
}

func FormatToolOutput(label, res string, width int, maxRows int) string {
	lines := toolOutputLines(label, res, width, maxRows)
	if len(lines) == 0 {
		return ""
	}
	return "\r" + strings.Join(lines, "\n\r")
}

// wrapPlainByANSIWidth splits a single display line at width cells. Tabs are
// measured against tab stops, but callers should expand them first (see
// expandTabs) so chunk boundaries match what the terminal will draw.
func wrapPlainByANSIWidth(line string, width int) []string {
	if width <= 0 || line == "" {
		return []string{line}
	}
	// Plain ASCII (the bulk of source lines and tool output) is one cell per
	// byte, so it can be cut by slicing instead of walking the string rune by
	// rune; tab handling and width probing then cost one byte scan.
	if isPlainASCII(line) {
		return splitPlainASCII(line, width)
	}
	var out []string
	var b strings.Builder
	// cur tracks the cells already emitted into b. Measuring the candidate
	// prefix on every rune (b.String() + DisplayWidth) made this quadratic:
	// a single 4 KB minified line cost millions of allocations.
	cur := 0
	g := uniseg.NewGraphemes(line)
	for g.Next() {
		cl := g.Str()
		cw := clusterCells(cl, cur)
		if b.Len() > 0 && cur+cw > width {
			out = append(out, b.String())
			b.Reset()
			cur = 0
			cw = clusterCells(cl, cur)
		}
		b.WriteString(cl)
		cur += cw
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// clusterCells is the number of terminal cells a grapheme cluster advances
// when printed at column col, matching DisplayWidth. Clusters are measured
// whole because variation selectors and ZWJ sequences change the width of
// their base rune.
func clusterCells(cl string, col int) int {
	switch c := cl[0]; {
	case c == '\t':
		return tabStopWidth - col%tabStopWidth
	case c < 0x20 || c == 0x7f:
		return 0
	case len(cl) == 1:
		return 1
	}
	return xansi.StringWidth(cl)
}

// isPlainASCII reports whether every byte is a single-cell printable ASCII
// character: no tabs, no control bytes, no wide or combining runes.
func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f {
			return false
		}
	}
	return true
}

// splitPlainASCII cuts a single-cell ASCII line into width-sized chunks.
func splitPlainASCII(line string, width int) []string {
	out := make([]string, 0, len(line)/width+1)
	for len(line) > width {
		out = append(out, line[:width])
		line = line[width:]
	}
	return append(out, line)
}
