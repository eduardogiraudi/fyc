package ui

import (
	"fmt"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// ReasoningBlock is a finished reasoning trace as the chat draws it: one summary
// row that unfolds the whole text on a click. The trace is UI-only and never
// reaches the conversation history, which keeps whatever the provider needs to
// replay its own reasoning state.
type ReasoningBlock struct {
	Label string
	Text  string

	expanded bool
	// hovered/hoveredID are the control under the pointer: its row is drawn
	// white on the hover background instead of grey.
	hovered   bool
	hoveredID int
}

// NewReasoningBlock builds the collapsed block for a finished reasoning trace.
func NewReasoningBlock(label, text string) *ReasoningBlock {
	return &ReasoningBlock{Label: label, Text: text}
}

// SetHover marks the control under the pointer (a negative id clears it) and
// reports whether the drawing changes.
func (r *ReasoningBlock) SetHover(id int) bool {
	if id < 0 {
		if !r.hovered {
			return false
		}
		r.hovered = false
		return true
	}
	if r.hovered && r.hoveredID == id {
		return false
	}
	r.hovered, r.hoveredID = true, id
	return true
}

// Toggle performs the control id and reports whether the display changed.
func (r *ReasoningBlock) Toggle(id int) bool {
	switch id {
	case ControlExpandResult:
		if r.expanded {
			return false
		}
		r.expanded = true
	case ControlCollapseResult:
		if !r.expanded {
			return false
		}
		r.expanded = false
	default:
		return false
	}
	return true
}

// Rows renders the block: the summary row alone while collapsed, the whole trace
// while expanded. Controls reports which returned rows a click can act on,
// exactly like ToolOutput.
func (r *ReasoningBlock) Rows(width int) ([]string, []BlockControl) {
	if width <= 0 {
		width = 80
	}
	const prefixCols = 2
	contentWidth := width - prefixCols
	if contentWidth < 20 {
		contentWidth = 20
	}
	label := strings.TrimSpace(r.Label)
	if label == "" {
		label = "reasoning"
	}
	trace := stripControlSequences(r.Text)

	type reasoningRow struct {
		text string
		// plain is the same text without styles: a hovered control is drawn with
		// one style, so its own colours must not survive under the hover colour.
		plain string
		// control marks a clickable row: it is drawn with one style, so the
		// pointer can turn its grey text white.
		control bool
		id      int
	}
	// The summary is the inline REPL's ❖ line: a finished trace must read the
	// same in both front-ends.
	summary := fmt.Sprintf("%s %s", OkStyle.Render("❖"), InfoStyle.Render(label))
	var body []reasoningRow
	switch {
	case !r.expanded && strings.TrimSpace(trace) == "":
		// Nothing to unfold (the provider sent no visible trace): the summary
		// stays, without advertising a click that would reveal nothing.
		body = append(body, reasoningRow{text: summary})
	case !r.expanded:
		body = append(body, reasoningRow{
			text:    summary + GrayStyle.Render(" · click to expand"),
			plain:   fmt.Sprintf("❖ %s · click to expand", label),
			control: true,
			id:      ControlExpandResult,
		})
	default:
		body = append(body, reasoningRow{text: summary})
		for _, line := range strings.Split(trace, "\n") {
			for _, wrapped := range wrapPlainByANSIWidth(expandTabs(line, tabStopWidth), contentWidth) {
				if strings.TrimSpace(wrapped) == "" {
					body = append(body, reasoningRow{text: GrayStyle.Render("│")})
					continue
				}
				body = append(body, reasoningRow{text: GrayStyle.Render("│") + " " + DimStyle.Render(wrapped)})
			}
		}
		body = append(body, reasoningRow{
			text:    GrayStyle.Render("╰ ⋯ hide reasoning"),
			plain:   "╰ ⋯ hide reasoning",
			control: true,
			id:      ControlCollapseResult,
		})
	}

	out := make([]string, 0, len(body))
	var controls []BlockControl
	for _, row := range body {
		if row.control && r.hovered && r.hoveredID == row.id {
			plain := row.plain
			if plain == "" {
				plain = stripControlSequences(row.text)
			}
			out = append(out, ToolControlHoverStyle.Render(plain))
		} else {
			out = append(out, row.text)
		}
		if row.control {
			controls = append(controls, BlockControl{Row: len(out) - 1, ID: row.id})
		}
	}
	out = clampLines(out, width)
	for i := range controls {
		controls[i].Width = xansi.StringWidth(out[controls[i].Row])
	}
	return out, controls
}
