package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/reflow/ansi"
)

type ReasoningView struct {
	vp      viewport.Model
	tracker ReasoningTracker
	frame   int
	width   int
	height  int
}

func NewReasoningView(width, height int) ReasoningView {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 7
	}
	vp := viewport.New(width, height)
	return ReasoningView{vp: vp, width: width, height: height}
}

func (r *ReasoningView) Resize(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 7
	}
	r.width = width
	r.height = height
	r.vp.Width = width
	r.vp.Height = height
	r.refresh()
}

func (r *ReasoningView) Active() bool { return r.tracker.Active() }

func (r *ReasoningView) Append(delta string) tea.Cmd {
	if delta == "" {
		return nil
	}
	var cmd tea.Cmd
	if !r.tracker.Active() {
		cmd = ReasoningTickCmd()
	}
	r.tracker.Append(delta)
	r.refresh()
	return cmd
}

func (r *ReasoningView) Tick() tea.Cmd {
	if !r.tracker.Active() {
		return nil
	}
	r.frame++
	return ReasoningTickCmd()
}

func (r *ReasoningView) Finish() string {
	if !r.tracker.Active() {
		return ""
	}
	label := r.tracker.Finish()
	r.Clear()
	return label
}

func (r *ReasoningView) Clear() {
	r.tracker.Reset()
	r.frame = 0
	r.vp.SetContent("")
}

func (r *ReasoningView) View() string {
	if !r.tracker.Active() {
		return ""
	}
	label := fmt.Sprintf("%s %s", SpinnerFrame(r.frame), "I am thinking...")
	body := r.vp.View()
	if strings.TrimSpace(body) == "" {
		return label
	}
	return label + "\n" + body
}

func (r *ReasoningView) refresh() {
	if !r.tracker.Active() {
		return
	}
	contentWidth := r.width - 2
	if contentWidth < 20 {
		contentWidth = 20
	}
	lines := wrapANSIText(r.tracker.Text(), contentWidth)
	for i := range lines {
		lines[i] = GrayStyle.Render("│") + " " + DimStyle.Render(lines[i])
	}
	r.vp.SetContent(strings.Join(lines, "\n"))
	r.vp.GotoBottom()
}

type ReasoningTickMsg struct{}

type SpinnerTickMsg struct{}

type FlushTickMsg struct{}

func ReasoningTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return ReasoningTickMsg{} })
}

func SpinnerTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return SpinnerTickMsg{} })
}

func wrapANSIText(s string, width int) []string {
	if width <= 0 {
		width = 80
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			out = append(out, "")
			continue
		}
		var current strings.Builder
		for _, word := range strings.Fields(line) {
			if current.Len() == 0 {
				if ansi.PrintableRuneWidth(word) <= width {
					current.WriteString(word)
					continue
				}
			} else if ansi.PrintableRuneWidth(current.String())+1+ansi.PrintableRuneWidth(word) <= width {
				current.WriteByte(' ')
				current.WriteString(word)
				continue
			} else {
				out = append(out, current.String())
				current.Reset()
				if ansi.PrintableRuneWidth(word) <= width {
					current.WriteString(word)
					continue
				}
			}
			for _, chunk := range hardWrapANSI(word, width) {
				if ansi.PrintableRuneWidth(chunk) == width {
					out = append(out, chunk)
				} else {
					current.WriteString(chunk)
				}
			}
		}
		if current.Len() > 0 {
			out = append(out, current.String())
		}
	}
	return out
}

func hardWrapANSI(s string, width int) []string {
	if width <= 0 || s == "" {
		return []string{s}
	}
	var out []string
	var b strings.Builder
	for _, rr := range s {
		candidate := b.String() + string(rr)
		if b.Len() > 0 && ansi.PrintableRuneWidth(candidate) > width {
			out = append(out, b.String())
			b.Reset()
		}
		b.WriteRune(rr)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
