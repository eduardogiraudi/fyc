package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// numberPickerModel is one integer adjusted with ◀/▶. It deliberately has no
// upper bound: a limit a user may need to exceed on a hard task is not a limit,
// and what actually bounds a runaway agent is its deadline. min is a floor (0
// makes the value go negative, so callers pass 1 for counts), and the page keys
// jump ten steps at a time so a large value stays reachable without typing.
type numberPickerModel struct {
	title string
	value int
	step  int
	min   int
	unit  string
	ok    bool
	done  bool
	width int
}

func (m numberPickerModel) Init() tea.Cmd { return nil }

func (m numberPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.ok, m.done = true, true
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			return m, tea.Quit
		case tea.KeyLeft:
			m.value -= m.step
		case tea.KeyRight:
			m.value += m.step
		case tea.KeyPgDown:
			m.value -= m.step * 10
		case tea.KeyPgUp:
			m.value += m.step * 10
		}
		m.value = max(m.min, m.value)
	}
	return m, nil
}

func (m numberPickerModel) View() string {
	if m.done {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = terminalWidth()
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render(m.title) + "\n\n")
	fmt.Fprintf(&b, "   ◀   %s   ▶   %s\n", TitleStyle.Render(strconv.Itoa(m.value)), m.unit)
	b.WriteString("\n" + HintStyle.Render(fmt.Sprintf(
		"  ◀/▶ adjust (%d)  ·  PgUp/PgDn (%d)  ·  ↵ confirm  ·  esc cancel", m.step, m.step*10)))
	return ClampBlock(b.String(), width)
}

// numberPickerResult carries the answer of the nested-program path, where the
// final model has to travel out of a generic value.
type numberPickerResult struct {
	value int
	ok    bool
}

// NumberPicker asks for one integer the user adjusts with the arrow keys. step
// is the size of one press, min the floor; there is no upper bound. ok is false
// when the user cancelled.
func NumberPicker(title string, value, step, min int, unit string) (int, bool) {
	if step < 1 {
		step = 1
	}
	model := numberPickerModel{
		title: title, value: max(value, min), step: step, min: min, unit: unit,
		width: terminalWidth(),
	}
	if final, ok := runInApp(model); ok {
		m := final.(numberPickerModel)
		return m.value, m.ok
	}
	res := withExternalPrompt(func() numberPickerResult {
		p := tea.NewProgram(model)
		m, _ := p.Run()
		nm := m.(numberPickerModel)
		return numberPickerResult{value: nm.value, ok: nm.ok}
	})
	return res.value, res.ok
}
