package ui

import "github.com/charmbracelet/lipgloss"

// hoverBackground is the background every pointer-reactive part paints.
const hoverBackground = "#7d4308"

// HoverStyle is the highlight shared by every pointer-reactive part: status-bar
// buttons and the rows of the suggestion and history popups. It only changes
// colours and weight, never a column.
var HoverStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#ffe6c2")).
	Background(lipgloss.Color(hoverBackground))

// HoverButton draws a one-line button for the status bar. Idle it returns
// exactly what the caller would have rendered anyway; hovered it repaints the
// same label with a highlighted background. Only colours change, never a
// column, so the click target cannot slide out from under the pointer.
func HoverButton(label, idle string, hovered bool) string {
	if !hovered || label == "" {
		return idle
	}
	return HoverStyle.Render(label)
}

// ToolControlHoverStyle draws the clickable row of a tool output (the "N more
// lines hidden" marker, the changed-lines control) under the pointer: white on
// the hover background, because the row is grey otherwise. The whole row is
// rendered with this style alone — nesting it under the grey one would leave the
// inner colour in place after its first reset.
var ToolControlHoverStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#ffffff")).
	Background(lipgloss.Color(hoverBackground))
