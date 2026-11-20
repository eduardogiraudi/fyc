package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type mouseButton struct {
	row, start, end int
	cursor          int
	key             tea.KeyType
}

type buttonFrame struct {
	targets        []mouseButton
	origin, height int
	rows           int
}

// mouseButtons delegates actions to the original model so mouse approval has
// exactly the same cancellation and destructive-action guards as the keyboard.
type mouseButtons struct {
	model    tea.Model
	frame    *buttonFrame
	x, y     int
	hovering bool
	width    int
}

// WithMouseButtons enables clickable controls in a hosted interactive screen.
// The caller must set its viewport after placing the view in the terminal frame.
func WithMouseButtons(model tea.Model) tea.Model {
	if _, ok := model.(mouseButtons); ok {
		return model
	}
	switch model.(type) {
	case SelectorModel, MultiSelectModel, cliSettingsModel, hooksSettingsModel, statusHookSettingsModel, textPromptModel, numberPickerModel, TextViewerModel, DiffViewerModel:
		return mouseButtons{model: model, frame: &buttonFrame{}}
	}
	return model
}

// WithoutMouseButtons returns the original model, including its final answer.
func WithoutMouseButtons(model tea.Model) tea.Model {
	if m, ok := model.(mouseButtons); ok {
		return m.model
	}
	return model
}

// SetMouseViewport records the position of the view that was actually drawn.
// A negative origin means its leading rows were clipped off the screen.
func SetMouseViewport(model tea.Model, origin, height int) {
	if m, ok := model.(mouseButtons); ok {
		m.frame.origin, m.frame.height = origin, height
	}
}

// MouseViewHeight is the height of the last rendered interactive view.
func MouseViewHeight(model tea.Model) int {
	if m, ok := model.(mouseButtons); ok {
		return m.frame.rows
	}
	return 0
}

func (m mouseButtons) Init() tea.Cmd { return m.model.Init() }

func (m mouseButtons) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
	}
	if mouse, ok := msg.(tea.MouseMsg); ok {
		localY := mouse.Y - m.frame.origin
		m.x, m.y = mouse.X, mouse.Y
		m.hovering = mouse.Y >= 0 && mouse.Y < m.frame.height && localY >= 0 && localY < m.frame.rows
		if mouse.Action == tea.MouseActionMotion {
			return m, nil
		}
		if !m.hovering || mouse.Action != tea.MouseActionPress {
			return m, nil
		}
		switch mouse.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			key := tea.KeyUp
			if mouse.Button == tea.MouseButtonWheelDown {
				key = tea.KeyDown
			}
			var cmd tea.Cmd
			m.model, cmd = m.model.Update(tea.KeyMsg{Type: key})
			m.frame.targets = nil
			return m, cmd
		case tea.MouseButtonLeft:
			for _, target := range m.frame.targets {
				if localY != target.row || mouse.X < target.start || mouse.X >= target.end {
					continue
				}
				m.model = setButtonCursor(m.model, target.cursor)
				var cmd tea.Cmd
				m.model, cmd = m.model.Update(tea.KeyMsg{Type: target.key})
				m.frame.targets = nil
				return m, cmd
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.model, cmd = m.model.Update(msg)
	// Never accept clicks on geometry belonging to an earlier model state.
	m.frame.targets = nil
	return m, cmd
}

func setButtonCursor(model tea.Model, cursor int) tea.Model {
	if cursor < 0 {
		return model
	}
	switch m := model.(type) {
	case SelectorModel:
		m.Cursor = cursor
		return m
	case MultiSelectModel:
		m.Cursor = cursor
		return m
	case cliSettingsModel:
		m.cursor = cursor
		return m
	case hooksSettingsModel:
		m.cursor = cursor
		return m
	case statusHookSettingsModel:
		m.cursor = cursor
		return m
	}
	return model
}

func (m mouseButtons) View() string {
	view := m.model.View()
	m.frame.targets = nil
	if view == "" {
		m.frame.rows = 0
		return ""
	}
	rows := strings.Split(view, "\n")
	addRow := func(row, cursor int) {
		if row < 0 || row >= len(rows) {
			return
		}
		plain := ansi.Strip(rows[row])
		label := strings.TrimSpace(plain)
		if label == "" {
			return
		}
		start := ansi.StringWidth(plain) - ansi.StringWidth(strings.TrimLeft(plain, " \t"))
		m.frame.targets = append(m.frame.targets, mouseButton{row, start, start + ansi.StringWidth(label), cursor, tea.KeyEnter})
	}
	addRows := func(row, cursor int, content string, enabled bool) int {
		n := strings.Count(content, "\n") + 1
		if enabled {
			for r := row; r < row+n; r++ {
				addRow(r, cursor)
			}
		}
		return row + n
	}
	titleRows := func(title, styled string) int {
		if styled != "" {
			title = styled
		}
		return strings.Count(title, "\n") + 1
	}
	confirm := false
	switch model := m.model.(type) {
	case SelectorModel:
		confirm = model.InputMode
		if !model.InputMode {
			row := titleRows(model.Title, model.StyledTitle)
			if model.searchable {
				row++
			}
			pinned := clampInt(model.PinnedTop, 0, len(model.Options))
			for i := 0; i < pinned; i++ {
				row = addRows(row, i, model.optionLine(i), !separatorOption(model.Options[i]))
			}
			if len(model.Options)-pinned > model.visibleCount() {
				row++
			}
			first := pinned + clampInt(model.Offset, 0, len(model.Options)-pinned)
			for i := first; i < min(first+model.visibleCount(), len(model.Options)); i++ {
				row = addRows(row, i, model.optionLine(i), !separatorOption(model.Options[i]))
			}
		}
	case MultiSelectModel:
		row := titleRows(model.Title, model.StyledTitle)
		if model.Offset > 0 {
			row++
		}
		for i := model.Offset; i < min(model.Offset+15, len(model.Options)); i++ {
			enabled := i != len(model.Options)-1 || !model.RequireSelection || model.hasSelection()
			row = addRows(row, i, model.Options[i], enabled)
		}
	case cliSettingsModel:
		for i := 0; i < 4; i++ {
			if i != 1 {
				addRow(strings.Count(model.title, "\n")+2+i, i)
			}
		}
		row := strings.Count(model.title, "\n") + 3
		if row < len(rows) {
			plain := ansi.Strip(rows[row])
			for _, arrow := range []struct {
				label string
				key   tea.KeyType
			}{{"◀", tea.KeyLeft}, {"▶", tea.KeyRight}} {
				if pos := strings.Index(plain, arrow.label); pos >= 0 {
					col := ansi.StringWidth(plain[:pos])
					m.frame.targets = append(m.frame.targets, mouseButton{row, col, col + 1, 1, arrow.key})
				}
			}
		}
	case hooksSettingsModel:
		confirm = model.editing
		if !model.editing {
			row := 2
			for i, cell := range []string{"", model.commandCell(1, model.preCmd), "", model.commandCell(3, model.postCmd), ""} {
				row = addRows(row, i, cell, true)
			}
		}
	case statusHookSettingsModel:
		confirm = model.editing
		if !model.editing {
			row := strings.Count(model.title, "\n") + 2
			for i, cell := range []string{"", model.cell(1, model.v.Command), "", "", "", ""} {
				row = addRows(row, i, cell, true)
			}
		}
	case textPromptModel:
		confirm = true
	case numberPickerModel:
		confirm = true
		// The arrows are the whole control: click them exactly where they were
		// drawn, so a wide value cannot leave a target under the wrong glyph.
		for row, line := range rows {
			plain := ansi.Strip(line)
			for _, arrow := range []struct {
				label string
				key   tea.KeyType
			}{{"◀", tea.KeyLeft}, {"▶", tea.KeyRight}} {
				if pos := strings.Index(plain, arrow.label); pos >= 0 {
					col := ansi.StringWidth(plain[:pos])
					m.frame.targets = append(m.frame.targets, mouseButton{row, col, col + 1, 0, arrow.key})
				}
			}
		}
	}
	// Explicit action buttons avoid turning explanatory text into an approval.
	footer := "  [Cancel]"
	if confirm {
		footer = "  [Confirm]  [Cancel]"
	}
	row := len(rows)
	switch m.model.(type) {
	case TextViewerModel, DiffViewerModel:
		row--
		rows[row] = footer
	default:
		rows = append(rows, footer)
	}
	if confirm {
		m.frame.targets = append(m.frame.targets, mouseButton{row, 2, 11, -1, tea.KeyEnter})
	}
	cancelCol := 2
	if confirm {
		cancelCol = 13
	}
	m.frame.targets = append(m.frame.targets, mouseButton{row, cancelCol, cancelCol + 8, -1, tea.KeyEsc})
	width := m.width
	if width <= 0 {
		width = terminalWidth()
	}
	rows = strings.Split(ClampBlock(strings.Join(rows, "\n"), width), "\n")
	targets := m.frame.targets[:0]
	for _, target := range m.frame.targets {
		target.end = min(target.end, ansi.StringWidth(rows[target.row]))
		if target.start >= target.end {
			continue
		}
		targets = append(targets, target)
		if m.hovering && target.row == m.y-m.frame.origin && m.x >= target.start && m.x < target.end {
			line := rows[target.row]
			label := ansi.Strip(ansi.Cut(line, target.start, target.end))
			rows[target.row] = ansi.Cut(line, 0, target.start) + HoverButton(label, label, true) + ansi.Cut(line, target.end, ansi.StringWidth(line))
		}
	}
	m.frame.targets = targets
	m.frame.rows = len(rows)
	return strings.Join(rows, "\n")
}

func separatorOption(option string) bool {
	return strings.HasPrefix(option, "---") || strings.HasPrefix(option, "───")
}
