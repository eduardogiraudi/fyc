package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/modes"
	"fuckyourcli/types"
)

type retrySink struct {
	recordSink
	tools int
}

func (s *retrySink) WriteToolOutput(string, string) { s.tools++ }

func TestRetrySlashProviderErrors(t *testing.T) {
	for _, failure := range []string{"rate limit HTTP 400", "partial tool stream"} {
		t.Run(failure, func(t *testing.T) {
			ws := testWorkspace(t)
			t.Setenv("HOME", t.TempDir())
			var requests []json.RawMessage
			var models []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages json.RawMessage `json:"messages"`
					Model    string          `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests = append(requests, body.Messages)
				models = append(models, body.Model)
				if len(requests) == 1 {
					if failure == "rate limit HTTP 400" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","message":"rate limited"}}`)
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\",\"tool_calls\":[{\"index\":0,\"id\":\"partial_call\",\"type\":\"function\",\"function\":{\"name\":\"ls\",\"arguments\":\"{\"}}]}}]}\n\n")
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			cfg := temperatureConfig(config.ProviderOpenAICompatible, "test", "default")
			cfg.OpenAICompatibleProviders[0].BaseURL = server.URL
			load := temperatureConfigLoader(t)
			load(cfg)
			sink := &retrySink{}
			s := &ChatSession{Sink: sink, ChatHistory: []types.Message{{Role: "system", Content: "sys"}}}
			if !s.ProcessInput("hello") || s.retryRequest == nil || len(s.ChatHistory) != 2 {
				t.Fatalf("failed request not retryable: %+v, %s", s.ChatHistory, sink.text())
			}
			if !strings.Contains(sink.text(), "/retry") || len(requests) != 1 || sink.tools != 0 {
				t.Fatal("failure retried automatically or executed a partial tool")
			}
			cfg.Model = "retry-model"
			cfg.OpenAICompatibleProviders[0].Model = cfg.Model
			load(cfg)
			if !s.ProcessInput("/retry") || len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
				t.Fatalf("retry did not resend the same context: %s", sink.text())
			}
			if !reflect.DeepEqual(models, []string{"test", "retry-model"}) {
				t.Fatalf("retry used stale model settings: %v", models)
			}
			if s.retryRequest != nil || len(s.ChatHistory) != 3 || s.ChatHistory[2].Content != "done" || sink.tools != 0 {
				t.Fatalf("retry polluted history: %+v", s.ChatHistory)
			}
			saved, err := readSessionFile(ws, s.SessionID)
			if err != nil || !reflect.DeepEqual(saved.Messages, s.ChatHistory) {
				t.Fatalf("retry was not autosaved: %v", err)
			}
			s.ProcessInput("/retry")
			if len(requests) != 2 || !strings.Contains(sink.text(), "No failed provider request") {
				t.Fatal("successful response remained retryable")
			}
		})
	}
}

func TestRetryKeepsCompletedToolsAndMode(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	sink := &retrySink{}
	s := &ChatSession{Sink: sink, RestrictedRoot: t.TempDir(), CurrentMode: &modes.PlanMode,
		turnModeOverride: &modes.DebugMode, ChatHistory: []types.Message{{Role: "user", Content: "task"}},
		AvailableTools: []types.Tool{{Function: types.ToolFunction{Name: "ls"}}, {Function: types.ToolFunction{Name: "write"}}}}
	requests := 0
	var failedHistory []types.Message
	var failedTools []types.Tool
	var failedCtx context.Context
	p := compactionProviderFunc(func(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
		requests++
		if requests == 1 {
			return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall("completed")}}, nil
		}
		if requests == 2 {
			failedHistory = append([]types.Message(nil), messages...)
			failedTools = append([]types.Tool(nil), tools...)
			failedCtx = ctx
			// Even a provider returning a partial result with an error must not
			// commit that result or execute its calls.
			sink.WriteText("partial")
			return &types.StreamResult{Content: "partial", ToolCalls: []types.ToolCall{compactionCall("partial")}}, fmt.Errorf("provider idle: %w", context.DeadlineExceeded)
		}
		if !reflect.DeepEqual(messages, failedHistory) || !reflect.DeepEqual(tools, failedTools) || ctx == failedCtx || ctx.Err() != nil {
			t.Fatal("retry lost context, mode restrictions or fresh cancellation context")
		}
		if s.activeMode() != &modes.DebugMode {
			t.Fatal("one-shot mode not restored")
		}
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	})
	streamProviderLoop(s, streamOptions{includeToolCallID: true}, cfg, p, nil)
	if s.retryRequest == nil || sink.tools != 1 || s.ChatHistory[len(s.ChatHistory)-1].ToolCallID != "completed" {
		t.Fatal("missing completed tool checkpoint")
	}
	s.turnModeOverride = nil
	if !retryFailedRequestWith(s, func(s *ChatSession) {
		streamProviderLoop(s, streamOptions{includeToolCallID: true, retryFirstRequest: true}, cfg, p, nil)
	}) {
		t.Fatal("retry rejected")
	}
	if requests != 3 || sink.tools != 1 || s.retryRequest != nil || s.CurrentMode != &modes.PlanMode || s.turnModeOverride != nil {
		t.Fatal("retry replayed tools or leaked the one-shot mode")
	}
}

func TestRetrySkipsInitialCompactionAndCanFailAgain(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	s := &ChatSession{ChatHistory: turnCompactionHistory()}
	p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return nil, fmt.Errorf("API error (400): rate limited")
	})
	streamProviderLoop(s, streamOptions{}, cfg, p, nil)
	before := append([]types.Message(nil), s.ChatHistory...)
	cfg.CompactionEnabled, cfg.CompactionMidTurn = true, true
	temperatureConfigLoader(t)(cfg)
	if !retryFailedRequestWith(s, func(s *ChatSession) {
		streamProviderLoop(s, streamOptions{retryFirstRequest: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (compactionResult, error) {
			t.Fatal("retry compacted its checkpoint")
			return compactionResult{}, nil
		})
	}) || s.retryRequest == nil || !reflect.DeepEqual(before, s.ChatHistory) {
		t.Fatal("failed retry lost checkpoint or changed history")
	}
}

func TestRetryRejectsChangedConversation(t *testing.T) {
	for _, change := range []string{"prompt", "tool result", "reasoning", "compact", "clear", "new", "switch", "resume", "system settings", "arguments"} {
		t.Run(change, func(t *testing.T) {
			ws := testWorkspace(t)
			s := &ChatSession{Sink: &recordSink{}, ChatHistory: turnCompactionHistory()}
			s.retryRequest = newRetryRequest(s)
			switch change {
			case "prompt":
				s.ChatHistory = append(s.ChatHistory, types.Message{Role: "user", Content: "new task"})
			case "tool result":
				s.ChatHistory[len(s.ChatHistory)-1].Content = "different"
			case "reasoning":
				s.ChatHistory[4].ReasoningItem = "different"
			case "compact":
				s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "summary"}}
			case "clear":
				handleSlashCommand("/clear", s)
			case "new":
				if err := startNewChat(s); err != nil {
					t.Fatal(err)
				}
			case "switch", "resume":
				other := &ChatSession{ChatHistory: turnCompactionHistory()}
				if err := saveChatSession(other); err != nil {
					t.Fatal(err)
				}
				var err error
				if change == "switch" {
					err = switchChatSession(s, other.SessionID)
				} else {
					err = resumeSession(s, ws, other.SessionID)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "system settings":
				s.ChatHistory[0].Content = "new system prompt"
			case "arguments":
				s.ChatHistory[4].ToolCalls[0].Function.Arguments["path"] = "other"
			}
			called := false
			ok := retryFailedRequestWith(s, func(*ChatSession) { called = true })
			want := change == "system settings"
			if ok != want || called != want || (!want && s.retryRequest != nil) {
				t.Fatal("stale checkpoint accepted or settings change rejected")
			}
		})
	}
}

func TestRetryUnavailableOnCancellation(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	for _, interruptedResult := range []bool{false, true} {
		s := &ChatSession{ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
		p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			s.GenerationCancelMu.Lock()
			s.GenerationCancel()
			s.GenerationCancelMu.Unlock()
			if interruptedResult {
				return &types.StreamResult{Content: "partial", Interrupted: true}, nil
			}
			return nil, context.Canceled
		})
		streamProviderLoop(s, streamOptions{}, cfg, p, nil)
		if s.retryRequest != nil || s.IsGenerating.Load() != 0 {
			t.Fatal("user cancellation became retryable")
		}
	}
}

func TestRetrySlashArgumentsAreNotPrompts(t *testing.T) {
	s := &ChatSession{Sink: &recordSink{}, ChatHistory: []types.Message{{Role: "user", Content: "task"}}}
	s.retryRequest = newRetryRequest(s)
	before := s.retryRequest
	handled, input := handleSlashCommand("/retry extra", s)
	if !handled || input != "" || s.retryRequest != before || len(s.ChatHistory) != 1 {
		t.Fatal("invalid retry changed context")
	}
}

func TestRetryGeminiModeSwitch(t *testing.T) {
	for _, target := range []config.ProviderType{config.ProviderOpenAICompatible, config.ProviderOpenAIResponsesCompatible, config.ProviderAnthropic, config.ProviderCodex} {
		for _, mode := range []*modes.Mode{&modes.TutorialMode, &modes.DebugMode} {
			for _, oneShot := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/one-shot=%t", target, mode.Name, oneShot), func(t *testing.T) {
					cfg := compactionTestConfig(t, false, false, 1000)
					cfg.Provider = config.ProviderGemini
					s := &ChatSession{Sink: &recordSink{}, CurrentMode: mode,
						ChatHistory:    []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}},
						AvailableTools: []types.Tool{{Function: types.ToolFunction{Name: "ls"}}, {Function: types.ToolFunction{Name: "write"}}}}
					if oneShot {
						s.CurrentMode, s.turnModeOverride = &modes.PlanMode, mode
					}
					streamProviderLoop(s, streamOptions{}, cfg, compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
						return nil, fmt.Errorf("Gemini failed")
					}), nil)
					if s.retryRequest == nil {
						t.Fatal("missing Gemini checkpoint")
					}
					s.turnModeOverride = nil
					current := s.CurrentMode
					before := append([]types.Message(nil), s.ChatHistory...)
					cfg.Provider = target
					requests := 0
					var first []types.Message
					p := compactionProviderFunc(func(_ context.Context, messages []types.Message, tools []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
						requests++
						instruction := "[INSTRUCTIONS]: " + mode.GetInstructions(s.subagentsEnabled())
						count := 0
						for _, m := range messages {
							if m.Role == "system" && strings.Contains(m.Content, instruction) {
								count++
							}
						}
						if count != 1 || len(messages) != len(before) || !strings.Contains(messages[0].Content, "sys") {
							t.Fatal("retry lost or duplicated Gemini mode instructions")
						}
						for _, tool := range tools {
							if !mode.ExposesTool(tool.Function.Name) {
								t.Fatal("retry lost mode restrictions")
							}
						}
						if requests == 1 {
							first = append([]types.Message(nil), messages...)
							return nil, fmt.Errorf("retry failed")
						}
						if !reflect.DeepEqual(first, messages) {
							t.Fatal("second retry changed the sampling boundary")
						}
						return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
					})
					stream := func(s *ChatSession) { streamProviderLoop(s, streamOptions{retryFirstRequest: true}, cfg, p, nil) }
					if !retryFailedRequestWith(s, stream) || s.retryRequest == nil || !reflect.DeepEqual(before, s.ChatHistory) {
						t.Fatal("failed retry mutated the conversation or lost its checkpoint")
					}
					if !retryFailedRequestWith(s, stream) || s.retryRequest != nil || requests != 2 {
						t.Fatal("second retry did not complete")
					}
					if s.CurrentMode != current || s.turnModeOverride != nil || !reflect.DeepEqual(before, s.ChatHistory[:len(before)]) {
						t.Fatal("retry leaked temporary mode instructions or state")
					}
				})
			}
		}
	}
}

func TestRetryGeminiCompletedToolsSwitch(t *testing.T) {
	for _, target := range []config.ProviderType{config.ProviderOpenAICompatible, config.ProviderOpenAIResponsesCompatible, config.ProviderAnthropic} {
		t.Run(string(target), func(t *testing.T) {
			testWorkspace(t)
			t.Setenv("HOME", t.TempDir())
			load := temperatureConfigLoader(t)
			cfg := temperatureConfig(config.ProviderGemini, "gemini-test", "default")
			load(cfg)
			sink := &retrySink{}
			s := &ChatSession{Sink: sink, RestrictedRoot: t.TempDir(), turnModeOverride: &modes.DebugMode,
				ChatHistory:    []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "list files"}},
				AvailableTools: []types.Tool{{Function: types.ToolFunction{Name: "ls"}}, {Function: types.ToolFunction{Name: "write"}}}}
			requests := 0
			p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
				requests++
				if requests == 1 {
					return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall(""), compactionCall("")}}, nil
				}
				return nil, fmt.Errorf("Gemini failed")
			})
			streamProviderLoop(s, streamOptions{includeReasoningSignature: true, includeToolCallID: true}, cfg, p, nil)
			if s.retryRequest == nil || sink.tools != 2 || len(s.ChatHistory) != 5 {
				t.Fatal("missing completed Gemini tool batch")
			}
			ids := []string{s.ChatHistory[2].ToolCalls[0].ID, s.ChatHistory[2].ToolCalls[1].ID}
			if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] || s.ChatHistory[3].ToolCallID != ids[0] || s.ChatHistory[4].ToolCallID != ids[1] {
				t.Fatal("Gemini calls and results have no stable matching IDs")
			}
			before := append([]types.Message(nil), s.ChatHistory...)
			s.turnModeOverride = nil
			var sent []json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if !strings.Contains(string(body["messages"])+string(body["instructions"])+string(body["system"]), "DEBUG MODE") {
					t.Error("wire request lost Gemini mode instructions")
				}
				if strings.Contains(string(body["tools"]), `"name":"write"`) {
					t.Error("wire request lost one-shot mode restrictions")
				}
				input := body["messages"]
				if target == config.ProviderOpenAIResponsesCompatible {
					input = body["input"]
				}
				sent = append(sent, input)
				for _, id := range ids {
					if strings.Count(string(input), `"`+id+`"`) != 2 {
						t.Errorf("call and result must share ID %q: %s", id, input)
					}
				}
				if len(sent) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch target {
				case config.ProviderOpenAICompatible:
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				case config.ProviderOpenAIResponsesCompatible:
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
				case config.ProviderAnthropic:
					fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}
			}))
			defer server.Close()
			cfg = temperatureConfig(target, "test", "default")
			switch target {
			case config.ProviderOpenAICompatible:
				cfg.OpenAICompatibleProviders[0].BaseURL = server.URL
			case config.ProviderOpenAIResponsesCompatible:
				cfg.OpenAIResponsesCompatibleProviders[0].BaseURL = server.URL
			case config.ProviderAnthropic:
				cfg.AnthropicProviders[0].BaseURL = server.URL
			}
			load(cfg)
			s.ProcessInput("/retry")
			if s.retryRequest == nil || !reflect.DeepEqual(before, s.ChatHistory) || sink.tools != 2 {
				t.Fatal("failed provider switch retry mutated history or replayed tools")
			}
			s.ProcessInput("/retry")
			if len(sent) != 2 || !reflect.DeepEqual(sent[0], sent[1]) || s.retryRequest != nil || sink.tools != 2 || s.ChatHistory[len(s.ChatHistory)-1].Content != "done" {
				t.Fatal("provider switch retry lost IDs or replayed completed tools")
			}
		})
	}
}

func TestRetryGeminiModeSurvivesFollowUpTools(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 1000)
	cfg.Provider = config.ProviderGemini
	sink := &retrySink{}
	s := &ChatSession{Sink: sink, RestrictedRoot: t.TempDir(), turnModeOverride: &modes.DebugMode,
		ChatHistory: []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}}
	streamProviderLoop(s, streamOptions{}, cfg, compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return nil, fmt.Errorf("Gemini failed")
	}), nil)
	s.turnModeOverride = nil
	before := append([]types.Message(nil), s.ChatHistory...)
	cfg.Provider = config.ProviderOpenAICompatible
	requests := 0
	var failed []types.Message
	p := compactionProviderFunc(func(_ context.Context, messages []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
		requests++
		if strings.Count(messages[0].Content, "[INSTRUCTIONS]: ") != 1 || !strings.Contains(messages[0].Content, "DEBUG MODE") {
			t.Fatal("follow-up lost or duplicated transport-only mode instructions")
		}
		if requests == 1 {
			return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall("")}}, nil
		}
		if requests == 2 {
			failed = append([]types.Message(nil), messages...)
			return nil, fmt.Errorf("follow-up failed")
		}
		if !reflect.DeepEqual(failed, messages) {
			t.Fatal("retry after a new tool moved the sampling boundary")
		}
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	})
	stream := func(s *ChatSession) { streamProviderLoop(s, streamOptions{retryFirstRequest: true}, cfg, p, nil) }
	if !retryFailedRequestWith(s, stream) || s.retryRequest == nil || sink.tools != 1 || !reflect.DeepEqual(before, s.ChatHistory[:len(before)]) {
		t.Fatal("follow-up failed without a safe retry checkpoint")
	}
	if !retryFailedRequestWith(s, stream) || s.retryRequest != nil || sink.tools != 1 || requests != 3 || s.CurrentMode != nil || s.turnModeOverride != nil {
		t.Fatal("follow-up retry replayed tools or leaked the one-shot mode")
	}
}

func TestRetryModeCompaction(t *testing.T) {
	for _, source := range []config.ProviderType{config.ProviderGemini, config.ProviderOpenAICompatible} {
		for _, target := range []config.ProviderType{config.ProviderOpenAICompatible, config.ProviderOpenAIResponsesCompatible, config.ProviderAnthropic} {
			for _, oneShot := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/one-shot=%t", source, target, oneShot), func(t *testing.T) {
					cfg := compactionTestConfig(t, false, false, 1000)
					cfg.Provider = source
					sink := &retrySink{}
					s := &ChatSession{Sink: sink, CurrentMode: &modes.DebugMode,
						RestrictedRoot: t.TempDir(), ChatHistory: turnCompactionHistory()}
					if oneShot {
						s.CurrentMode, s.turnModeOverride = &modes.PlanMode, &modes.DebugMode
					}
					streamProviderLoop(s, streamOptions{}, cfg, compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
						return nil, fmt.Errorf("initial failure")
					}), nil)
					if s.retryRequest == nil {
						t.Fatal("missing checkpoint")
					}
					s.turnModeOverride = nil
					current := s.CurrentMode
					cfg.Provider, cfg.CompactionEnabled, cfg.CompactionMidTurn = target, true, true
					temperatureConfigLoader(t)(cfg)
					requests, summaries := 0, 0
					var failed []types.Message
					instruction := "[INSTRUCTIONS]: " + modes.DebugMode.GetInstructions(s.subagentsEnabled())
					p := compactionProviderFunc(func(_ context.Context, messages []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
						requests++
						count := 0
						for _, message := range messages {
							count += strings.Count(message.Content, instruction)
						}
						if count != 1 {
							t.Errorf("request %d has %d copies of mode instructions", requests, count)
						}
						if s.activeMode() != &modes.DebugMode {
							t.Fatal("retry lost active mode")
						}
						if _, err := compactionGroups(messages); err != nil {
							t.Fatal(err)
						}
						switch requests {
						case 1:
							return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall("new")}}, nil
						case 2:
							failed = append([]types.Message(nil), messages...)
							return nil, fmt.Errorf("failure after compaction")
						default:
							if !reflect.DeepEqual(failed, messages) {
								t.Fatal("second retry moved the compacted sampling boundary")
							}
							return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
						}
					})
					stream := func(s *ChatSession) {
						streamProviderLoop(s, streamOptions{retryFirstRequest: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (compactionResult, error) {
							summaries++
							return compactionResult{Summary: "old work"}, nil
						})
					}
					if !retryFailedRequestWith(s, stream) || s.retryRequest == nil || summaries != 1 || sink.tools != 1 {
						t.Fatalf("retry did not compact after the new tool: summaries=%d tools=%d", summaries, sink.tools)
					}
					for attempt := 0; attempt < 2; attempt++ {
						if s.CurrentMode != current || s.turnModeOverride != nil {
							t.Fatal("retry leaked temporary mode state")
						}
						wantFlag := ""
						if !oneShot {
							wantFlag = modes.DebugMode.Name
						}
						if s.persistentModeInjected != wantFlag {
							t.Errorf("persistent mode marker = %q, want %q", s.persistentModeInjected, wantFlag)
						}
						if attempt == 0 && (!retryFailedRequestWith(s, stream) || s.retryRequest != nil) {
							t.Fatal("second retry failed")
						}
					}
					if requests != 3 || summaries != 1 || sink.tools != 1 {
						t.Fatal("second retry repeated compaction or tools")
					}
					if !oneShot && s.claimModeInstructions(current, false) {
						t.Fatal("next prompt would duplicate persistent mode instructions")
					}
				})
			}
		}
	}
}

func TestRetryCompactionRestoresChangedMode(t *testing.T) {
	for _, current := range []*modes.Mode{nil, &modes.PlanMode} {
		name := "chat"
		if current != nil {
			name = current.Name
		}
		t.Run(name, func(t *testing.T) {
			cfg := compactionTestConfig(t, false, false, 1000)
			s := &ChatSession{CurrentMode: &modes.DebugMode, RestrictedRoot: t.TempDir(), ChatHistory: turnCompactionHistory()}
			streamProviderLoop(s, streamOptions{}, cfg, compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
				return nil, fmt.Errorf("initial failure")
			}), nil)
			s.CurrentMode, s.persistentModeInjected = current, ""
			cfg.CompactionEnabled, cfg.CompactionMidTurn = true, true
			temperatureConfigLoader(t)(cfg)
			requests, summaries := 0, 0
			p := compactionProviderFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
				requests++
				if requests == 1 {
					return &types.StreamResult{ToolCalls: []types.ToolCall{compactionCall("new")}}, nil
				}
				return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
			})
			if !retryFailedRequestWith(s, func(s *ChatSession) {
				streamProviderLoop(s, streamOptions{retryFirstRequest: true}, cfg, p, func(context.Context, []types.Message, *ChatSession) (compactionResult, error) {
					summaries++
					return compactionResult{Summary: "old work"}, nil
				})
			}) || summaries != 1 {
				t.Fatal("retry did not compact")
			}
			if s.CurrentMode != current || s.turnModeOverride != nil || s.persistentModeInjected != "" {
				t.Fatal("retry leaked the failed turn's persistent mode marker")
			}
		})
	}
}
