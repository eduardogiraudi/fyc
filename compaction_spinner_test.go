package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/types"
	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
)

type compactionSpinnerSink struct {
	recordSink
	active  bool
	label   string
	events  []string
	onStart func()
}

func (s *compactionSpinnerSink) StartSpinner(label string) {
	s.active = true
	s.label = label
	s.events = append(s.events, "start")
	if s.onStart != nil {
		s.onStart()
	}
}

func (s *compactionSpinnerSink) StopSpinner() {
	s.active = false
	s.events = append(s.events, "stop")
}

func (s *compactionSpinnerSink) WriteText(text string) {
	s.events = append(s.events, "text")
	s.recordSink.WriteText(text)
}

func assertCompactionSpinnerResult(t *testing.T, sink *compactionSpinnerSink, label, outcome string) {
	t.Helper()
	if sink.active || !reflect.DeepEqual(sink.events, []string{"start", "stop", "text"}) {
		t.Fatalf("spinner must stop before the sole result line: active=%v events=%v", sink.active, sink.events)
	}
	if !strings.HasPrefix(sink.label, label) || !strings.Contains(sink.text(), outcome) {
		t.Fatalf("label=%q output=%q, want %q / %q", sink.label, sink.text(), label, outcome)
	}
	if strings.Contains(sink.text(), "compacting context") || strings.Contains(sink.text(), "Compacting context") {
		t.Fatalf("progress label leaked into scrollback: %q", sink.text())
	}
}

func TestManualCompactionSpinnerLifecycle(t *testing.T) {
	for _, outcome := range []string{"success", "empty", "error", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if outcome == "error" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, "backend failed")
					return
				}
				content := "hidden handoff summary"
				if outcome == "empty" {
					content = " "
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", content)
			}))
			defer server.Close()
			cfg := config.Config{Provider: config.ProviderOpenAICompatible, Model: "test",
				OpenAICompatibleProviders: []config.OpenAICompatibleProviderConfig{{Name: "test", Model: "test", BaseURL: server.URL}}}
			temperatureConfigLoader(t)(cfg)
			t.Setenv("FYC_CACHE_DIR", t.TempDir())
			history := turnCompactionHistory()
			sink := &compactionSpinnerSink{}
			s := &ChatSession{ChatHistory: history, Sink: sink}
			sink.onStart = func() {
				if s.IsGenerating.Load() != 1 || s.runContext().Err() != nil {
					t.Fatal("manual compaction must install a live cancellable context before the spinner")
				}
				if outcome == "cancel" {
					m := newChatModel(s, nil, nil, nil)
					m.generating = true
					updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
					if updated.(chatModel).quitting || s.runContext().Err() == nil {
						t.Fatal("Ctrl+C must cancel manual compaction without quitting")
					}
				}
			}
			if handled, modified := handleSlashCommand("/compact", s); !handled || modified != "" {
				t.Fatal("/compact not handled")
			}
			want := "✗ Error during compaction:"
			if outcome == "success" {
				want = "✓ Context compacted. History reduced from 7 to 3 messages"
				if len(s.ChatHistory) != 3 {
					t.Fatalf("retained messages = %d", len(s.ChatHistory))
				}
			} else if !reflect.DeepEqual(s.ChatHistory, history) {
				t.Fatal("failed or canceled compaction mutated history")
			}
			wantRequests := 1
			if outcome == "cancel" {
				want = "Compaction interrupted."
				wantRequests = 0
			}
			assertCompactionSpinnerResult(t, sink, "Compacting context", want)
			if requests != wantRequests || strings.Contains(sink.text(), "hidden handoff summary") {
				t.Fatalf("requests=%d output=%q", requests, sink.text())
			}
			if s.IsGenerating.Load() != 0 || s.GenerationCancel != nil || s.runContext().Err() != nil {
				t.Fatal("manual compaction leaked generation state")
			}
		})
	}
}

func TestAutoCompactionSpinnerLifecycle(t *testing.T) {
	for _, retainTurn := range []bool{false, true} {
		for _, outcome := range []string{"success", "empty", "error", "insufficient", "cancel"} {
			t.Run(fmt.Sprintf("mid-turn=%v/%s", retainTurn, outcome), func(t *testing.T) {
				compactionTestConfig(t, true, retainTurn, 1000)
				history := turnCompactionHistory()
				s := &ChatSession{ChatHistory: history}
				sink := &compactionSpinnerSink{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				attempted := maybeAutoCompactWith(ctx, s, sink, retainTurn, func(got context.Context, _ []types.Message, _ *ChatSession) (compactionResult, error) {
					if got != ctx || !sink.active || !reflect.DeepEqual(sink.events, []string{"start"}) {
						t.Fatal("spinner must remain active throughout the summary request")
					}
					switch outcome {
					case "empty":
						return compactionResult{Summary: " "}, nil
					case "error":
						return compactionResult{}, errors.New("provider failure")
					case "insufficient":
						return compactionResult{Summary: strings.Repeat("s", 4000)}, nil
					case "cancel":
						cancel()
						return compactionResult{Summary: "partial"}, nil
					default:
						return compactionResult{Summary: "hidden handoff summary"}, nil
					}
				})
				if !attempted {
					t.Fatal("auto-compaction not attempted")
				}
				want := "✓ Auto-compaction done:"
				switch outcome {
				case "empty", "error":
					want = "✗ Auto-compaction failed:"
				case "insufficient":
					want = "⚠ Auto-compaction insufficient"
				case "cancel":
					want = "Auto-compaction interrupted."
				}
				assertCompactionSpinnerResult(t, sink, "Auto-compacting context (~", want)
				if strings.Contains(sink.text(), "hidden handoff summary") {
					t.Fatal("summary must stay hidden")
				}
				wantSuppressed := outcome == "empty" || outcome == "error" || outcome == "insufficient"
				if s.autoCompactSuppressed != wantSuppressed {
					t.Fatalf("suppressed=%v want=%v", s.autoCompactSuppressed, wantSuppressed)
				}
				if (outcome == "empty" || outcome == "error" || outcome == "cancel") && !reflect.DeepEqual(s.ChatHistory, history) {
					t.Fatal("failed or canceled compaction mutated history")
				}
			})
		}
	}
}

func TestCompactionSpinnerNoopsStayQuiet(t *testing.T) {
	for _, reason := range []string{"disabled", "below threshold", "pending tools", "suppressed", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			compactionTestConfig(t, reason != "disabled", true, 1000)
			s := &ChatSession{ChatHistory: turnCompactionHistory()}
			sink := &compactionSpinnerSink{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch reason {
			case "below threshold":
				s.ChatHistory = s.ChatHistory[:1]
			case "pending tools":
				s.ChatHistory = s.ChatHistory[:5]
			case "suppressed":
				s.autoCompactSuppressed = true
			case "canceled":
				cancel()
			}
			if maybeAutoCompactWith(ctx, s, sink, true, func(context.Context, []types.Message, *ChatSession) (compactionResult, error) {
				t.Fatal("no-op must not request a summary")
				return compactionResult{}, nil
			}) || len(sink.events) != 0 {
				t.Fatalf("no-op emitted progress: %v", sink.events)
			}
		})
	}
	sink := &compactionSpinnerSink{}
	s := &ChatSession{ChatHistory: []types.Message{{Role: "system", Content: "sys"}}, Sink: sink}
	handleSlashCommand("/compact", s)
	if !reflect.DeepEqual(sink.events, []string{"text"}) || !strings.Contains(sink.text(), "Nothing to compact") {
		t.Fatalf("empty manual compaction must not start a spinner: %v %q", sink.events, sink.text())
	}
}

func TestCompactionSpinnerVisibleUntilResult(t *testing.T) {
	m := newChatModel(&ChatSession{}, nil, nil, nil)
	m.generating = true
	updated, _ := m.Update(ui.StreamSpinnerStartMsg{Label: "Compacting context"})
	m = updated.(chatModel)
	if !strings.Contains(m.View(), "Compacting context") || !m.spinnerTickScheduled {
		t.Fatal("compaction must show an animated spinner")
	}
	updated, _ = m.Update(ui.StreamSpinnerStopMsg{})
	m = updated.(chatModel)
	updated, _ = m.Update(ui.StreamTextMsg{Token: "✓ Context compacted."})
	m = updated.(chatModel)
	if m.spinnerLabel != "" || m.activeSpinnerLabel != "" || strings.Contains(m.View(), "Compacting context") || !strings.Contains(m.View(), "✓ Context compacted.") {
		t.Fatal("the result must replace the spinner")
	}
}
