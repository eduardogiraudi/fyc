package main

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"fuckyourcli/providers"
	"fuckyourcli/types"
	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// frameRetrySink exercises the real frame synchronously through the chat loop.
type frameRetrySink struct {
	resendSink
	model     chatModel
	rollbacks int
}

func newFrameRetrySink() *frameRetrySink {
	s := &frameRetrySink{model: newChatModel(nil, nil, nil, nil)}
	s.model.enableAltScreen([]string{"banner"})
	return s
}

func (s *frameRetrySink) update(msg tea.Msg) {
	model, _ := s.model.Update(msg)
	s.model = model.(chatModel)
}
func (s *frameRetrySink) BeginStreamAttempt(context.Context) bool {
	reply := make(chan bool, 1)
	s.update(ui.StreamAttemptBeginMsg{Reply: reply})
	return <-reply
}
func (s *frameRetrySink) RollbackStreamAttempt(context.Context) bool {
	reply := make(chan bool, 1)
	s.update(ui.StreamAttemptRollbackMsg{Reply: reply})
	s.rollbacks++
	return <-reply
}
func (s *frameRetrySink) EndStreamAttempt()          { s.update(ui.StreamAttemptEndMsg{}) }
func (s *frameRetrySink) WriteText(text string)      { s.update(ui.StreamTextMsg{Token: text}) }
func (s *frameRetrySink) WriteReasoning(text string) { s.update(ui.StreamReasoningMsg{Delta: text}) }
func (s *frameRetrySink) WriteDiffCraft(add, del int) {
	s.update(ui.StreamDiffCraftMsg{Add: add, Del: del})
}
func (s *frameRetrySink) WriteToolOutput(label, result string) {
	s.tools++
	s.update(ui.StreamToolOutputMsg{Label: label, Result: result})
}
func (s *frameRetrySink) WriteNewline() { s.update(ui.StreamNewlineMsg{}) }
func (s *frameRetrySink) Reset()        { s.update(ui.StreamResetMsg{}) }
func (s *frameRetrySink) Finalize()     { s.update(ui.StreamFinalizeMsg{}) }
func (s *frameRetrySink) text() string {
	return strings.Join(s.model.transcript.lines, "\n") + "\n" + s.model.textBuf
}

func TestAltScreenResendPreservesCompletedTools(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	sink := newFrameRetrySink()
	s := &ChatSession{Sink: sink, RestrictedRoot: t.TempDir(), ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	requests := 0
	var failedHistory []types.Message
	p := compactionProviderFunc(func(_ context.Context, messages []types.Message, _ []types.Tool, out types.OutputSink) (*types.StreamResult, error) {
		requests++
		switch requests {
		case 1:
			return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall("completed")}}, nil
		case 2:
			failedHistory = append([]types.Message(nil), messages...)
			out.WriteReasoning("failed reasoning")
			out.WriteText("failed paragraph\n\npending text")
			out.WriteDiffCraft(3, 2)
			return &types.StreamResult{Content: "failed paragraph", ToolCalls: []types.ToolCall{compactionCall("partial")}}, idleStreamFailure()
		default:
			if !reflect.DeepEqual(messages, failedHistory) {
				t.Fatal("resend changed the sampling boundary")
			}
			out.WriteText("recovered")
			return &types.StreamResult{Content: "recovered", FinishReason: "stop"}, nil
		}
	})
	streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, nil)
	sink.Finalize()
	if requests != 3 || sink.tools != 1 || sink.rollbacks != 1 {
		t.Fatalf("requests=%d tools=%d rollbacks=%d", requests, sink.tools, sink.rollbacks)
	}
	if len(s.ChatHistory) != 4 || s.ChatHistory[2].ToolCallID != "completed" || s.ChatHistory[3].Content != "recovered" {
		t.Fatalf("failed request polluted history: %+v", s.ChatHistory)
	}
	if text := sink.text(); strings.Contains(text, "failed paragraph") || strings.Contains(text, "pending text") || strings.Contains(text, "/retry") || !strings.Contains(text, "recovered") {
		t.Fatalf("rollback left failed output: %s", text)
	}
	if s.retryRequest != nil || sink.model.streamAttempt != nil || sink.model.diffActive || sink.model.reasoning.Active() {
		t.Fatal("recovered request left transient state")
	}
}

func TestAltScreenResendExhaustionKeepsLastAttempt(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	sink := newFrameRetrySink()
	s := &ChatSession{Sink: sink, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	requests := 0
	p := compactionProviderFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, out types.OutputSink) (*types.StreamResult, error) {
		requests++
		out.WriteText(fmt.Sprintf("attempt-%d\n\n", requests))
		return nil, idleStreamFailure()
	})
	streamProviderLoop(s, streamOptions{}, cfg, p, nil)
	sink.Finalize()
	if requests != providers.MaxStreamResends+1 || sink.rollbacks != providers.MaxStreamResends || s.retryRequest == nil || len(s.ChatHistory) != 1 {
		t.Fatal("retry exhaustion lost the manual checkpoint or changed history")
	}
	if text := sink.text(); strings.Contains(text, "attempt-1") || !strings.Contains(text, fmt.Sprintf("attempt-%d", requests)) || !strings.Contains(text, "/retry") {
		t.Fatalf("wrong output after exhaustion: %s", text)
	}
}

func TestAltScreenResendDoesNotHidePermanentErrorsOrCancellation(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelTurn), func(t *testing.T) {
			cfg := compactionTestConfig(t, false, false, 1000)
			sink := newFrameRetrySink()
			s := &ChatSession{Sink: sink, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
			requests := 0
			p := compactionProviderFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, out types.OutputSink) (*types.StreamResult, error) {
				requests++
				out.WriteText("partial")
				if cancelTurn {
					s.GenerationCancelMu.Lock()
					s.GenerationCancel()
					s.GenerationCancelMu.Unlock()
					return nil, idleStreamFailure()
				}
				return nil, fmt.Errorf("API error (400): invalid request")
			})
			streamProviderLoop(s, streamOptions{}, cfg, p, nil)
			if requests != 1 || sink.rollbacks != 0 || !strings.Contains(sink.text(), "partial") {
				t.Fatal("unsafe retry or hidden output")
			}
			if cancelTurn && s.retryRequest != nil {
				t.Fatal("cancelled turn became retryable")
			}
		})
	}
}

func TestAltScreenRollbackRestoresTrimmedTranscript(t *testing.T) {
	sink := newFrameRetrySink()
	for i := 0; i < transcriptMaxLines; i++ {
		sink.model.transcript.Append(fmt.Sprintf("original-%d", i))
	}
	sink.model.transcript.View(10)
	sink.model.transcript.ScrollUp(5)
	before := append([]string(nil), sink.model.transcript.lines...)
	if !sink.BeginStreamAttempt(context.Background()) {
		t.Fatal("alt screen refused rollback")
	}
	for attempt := 0; attempt < 2; attempt++ {
		sink.WriteReasoning("thinking")
		sink.WriteText("partial\n\npending")
		sink.WriteDiffCraft(2, 1)
		if !sink.RollbackStreamAttempt(context.Background()) || !reflect.DeepEqual(sink.model.transcript.lines, before) || sink.model.transcript.offset != 5 {
			t.Fatal("rollback lost capped transcript or scroll position")
		}
		if sink.model.textBuf != "" || sink.model.reasoning.Active() || sink.model.diffActive {
			t.Fatal("rollback left live output")
		}
	}
	sink.EndStreamAttempt()
	if !sink.BeginStreamAttempt(context.Background()) {
		t.Fatal("snapshot was not released")
	}
	sink.EndStreamAttempt()
}

type retryRuntimeModel struct{ chatModel }

func (m retryRuntimeModel) Init() tea.Cmd { return nil }

func TestTerminalRollbackUsesRunningFrameNotConfig(t *testing.T) {
	for _, alt := range []bool{false, true} {
		t.Run(fmt.Sprint(alt), func(t *testing.T) {
			sink := ui.NewTerminalOutputSink(nil, nil)
			m := newChatModel(nil, sink, nil, nil)
			if alt {
				m.enableAltScreen([]string{"banner"})
			}
			p := tea.NewProgram(retryRuntimeModel{m}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignals())
			sink.AttachProgram(p)
			defer ui.SetPromptProgram(nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan bool, 1)
			go func() {
				supported := sink.BeginStreamAttempt(ctx)
				if supported {
					sink.WriteText("partial\n\n")
					supported = sink.RollbackStreamAttempt(ctx)
					sink.EndStreamAttempt()
				}
				done <- supported
				p.Quit()
			}()
			final, err := p.Run()
			if err != nil {
				t.Fatal(err)
			}
			if got := <-done; got != alt {
				t.Fatalf("rollback support=%v, alt=%v", got, alt)
			}
			if alt {
				frame := final.(chatModel)
				if strings.Join(frame.transcript.lines, "\n") != "banner" || frame.streamAttempt != nil {
					t.Fatal("terminal sink failed to retract output")
				}
			}
		})
	}
}
