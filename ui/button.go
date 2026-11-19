package ui

import "github.com/charmbracelet/lipgloss"

// HoverStyle is the highlight shared by every pointer-reactive part: status-bar
// buttons and the rows of the suggestion and history popups. It only changes
// colours and weight, never a column.
var HoverStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#ffe6c2")).
	Background(lipgloss.Color("#7d4308"))

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
