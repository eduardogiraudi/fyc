package ui

import (
	"fmt"
	"fuckyourcli/types"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

const (
	agentPanelContentHeight = 8
	twoColumnThreshold      = 150
	panelGap                = 1
)

// AgentPanel renders a bordered split-panel for one sub-agent.
// Uses PanelBuffer for content rendered identically to the main agent.
type AgentPanel struct {
	Buffer *PanelBuffer
	vp     viewport.Model
}

func newAgentPanel(buf *PanelBuffer) *AgentPanel {
	return &AgentPanel{
		Buffer: buf,
		vp:     viewport.New(80, agentPanelContentHeight),
	}
}

func (ap *AgentPanel) render(width int, frame int) string {
	cw := width - 4
	if cw < 20 {
		cw = 20
	}
	ap.Buffer.SetContentWidth(cw)
	segments, text, reasoning, spinner, runningTool, done := ap.Buffer.Snapshot()

	// ── Fixed header (outside viewport) ──────────────────────────
	sc := spinnerFrameChar(frame)
	var headerLine string
	switch {
	case done:
		headerLine = fmt.Sprintf("%s %s", OkStyle.Render("✓"), InfoStyle.Render(ap.Buffer.ID))
	default:
		headerLine = fmt.Sprintf("%s %s", sc, TitleStyle.Render(ap.Buffer.ID))
	}

	// ── Viewport: completed segments + current activity ──────────
	var eventLines []string

	// 1. Completed segments — sequential, like main agent
	for _, seg := range segments {
		for _, l := range seg.Lines {
			if l != "" {
				eventLines = append(eventLines, l)
			}
		}
	}

	// 2. Current in-progress activity
	if reasoning != "" {
		eventLines = append(eventLines, fmt.Sprintf(" %s %s", DimStyle.Render("◆"), DimStyle.Render("I am thinking...")))
		rLines := strings.Split(strings.TrimRight(reasoning, "\n"), "\n")
		start := 0
		if len(rLines) > agentPanelContentHeight-1 {
			start = len(rLines) - agentPanelContentHeight + 1
		}
		for _, rl := range rLines[start:] {
			line := fmt.Sprintf(" %s %s", GrayStyle.Render("│"), DimStyle.Render(truncateToANSIWidth(rl, cw)))
			eventLines = append(eventLines, line)
		}
	} else if runningTool != "" {
		eventLines = append(eventLines, fmt.Sprintf(" %s %s", spinnerFrameChar(frame), TitleStyle.Render(runningTool)))
	} else if text != "" {
		// In-progress text shown as dim raw text (will be rendered via markdown when closed)
		tLines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		avail := agentPanelContentHeight
		if avail < 1 {
			avail = 1
		}
		start := 0
		if len(tLines) > avail {
			start = len(tLines) - avail
		}
		for _, tl := range tLines[start:] {
			line := truncateToANSIWidth(tl, cw)
			if line != "" {
				eventLines = append(eventLines, " "+DimStyle.Render(line))
			}
		}
	} else if spinner != "" {
		eventLines = append(eventLines, fmt.Sprintf(" %s %s", spinnerFrameChar(frame), TitleStyle.Render(spinner)))
	}

	// Truncate to viewport height — show last agentPanelContentHeight lines
	if len(eventLines) > agentPanelContentHeight {
		eventLines = eventLines[len(eventLines)-agentPanelContentHeight:]
	}

	// Safety truncation — ensure no line exceeds panel content width
	for i := range eventLines {
		eventLines[i] = truncateToANSIWidth(eventLines[i], cw)
	}
	vpContent := strings.Join(eventLines, "\n")
	ap.vp.Width = cw
	ap.vp.Height = agentPanelContentHeight
	ap.vp.SetContent(vpContent)
	ap.vp.GotoBottom()
	vpView := ap.vp.View()

	fullBody := headerLine + "\n" + vpView

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("250")).
		Width(cw)
	return style.Render(fullBody)
}

// SubagentPanel renders live split panels for concurrent sub-agents.
type SubagentPanel struct {
	mu     sync.Mutex
	agents []*AgentPanel
	frame  int
}

func NewSubagentPanel() *SubagentPanel {
	return &SubagentPanel{}
}

// AddAgent creates a PanelBuffer and returns it as types.OutputSink.
func (p *SubagentPanel) AddAgent(id, label string) types.OutputSink {
	buf := NewPanelBuffer(id, label)
	p.mu.Lock()
	p.agents = append(p.agents, newAgentPanel(buf))
	p.mu.Unlock()
	return buf
}

func (p *SubagentPanel) AllDone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.agents) == 0 {
		return false
	}
	for _, a := range p.agents {
		_, _, _, _, _, done := a.Buffer.Snapshot()
		if !done {
			return false
		}
	}
	return true
}

func (p *SubagentPanel) Tick() {
	p.mu.Lock()
	p.frame++
	p.mu.Unlock()
}

func (p *SubagentPanel) View(width int) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if width <= 0 {
		width = 80
	}
	if len(p.agents) == 0 {
		return ""
	}

	anyActive := false
	for _, a := range p.agents {
		_, _, _, _, _, done := a.Buffer.Snapshot()
		if !done {
			anyActive = true
			break
		}
	}
	if !anyActive {
		return ""
	}

	frame := p.frame
	maxVisible := 4
	useTwoColumns := width >= twoColumnThreshold

	var visible []*AgentPanel
	hiddenCount := 0
	for _, a := range p.agents {
		if len(visible) >= maxVisible {
			hiddenCount++
			continue
		}
		visible = append(visible, a)
	}
	if len(visible) == 0 {
		return ""
	}

	var rows []string

	if useTwoColumns && len(visible) > 1 {
		colWidth := (width - panelGap) / 2
		if colWidth < 40 {
			colWidth = 40
		}
		i := 0
		for i < len(visible) {
			if i+1 < len(visible) {
				left := visible[i].render(colWidth, frame)
				right := visible[i+1].render(colWidth, frame)
				row := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", panelGap), right)
				rows = append(rows, row)
				i += 2
			} else {
				rows = append(rows, visible[i].render(width, frame))
				i++
			}
		}
	} else {
		for _, a := range visible {
			rows = append(rows, a.render(width, frame))
		}
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(rows, "\n"))
	if hiddenCount > 0 {
		sb.WriteString(fmt.Sprintf("\n%s %s", GrayStyle.Render("╰"), GrayStyle.Render(fmt.Sprintf("%d agents hidden", hiddenCount))))
	}
	return sb.String()
}

func spinnerFrameChar(frame int) string {
	return SpinnerFrame(frame)
}
func truncateToANSIWidth(s string, maxLen int) string {
	if maxLen < 1 || s == "" {
		return ""
	}
	var visible int
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			result.WriteRune(r)
			continue
		}
		if inEscape {
			result.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if visible >= maxLen {
			break
		}
		result.WriteRune(r)
		visible++
	}
	return result.String()
}

var ErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
