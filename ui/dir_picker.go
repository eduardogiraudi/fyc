package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
)

// dirPickerModel is the folder chooser behind /cd. It wraps the bubbles file
// picker with file selection disabled and one extra key: space confirms the
// folder currently open, enter/l still descend into the highlighted directory,
// and esc/ctrl+c cancel (the picker's own esc "go up" binding would be read as a
// cancellation anyway).
type dirPickerModel struct {
	picker  filepicker.Model
	title   string
	allowed map[string]bool
	value   string
	ok      bool
	done    bool
	width   int
	height  int
}

func newDirPickerModel(title, start string, allowed []string) dirPickerModel {
	fp := filepicker.New()
	fp.DirAllowed = false
	fp.FileAllowed = false
	fp.ShowHidden = false
	fp.ShowSize = false
	fp.ShowPermissions = false
	fp.AutoHeight = false
	fp.SetHeight(10)

	start = strings.TrimSpace(start)
	if info, err := os.Stat(start); err != nil || !info.IsDir() {
		start, _ = os.UserHomeDir()
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	fp.CurrentDirectory = start

	set := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		set[a] = true
	}
	return dirPickerModel{picker: fp, title: title, allowed: set}
}

func (m dirPickerModel) Init() tea.Cmd { return m.picker.Init() }

func (m dirPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Title, current directory and the hint line live outside the picker.
		m.picker.SetHeight(max(3, msg.Height-4))
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			return m, tea.Quit
		case tea.KeySpace:
			m.value = m.picker.CurrentDirectory
			m.ok = true
			m.done = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	return m, cmd
}

func (m dirPickerModel) View() string {
	if m.done {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = terminalWidth()
	}
	dir := m.picker.CurrentDirectory

	var b strings.Builder
	b.WriteString(TitleStyle.Render(m.title) + "\n")
	b.WriteString(DimStyle.Render("  " + dir))
	if m.allowed[dir] {
		b.WriteString(" " + OkStyle.Render("(already allowed)"))
	}
	b.WriteString("\n")
	b.WriteString(m.picker.View() + "\n")
	b.WriteString(HintStyle.Render("  enter open · space select this folder · h back · esc cancel"))
	return ClampBlock(b.String(), width)
}

// PickDirectory asks for a folder, starting at start. The second result is false
// when the user cancelled. allowed lists the roots already permitted, so the
// picker can say which one is redundant; it is only a hint.
func PickDirectory(title, start string, allowed []string) (string, bool) {
	model := newDirPickerModel(title, start, allowed)
	if final, ok := runInApp(model); ok {
		m := final.(dirPickerModel)
		return m.value, m.ok
	}
	res := withExternalPrompt(func() struct {
		value string
		ok    bool
	} {
		p := tea.NewProgram(model)
		final, _ := p.Run()
		m, _ := final.(dirPickerModel)
		return struct {
			value string
			ok    bool
		}{m.value, m.ok}
	})
	return res.value, res.ok
}
