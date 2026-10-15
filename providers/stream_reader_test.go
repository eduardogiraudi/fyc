package providers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"fuckyourcli/types"
)

type streamFixture struct {
	name   string
	prefix string
	delta  string
	finish func(string) string
	parse  func(context.Context, io.ReadCloser, types.OutputSink) (*types.StreamResult, error)
}

func streamFixtures() []streamFixture {
	return []streamFixture{
		{
			name: "responses",
			prefix: responsesToolEvent("response.output_item.added", map[string]any{"item": map[string]any{
				"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "sh", "arguments": "",
			}}) + responsesToolEvent("response.function_call_arguments.delta", map[string]any{"item_id": "fc_1", "delta": `{"command":"`}),
			delta: responsesToolEvent("response.function_call_arguments.delta", map[string]any{"item_id": "fc_1", "delta": "x"}),
			finish: func(command string) string {
				return responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_1", "arguments": `{"command":"` + command + `"}`}) +
					responsesToolEvent("response.completed", map[string]any{"response": map[string]any{}})
			},
			parse: func(ctx context.Context, b io.ReadCloser, sink types.OutputSink) (*types.StreamResult, error) {
				return parseCodexStreamResponse(ctx, b, false, sink)
			},
		},
		{
			name:   "openai",
			prefix: "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"sh","arguments":"{\"command\":\""}}]}}]}` + "\n\n",
			delta:  "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"x"}}]}}]}` + "\n\n",
			finish: func(command string) string {
				return "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
			},
			parse: func(ctx context.Context, b io.ReadCloser, sink types.OutputSink) (*types.StreamResult, error) {
				return parseOpenAIStreamResponse(ctx, b, false, sink)
			},
		},
		{
			name: "anthropic",
			prefix: anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"sh","input":{}}}`) +
				anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"partial_json":"{\"command\":\""}}`),
			delta: anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"partial_json":"x"}}`),
			finish: func(command string) string {
				return anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"partial_json":"\"}"}}`) +
					anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
					anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
					anthropicSSE("message_stop", `{"type":"message_stop"}`)
			},
			parse: parseAnthropicStreamResponse,
		},
		{
			name:   "gemini",
			prefix: "",
			delta:  "data: " + `{"candidates":[{"content":{"parts":[{"text":"x","thought":true}]}}]}` + "\n\n",
			finish: func(command string) string {
				return "data: " + fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"sh","args":{"command":%q}}} ]},"finishReason":"STOP"}]}`, command) + "\n\n"
			},
			parse: parseGeminiStreamResponse,
		},
	}
}

func fixtureProvider(name string) types.Provider {
	switch name {
	case "codex":
		return NewCodexProvider("", "probe", 0, false, "")
	case "responses":
		return NewOpenAIResponsesCompatibleProvider("https://probe.test", "", "probe", "probe", 0, false, "")
	case "openai":
		return NewOpenAICompatibleProvider("https://probe.test", "", "probe", "probe", 0, false, "")
	case "anthropic":
		return NewAnthropicProvider("https://probe.test", "", "probe", "probe", 0)
	default:
		return NewGeminiProvider("", "probe", 0)
	}
}

func TestProviderStreamActivityAndCancellation(t *testing.T) {
	fixtures := streamFixtures()
	codex := fixtures[0]
	codex.name = "codex"
	fixtures = append(fixtures, codex)
	for _, fixture := range fixtures {
		for _, mode := range []string{"active", "idle", "headers_stall", "retry_wait", "cancel"} {
			t.Run(fixture.name+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					old := providerHTTPClient
					defer func() { providerHTTPClient = old }()
					var requestCtx context.Context
					var chunks, attempts int
					providerHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						requestCtx = req.Context()
						attempts++
						if mode == "headers_stall" {
							<-req.Context().Done()
							return nil, req.Context().Err()
						}
						if mode == "retry_wait" {
							return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"3600"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
						}
						reader, writer := io.Pipe()
						go func() {
							defer writer.Close()
							if _, err := io.WriteString(writer, fixture.prefix); err != nil {
								return
							}
							if mode != "active" {
								<-req.Context().Done()
								writer.CloseWithError(req.Context().Err())
								return
							}
							for i := 0; i < 24; i++ {
								time.Sleep(20 * time.Second)
								if _, err := io.WriteString(writer, fixture.delta); err != nil {
									return
								}
								chunks++
							}
							io.WriteString(writer, fixture.finish(strings.Repeat("x", 24)))
						}()
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
					})}
					parent, cancel := context.WithCancel(context.Background())
					defer cancel()
					if mode == "cancel" {
						go func() { time.Sleep(time.Second); cancel() }()
					}
					start := time.Now()
					result, err := fixtureProvider(fixture.name).StreamChat(parent, []types.Message{{Role: "user", Content: "probe"}}, nil, NewSilentOutputSink())
					elapsed := time.Since(start)
					switch mode {
					case "active":
						if err != nil || result == nil || result.Interrupted || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Arguments["command"] != strings.Repeat("x", 24) {
							t.Fatalf("active stream: %+v, %v", result, err)
						}
						if elapsed != 8*time.Minute || chunks != 24 {
							t.Fatalf("stream cut short: elapsed=%s, chunks=%d", elapsed, chunks)
						}
					case "cancel":
						if err != nil || result == nil || !result.Interrupted || len(result.ToolCalls) != 0 || elapsed != time.Second {
							t.Fatalf("cancel: elapsed=%s, result=%+v, err=%v", elapsed, result, err)
						}
					default:
						limit := 4 * time.Minute
						if fixture.name == "gemini" {
							limit = 2 * time.Minute
						}
						if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "idle") || result != nil || elapsed != limit || parent.Err() != nil {
							t.Fatalf("stall: elapsed=%s, result=%+v, err=%v", elapsed, result, err)
						}
					}
					if requestCtx.Err() == nil || attempts != 1 {
						t.Fatalf("request leaked or stream replayed: context=%v, attempts=%d", requestCtx.Err(), attempts)
					}
				})
			})
		}
	}
}

func TestStreamReadersRejectTruncation(t *testing.T) {
	for _, fixture := range streamFixtures() {
		for _, failure := range []struct {
			name string
			err  error
		}{
			{"eof", io.EOF}, {"disconnect", io.ErrUnexpectedEOF}, {"deadline", context.DeadlineExceeded},
		} {
			t.Run(fixture.name+"/"+failure.name, func(t *testing.T) {
				body := nopCloser{&errReader{data: []byte(fixture.prefix + fixture.delta), err: failure.err}}
				result, err := fixture.parse(context.Background(), body, NewSilentOutputSink())
				if err == nil || result != nil {
					t.Fatalf("truncation accepted: %+v, %v", result, err)
				}
				if failure.err != io.EOF && !errors.Is(err, failure.err) {
					t.Fatalf("read error lost: %v", err)
				}
			})
		}
		t.Run(fixture.name+"/oversized_line", func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(fixture.prefix + "data: " + strings.Repeat("x", 8*1024*1024) + "\n\n"))
			if result, err := fixture.parse(context.Background(), body, NewSilentOutputSink()); result != nil || !errors.Is(err, bufio.ErrTooLong) {
				t.Fatalf("oversized line accepted: %+v, %v", result, err)
			}
		})
		t.Run(fixture.name+"/large_sh", func(t *testing.T) {
			command := strings.Repeat("x", 256*1024)
			var stream string
			switch fixture.name {
			case "responses", "gemini":
				stream = fixture.prefix + fixture.finish(command)
			case "openai":
				stream = fixture.prefix + "data: " + fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":%q}}]}}]}`, command) + "\n\n" + fixture.finish(command)
			case "anthropic":
				stream = fixture.prefix + anthropicSSE("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"partial_json":%q}}`, command)) + fixture.finish(command)
			}
			result, err := fixture.parse(context.Background(), io.NopCloser(strings.NewReader(stream)), NewSilentOutputSink())
			if err != nil || result == nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Arguments["command"] != command {
				t.Fatalf("large command lost or truncated: err=%v", err)
			}
		})
	}
}

func TestStreamReaderJoinsOnEarlyReturn(t *testing.T) {
	for _, fixture := range streamFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				reader, writer := io.Pipe()
				writerDone := make(chan struct{})
				go func() {
					defer close(writerDone)
					defer writer.Close()
					io.WriteString(writer, fixture.prefix+fixture.delta)
					// Leave the scanner blocked in Read until the parser closes it.
					<-ctx.Done()
				}()
				go func() { time.Sleep(time.Second); cancel() }()
				result, err := fixture.parse(ctx, reader, NewSilentOutputSink())
				if err != nil || result == nil || !result.Interrupted {
					t.Fatalf("interrupt: %+v, %v", result, err)
				}
				<-writerDone
			})
		})
	}
}

func TestStreamReadersRejectInvalidToolJSON(t *testing.T) {
	for _, fixture := range streamFixtures() {
		if fixture.name == "gemini" {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			var ending string
			switch fixture.name {
			case "responses":
				ending = responsesToolEvent("response.completed", map[string]any{"response": map[string]any{}})
			case "openai":
				ending = "data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
			case "anthropic":
				ending = anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
					anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
					anthropicSSE("message_stop", `{"type":"message_stop"}`)
			}
			stream := fixture.prefix + fixture.delta + ending
			result, err := fixture.parse(context.Background(), io.NopCloser(strings.NewReader(stream)), NewSilentOutputSink())
			if err == nil || result != nil {
				t.Fatalf("partial tool accepted: %+v, %v", result, err)
			}
		})
	}
}

func TestStreamRequestRenewsOnBodyBytes(t *testing.T) {
	for _, chunk := range []string{"x", ": heartbeat\n\n"} {
		t.Run(fmt.Sprintf("chunk=%q", chunk), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stream := newStreamRequest(context.Background(), time.Minute)
				defer stream.close()
				reader, writer := io.Pipe()
				go func() {
					defer writer.Close()
					for i := 0; i < 24; i++ {
						time.Sleep(20 * time.Second)
						if _, err := io.WriteString(writer, chunk); err != nil {
							return
						}
					}
					io.WriteString(writer, "\n")
				}()
				start := time.Now()
				lines, stop := readStreamLines(context.Background(), stream.body(reader))
				defer stop()
				for {
					line := <-lines
					if line.done {
						if line.err != nil {
							t.Fatal(line.err)
						}
						break
					}
				}
				if stream.ctx.Err() != nil || time.Since(start) != 8*time.Minute {
					t.Fatalf("body bytes failed to renew stream: %v", context.Cause(stream.ctx))
				}
			})
		})
	}
}

func TestStreamRequestLateExpiryChecksRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := newStreamRequest(context.Background(), time.Minute)
		defer stream.close()
		time.Sleep(30 * time.Second)
		stream.touch()
		stream.expire()
		time.Sleep(59 * time.Second)
		if stream.ctx.Err() != nil {
			t.Fatal("a stale timer callback canceled the renewed stream")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !errors.Is(context.Cause(stream.ctx), context.DeadlineExceeded) {
			t.Fatal("idle stream did not expire after the renewed budget")
		}
	})
}

func TestStreamReaderStopsWithFullChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, stop := readStreamLines(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("data: x\n\n", 2000))))
		synctest.Wait()
		stop()
	})
}

func TestStreamReadersRejectPendingToolsAtTerminal(t *testing.T) {
	for _, fixture := range streamFixtures() {
		if fixture.name == "gemini" {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			ending := "data: [DONE]\n\n"
			if fixture.name == "anthropic" {
				ending = anthropicSSE("message_stop", `{"type":"message_stop"}`)
			}
			result, err := fixture.parse(context.Background(), io.NopCloser(strings.NewReader(fixture.prefix+fixture.delta+ending)), NewSilentOutputSink())
			if err == nil || result != nil {
				t.Fatalf("pending tool accepted at terminal event: %+v, %v", result, err)
			}
		})
	}
}
