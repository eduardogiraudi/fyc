package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"fuckyourcli/types"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// ToolOutputRows is how many result rows a tool call shows in the chat; the
// rest collapses into a "… N more lines hidden" marker. Session replay reuses
// it so a resumed conversation looks exactly like the live one.
const ToolOutputRows = 2

var anchorEditSummary = regexp.MustCompile(`^updated \(\+([0-9]+) -([0-9]+) lines\)$`)

// BlockControl is a clickable row of an expandable block (see ToolOutput):
// Row is the offset inside the rendered rows and ID says which control it is.
type BlockControl struct {
	Row int
	ID  int
}

// Control ids of an expandable tool output. They are the actions a click on a
// control row can perform.
const (
	ControlExpandResult = iota
	ControlCollapseResult
	ControlOpenDetail
	ControlCloseDetail
)

// ToolOutput is a tool result as the chat displays it, with the whole payload
// kept so the alt-screen TUI can expand in place what the inline REPL only
// truncates. Detail is human-facing only (e.g. the changed lines of an anchored
// edit) and never reaches the model.
type ToolOutput struct {
	Label  string
	Result string
	Detail string
	// MaxRows is the collapsed row budget of the result; zero means
	// ToolOutputRows.
	MaxRows int
	// Interactive marks an output whose controls are clickable: the truncated
	// marker then advertises the click, and the collapse/detail rows exist only
	// where a click can reach them.
	Interactive bool

	expanded   bool
	detailOpen bool
	// hovered/hoveredID are the control under the pointer: its row is drawn
	// white on the hover background instead of grey.
	hovered   bool
	hoveredID int
}

// SetHover marks the control under the pointer (a negative id clears it) and
// reports whether the drawing changes.
func (t *ToolOutput) SetHover(id int) bool {
	if id < 0 {
		if !t.hovered {
			return false
		}
		t.hovered = false
		return true
	}
	if t.hovered && t.hoveredID == id {
		return false
	}
	t.hovered, t.hoveredID = true, id
	return true
}

// NewToolOutput builds the chat's tool result: the alt-screen transcript keeps
// it expandable, so its marker row becomes a button.
func NewToolOutput(label, res, detail string) *ToolOutput {
	return &ToolOutput{Label: label, Result: res, Detail: detail, MaxRows: ToolOutputRows, Interactive: true}
}

// Toggle performs the control id and reports whether the display changed.
func (t *ToolOutput) Toggle(id int) bool {
	switch id {
	case ControlExpandResult:
		if t.expanded {
			return false
		}
		t.expanded = true
	case ControlCollapseResult:
		if !t.expanded {
			return false
		}
		t.expanded = false
	case ControlOpenDetail:
		if t.detailOpen {
			return false
		}
		t.detailOpen = true
	case ControlCloseDetail:
		if !t.detailOpen {
			return false
		}
		t.detailOpen = false
	default:
		return false
	}
	return true
}

// Rows renders the display lines for a tool call: the (wrapped) label, then the
// result truncated to MaxRows, then the expandable parts. Both renderers — the
// Tea chat and the plain terminal fallback — go through it, so the display can
// never emit a line wider than the terminal. Controls reports which of the
// returned rows a click can act on.
func (t *ToolOutput) Rows(width int) ([]string, []BlockControl) {
	if width <= 0 {
		width = 80
	}
	maxRows := t.MaxRows
	if maxRows <= 0 {
		maxRows = ToolOutputRows
	}
	const prefixCols = 4
	contentWidth := width - prefixCols
	if contentWidth < 20 {
		contentWidth = 20
	}

	failed := types.ToolResultFailed(t.Result)

	var out []string
	if strings.TrimSpace(t.Label) != "" {
		icon, labelStyle := ToolStyle.Render("☉"), InfoStyle
		if failed {
			icon, labelStyle = ErrorStyle.Render("✗"), ErrorStyle
		}
		// The label is untrusted too ("ran '<cmd>'", paths from the model), so
		// it is wrapped instead of being printed verbatim.
		label := expandTabs(stripControlSequences(t.Label), tabStopWidth)
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
	res := stripControlSequences(t.Result)
	if strings.TrimSpace(res) == "" {
		if len(out) == 0 {
			return nil, nil
		}
		out = append(out, fmt.Sprintf("%s %s", GrayStyle.Render("╰"), GrayStyle.Render("(no output)")))
		return clampLines(out, width), nil
	}
	formattedRes := res
	firstLine, _, _ := strings.Cut(res, "\n")
	summary := anchorEditSummary.FindStringSubmatch(firstLine)
	if !failed && summary != nil {
		// Fresh anchors remain in history for the model, not in the user's summary.
		formattedRes = "+" + summary[1] + " -" + summary[2] + " lines"
	}
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

	truncated := len(visualLines) > maxRows
	shown := len(visualLines)
	if truncated && !t.expanded {
		shown = maxRows
	}
	detail := strings.TrimSpace(t.Detail)
	showDetail := t.Interactive && detail != "" && t.detailOpen

	type bodyRow struct {
		text string
		// control marks a clickable row: the tail loop draws it as one styled
		// line, so the pointer can turn its grey text white.
		control bool
		id      int
	}
	body := make([]bodyRow, 0, shown+4)
	var controls []BlockControl
	addControl := func(id int, label string) {
		controls = append(controls, BlockControl{Row: len(body), ID: id})
		body = append(body, bodyRow{text: label, control: true, id: id})
	}

	// The changed lines replace the result summary while they are open: the
	// counts (+N -M) are the collapsed view of the same patch, the diff is the
	// expanded one.
	if !showDetail {
		for i := 0; i < shown; i++ {
			body = append(body, bodyRow{text: toolResultRow(visualLines[i], failed, summary)})
		}
		switch {
		case truncated && !t.expanded && t.Interactive:
			addControl(ControlExpandResult, fmt.Sprintf("… %d more lines hidden · click to expand", len(visualLines)-shown))
		case truncated && !t.expanded:
			body = append(body, bodyRow{text: GrayStyle.Render(fmt.Sprintf("… %d more lines hidden", len(visualLines)-shown))})
		case truncated && t.Interactive:
			addControl(ControlCollapseResult, fmt.Sprintf("⋯ collapse %d lines", len(visualLines)-maxRows))
		}
	}
	if detail != "" && t.Interactive {
		if !t.detailOpen {
			addControl(ControlOpenDetail, "⋯ show changed lines")
		} else {
			for _, line := range strings.Split(detail, "\n") {
				for _, wrapped := range wrapPlainByANSIWidth(expandTabs(line, tabStopWidth), contentWidth) {
					body = append(body, bodyRow{text: toolDetailRow(wrapped)})
				}
			}
			addControl(ControlCloseDetail, "⋯ hide changed lines")
		}
	}

	// Every row but the last uses the connector; the last one closes the block.
	base := len(out)
	for i, row := range body {
		prefix := "│"
		if i == len(body)-1 {
			prefix = "╰"
		}
		switch {
		case !row.control:
			out = append(out, fmt.Sprintf("%s %s", GrayStyle.Render(prefix), row.text))
		case t.hovered && t.hoveredID == row.id:
			out = append(out, ToolControlHoverStyle.Render(prefix+" "+row.text))
		default:
			out = append(out, GrayStyle.Render(prefix+" "+row.text))
		}
	}
	for i := range controls {
		controls[i].Row += base
	}
	return clampLines(out, width), controls
}

// toolResultRow renders one row of the result, colouring the added/removed
// counters of an anchored edit summary.
func toolResultRow(line string, failed bool, summary []string) string {
	if failed || summary == nil {
		return BlueStyle.Render(line)
	}
	parts := strings.Split(line, " ")
	for j, part := range parts {
		style := BlueStyle
		if strings.HasPrefix(part, "+") {
			style = AddStyle
		} else if strings.HasPrefix(part, "-") {
			style = DeleteStyle
		}
		parts[j] = style.Render(part)
	}
	return strings.Join(parts, " ")
}

// toolDetailRow renders one row of the human-facing detail of a patch: the
// removed lines in red, the added ones in green, the context dimmed.
func toolDetailRow(line string) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return AddStyle.Render(line)
	case strings.HasPrefix(line, "-"):
		return DeleteStyle.Render(line)
	}
	return GrayStyle.Render(line)
}

func toolOutputLines(label, res string, width, maxRows int) []string {
	lines, _ := (&ToolOutput{Label: label, Result: res, MaxRows: maxRows}).Rows(width)
	return lines
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
