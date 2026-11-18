package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
)

// MarkdownRenderer encapsulates a glamour renderer with configurable width.
// Used by both MarkdownStreamRenderer (main agent) and PanelBuffer (subagent).
type MarkdownRenderer struct {
	renderer *glamour.TermRenderer
	width    int
}

// NewMarkdownRenderer creates a MarkdownRenderer with the given width.
func NewMarkdownRenderer(width int) *MarkdownRenderer {
	if width <= 0 {
		width = 78
	}
	return &MarkdownRenderer{
		renderer: NewCompactRenderer(width),
		width:    width,
	}
}

// WithWidth updates the width and recreates the renderer if necessary.
func (m *MarkdownRenderer) WithWidth(width int) {
	if width <= 0 {
		width = 78
	}
	if m.width != width {
		m.width = width
		m.renderer = NewCompactRenderer(width)
	}
}

// Width returns the current width.
func (m *MarkdownRenderer) Width() int { return m.width }

// Render performs preprocessLatex + glamour rendering.
func (m *MarkdownRenderer) Render(s string) string {
	if m.renderer == nil || s == "" {
		return s
	}
	s = preprocessLatex(s)
	out, err := m.renderer.Render(s)
	if err != nil {
		return s
	}
	// Glamour pads rows to the wrap width, even with zero margins. Keep
	// source indentation, but never put that decorative padding on the clipboard.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimRight(plain, " ")
		if trimmed == "" {
			lines[i] = ""
		} else if len(trimmed) != len(plain) {
			lines[i] = closeStyles(ansi.Cut(line, 0, ansi.StringWidth(trimmed)))
		}
	}
	return strings.TrimLeft(strings.Join(lines, "\n"), "\n")
}
