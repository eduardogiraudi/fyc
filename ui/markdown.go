package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
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
	return strings.TrimLeft(out, "\n")
}
