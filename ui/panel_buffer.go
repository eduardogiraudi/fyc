package ui

import (
	"fmt"
	"strings"
	"sync"

	"fuckyourcli/types"
)

// SegmentKind indicates the type of a completed display segment.
type SegmentKind int

const (
	SegmentText SegmentKind = iota
	SegmentReasoning
	SegmentTool
)

// PanelSegment is a pre-rendered completed segment in the subagent's display,
// mirroring the main agent's sequential output.
type PanelSegment struct {
	Kind      SegmentKind
	Lines     []string // pre-rendered display lines (ANSI)
	ToolLabel string   // for SegmentTool
}

// PanelBuffer implements types.OutputSink for sub-agent panels.
// It maintains a sequential list of completed segments (text, reasoning, tool)
// and shows current in-progress activity, mirroring the main agent's display.
type PanelBuffer struct {
	mu    sync.Mutex
	ID    string
	Label string

	segments []PanelSegment

	textBuf     strings.Builder
	reasoning   ReasoningTracker
	Spinner     string
	RunningTool string

	Done         bool
	FinalResult  string
	contentWidth int
	mdRenderer   *MarkdownRenderer
}

func NewPanelBuffer(id, label string) *PanelBuffer {
	return &PanelBuffer{ID: id, Label: label}
}

// SetContentWidth updates the markdown renderer width for text wrapping.
func (pb *PanelBuffer) SetContentWidth(w int) {
	if w < 20 {
		w = 20
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.contentWidth != w {
		pb.contentWidth = w
		if pb.mdRenderer == nil {
			pb.mdRenderer = NewMarkdownRenderer(w)
		} else {
			pb.mdRenderer.WithWidth(w)
		}
	}
}

// closeTextLocked flushes accumulated text through markdown renderer.
func (pb *PanelBuffer) closeTextLocked() {
	if pb.textBuf.Len() == 0 {
		return
	}
	text := strings.TrimRight(pb.textBuf.String(), "\n")
	if text == "" {
		pb.textBuf.Reset()
		return
	}
	pb.textBuf.Reset()

	// Render via markdown renderer
	if pb.mdRenderer == nil {
		w := pb.contentWidth
		if w <= 0 {
			w = 78
		}
		pb.mdRenderer = NewMarkdownRenderer(w)
	}
	out := pb.mdRenderer.Render(text)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	pb.segments = append(pb.segments, PanelSegment{Kind: SegmentText, Lines: lines})
}

// closeReasoningLocked records the current reasoning as a collapsed event.
func (pb *PanelBuffer) closeReasoningLocked() {
	if !pb.reasoning.Active() {
		return
	}
	label := pb.reasoning.Finish()
	pb.segments = append(pb.segments, PanelSegment{
		Kind:  SegmentReasoning,
		Lines: []string{fmt.Sprintf(" %s %s", DimStyle.Render("◆"), DimStyle.Render(label))},
	})
}

func (pb *PanelBuffer) WriteText(token string) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.reasoning.Active() {
		pb.closeReasoningLocked()
	}
	pb.textBuf.WriteString(token)
	pb.flushCompletedTextLocked()
}

// flushCompletedTextLocked renders completed markdown blocks incrementally,
// mirroring the main agent's LastCompletedBlockBoundary approach.
func (pb *PanelBuffer) flushCompletedTextLocked() {
	text := pb.textBuf.String()
	boundary := LastCompletedBlockBoundary(text)
	if boundary <= 0 {
		return
	}
	completed := text[:boundary]
	pb.textBuf.Reset()
	pb.textBuf.WriteString(text[boundary:])

	// Render completed block through markdown renderer
	if pb.mdRenderer == nil {
		w := pb.contentWidth
		if w <= 0 {
			w = 78
		}
		pb.mdRenderer = NewMarkdownRenderer(w)
	}
	out := pb.mdRenderer.Render(completed)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	pb.segments = append(pb.segments, PanelSegment{Kind: SegmentText, Lines: lines})
}

func (pb *PanelBuffer) WriteReasoning(delta string) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	// Close any in-progress text first
	if pb.textBuf.Len() > 0 {
		pb.closeTextLocked()
	}
	pb.reasoning.Append(delta)
}

func (pb *PanelBuffer) StartSpinner(label string) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.textBuf.Len() > 0 {
		pb.closeTextLocked()
	}
	if pb.reasoning.Active() {
		pb.closeReasoningLocked()
	}
	pb.Spinner = label
}

func (pb *PanelBuffer) StopSpinner() {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.Spinner = ""
}

func (pb *PanelBuffer) WriteDiffCraft(add, del int) {}
func (pb *PanelBuffer) FinishDiffCraft()            {}

func (pb *PanelBuffer) WriteToolOutput(label, res string) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.textBuf.Len() > 0 {
		pb.closeTextLocked()
	}
	if pb.reasoning.Active() {
		pb.closeReasoningLocked()
	}
	if res == "" {
		pb.RunningTool = label
		return
	}
	failed := types.ToolResultFailed(res)

	icon, labelStyle := OkStyle.Render("☉"), InfoStyle
	if failed {
		icon, labelStyle = ErrorStyle.Render("✗"), ErrorStyle
	}

	maxW := pb.contentWidth
	if maxW <= 0 {
		maxW = 78
	}
	resultText, resultStyle := "(no output)", GrayStyle
	if trimmed := strings.TrimSpace(res); trimmed != "" {
		resultText = trimmed
		if idx := strings.Index(resultText, "\n"); idx >= 0 {
			resultText = resultText[:idx]
		}
		resultStyle = BlueStyle
	}
	lines := []string{
		fmt.Sprintf(" %s %s", icon, labelStyle.Render(label)),
		fmt.Sprintf(" %s %s", GrayStyle.Render("│"), resultStyle.Render(truncateToANSIWidth(resultText, maxW-3))),
	}
	pb.segments = append(pb.segments, PanelSegment{Kind: SegmentTool, Lines: lines, ToolLabel: label})
	pb.RunningTool = ""
}

func (pb *PanelBuffer) WriteInterrupt() {}

func (pb *PanelBuffer) WriteNewline() {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.textBuf.WriteString("\n")
}

func (pb *PanelBuffer) WriteStats(providerInfo, tokenStats string) {}

func (pb *PanelBuffer) Reset() {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	// Close pending reasoning → adds "◆ reasoned for Xs" segment
	if pb.reasoning.Active() {
		pb.closeReasoningLocked()
	}
	// Flush pending text → adds SegmentText via markdown
	if pb.textBuf.Len() > 0 {
		pb.closeTextLocked()
	}
	// Clear transient state (like main agent's clearTransient)
	pb.Spinner = ""
	pb.RunningTool = ""
	pb.textBuf.Reset()
	pb.reasoning.Reset()
	// Do NOT clear segments — they are completed output, like stdout in main agent
	// Do NOT clear mdRenderer/contentWidth — reused across iterations
}

func (pb *PanelBuffer) Finalize()                                       {}
func (pb *PanelBuffer) SelectOption(title string, options []string) int { return 0 }
func (pb *PanelBuffer) ReadLine() (string, bool)                        { return "", false }

// Snapshot returns: completed segments, current text, current reasoning, spinner, runningTool, done
func (pb *PanelBuffer) Snapshot() (segments []PanelSegment, text, reasoning, spinner, runningTool string, done bool) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	segCopy := make([]PanelSegment, len(pb.segments))
	copy(segCopy, pb.segments)
	return segCopy, pb.textBuf.String(), pb.reasoning.Text(), pb.Spinner, pb.RunningTool, pb.Done
}

func (pb *PanelBuffer) SetDone(result string) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.reasoning.Active() {
		pb.closeReasoningLocked()
	}
	if pb.textBuf.Len() > 0 {
		pb.closeTextLocked()
	}
	pb.Done = true
	pb.FinalResult = result
	pb.Spinner = ""
	pb.RunningTool = ""
}

func (pb *PanelBuffer) StartSubagent(id, label string)          {}
func (pb *PanelBuffer) UpdateSubagent(id, status string)        {}
func (pb *PanelBuffer) FinishSubagent(id, label, result string) {}
func (pb *PanelBuffer) StartSubagentBatch(count int)            {}
func (pb *PanelBuffer) FinishSubagentBatch()                    {}
func (pb *PanelBuffer) StartSubagentPanel(v interface{})        {}
