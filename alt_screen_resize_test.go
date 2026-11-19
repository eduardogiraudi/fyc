package main

import (
	"fmt"
	"strings"
	"testing"

	"fuckyourcli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestAltScreenHeaderRepaintsOnResize(t *testing.T) {
	for _, compact := range []bool{true, false} {
		t.Run(fmt.Sprint(compact), func(t *testing.T) {
			info := []string{"cwd: /a/long/path/that/must/reappear/after/enlarging", "provider: example • model: example"}
			header := func(width int) string { return ui.StartupHeaderBlockAtWidth(compact, info, width) }
			m := newChatModel(nil, nil, nil, nil)
			m.width, m.height = 28, 50
			m.enableAltScreen([]string{header(28), "tips"}, header)
			for _, width := range []int{100, 24, 120} {
				next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 50})
				m = next.(chatModel)
				if got, want := strings.Join(m.transcript.lines, "\n"), header(width)+"\ntips"; got != want {
					t.Fatalf("width %d: header was not regenerated\ngot: %s\nwant: %s", width, got, want)
				}
				rows := strings.Split(m.View(), "\n")
				if len(rows) != 50 {
					t.Fatalf("frame has %d rows", len(rows))
				}
				for _, row := range rows {
					if ansi.StringWidth(row) > width {
						t.Fatalf("row exceeds width %d: %q", width, row)
					}
				}
			}
		})
	}
}

func TestAltScreenLongPromptIsCompleteAndReflows(t *testing.T) {
	input := strings.Repeat("abcdefgh界e\u0301", 25)
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprint(replay), func(t *testing.T) {
			m := newChatModel(nil, nil, nil, nil)
			m.width, m.height = 20, 14
			m.enableAltScreen(nil)
			var next tea.Model
			if replay {
				next, _ = m.Update(ui.StreamPrintMsg{Text: ui.RenderSubmittedPrompt(input)})
			} else {
				// Do not execute the command that starts the provider request.
				next, _ = m.Update(ui.InputSubmitMsg{Value: input})
			}
			m = next.(chatModel)
			for _, width := range []int{20, 70, 12, 100} {
				next, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 14})
				m = next.(chatModel)
				promptRows := m.transcript.blocks[0].rows
				got := ansi.Strip(strings.Join(promptRows, ""))
				if got != "❯ "+input {
					t.Fatalf("width %d: prompt lost content: %q", width, got)
				}
				for _, row := range promptRows {
					if ansi.StringWidth(row) > width {
						t.Fatalf("prompt exceeds width %d: %q", width, row)
					}
				}
				if len(strings.Split(m.View(), "\n")) != 14 {
					t.Fatal("frame height changed")
				}
			}
		})
	}
}

func TestTranscriptResizeKeepsScrollAnchorAndClearsSelection(t *testing.T) {
	tr := &transcriptState{}
	tr.AppendRendered(func(width int) string { return ui.WrapBlock(strings.Repeat("x", 120), width) }, 40)
	for i := 0; i < 30; i++ {
		tr.Append(fmt.Sprintf("row-%02d", i))
	}
	tr.View(5)
	tr.ScrollUp(10)
	before := tr.View(5)
	tr.SelectionStart(0, 0)
	tr.Resize(10)
	if got := tr.View(5); got != before {
		t.Fatalf("resize moved the reader: %q -> %q", before, got)
	}
	if tr.selActive {
		t.Fatal("resize retained stale selection coordinates")
	}
}

func TestTranscriptResizeNeverResurrectsEvictedSource(t *testing.T) {
	tr := &transcriptState{}
	tr.AppendRendered(func(width int) string {
		return ui.WrapBlock("evicted-head\n"+strings.Repeat("tail\n", transcriptMaxLines), width)
	}, 40)
	if strings.Contains(strings.Join(tr.lines, "\n"), "evicted-head") {
		t.Fatal("head was not evicted")
	}
	for _, width := range []int{10, 80} {
		tr.Resize(width)
		if len(tr.lines) != transcriptMaxLines {
			t.Fatalf("cap changed: %d", len(tr.lines))
		}
		if strings.Contains(strings.Join(tr.lines, "\n"), "evicted-head") {
			t.Fatal("resize resurrected evicted source")
		}
	}
	tr.Append("last")
	if len(tr.lines) != transcriptMaxLines {
		t.Fatal("append exceeded cap")
	}
}

func TestAltScreenResizeAlsoReflowsRetrySnapshot(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width = 20
	m.enableAltScreen(nil)
	input := strings.Repeat("prompt", 30)
	m.printCmd(ui.RenderSubmittedPrompt(input))
	reply := make(chan bool, 1)
	next, _ := m.Update(ui.StreamAttemptBeginMsg{Reply: reply})
	m = next.(chatModel)
	if !<-reply {
		t.Fatal("snapshot not supported")
	}
	m.printCmd("failed attempt")
	next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatModel)
	next, _ = m.Update(ui.StreamAttemptRollbackMsg{Reply: reply})
	m = next.(chatModel)
	if !<-reply {
		t.Fatal("rollback failed")
	}
	want := ui.WrapBlock(ui.RenderSubmittedPrompt(input), 80)
	if got := strings.Join(m.transcript.lines, "\n"); got != want {
		t.Fatalf("rollback restored old-width output: %q", got)
	}
	m.transcript.Resize(10)
	if m.streamAttempt.width != 80 {
		t.Fatal("resize mutated the saved snapshot")
	}
}

func TestSessionStartupSeedKeepsLongPrompt(t *testing.T) {
	var seed []string
	ui.SetPreProgramBlockSink(func(block ui.SeedBlock) { seed = append(seed, block.Text) })
	defer ui.SetPreProgramBlockSink(nil)
	input := strings.Repeat("input", 100)
	writeSessionBlock(&ChatSession{}, ui.RenderSubmittedPrompt(input))
	m := newChatModel(nil, nil, nil, nil)
	m.width = 30
	m.enableAltScreen(seed)
	if got := ansi.Strip(strings.Join(m.transcript.lines, "")); got != "❯ "+input {
		t.Fatalf("startup replay truncated prompt: %q", got)
	}
}
