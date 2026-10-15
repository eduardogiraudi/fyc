package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"fuckyourcli/config"
	fycmcp "fuckyourcli/mcp"
	"fuckyourcli/providers"
	"fuckyourcli/tools"
	"fuckyourcli/types"
	"fuckyourcli/ui"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type responsesMCPApproval struct {
	requests []tools.InteractionRequest
}

func (s *responsesMCPApproval) Interact(_ context.Context, req tools.InteractionRequest) (tools.InteractionResponse, error) {
	s.requests = append(s.requests, req)
	return tools.InteractionResponse{Choice: req.AllowIndex}, nil
}

func TestResponsesMCPFinalArgumentsReachApprovalAndServer(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg := config.Get()
	config.SetNoAsk(false)
	config.SetPreToolHook(false, "")
	config.SetPostToolHook(false, "")
	t.Cleanup(func() {
		config.SetNoAsk(cfg.NoAsk)
		config.SetPreToolHook(cfg.Hooks.PreTool.Enabled, cfg.Hooks.PreTool.Command)
		config.SetPostToolHook(cfg.Hooks.PostTool.Enabled, cfg.Hooks.PostTool.Command)
		tools.SetInteractor(nil)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	server := sdk.NewServer(&sdk.Implementation{Name: "REA", Version: "test"}, nil)
	received := make(chan map[string]any, 8)
	server.AddTool(&sdk.Tool{
		Name: "binary_session",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		received <- args
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "session opened"}}}, nil
	})
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "fyc-test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	manager := fycmcp.NewMCPManager("test")
	manager.Sessions["REA"] = clientSession
	manager.Clients["REA"] = client
	listed, err := manager.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}

	event := func(kind string, fields map[string]any) string {
		fields["type"] = kind
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return "data: " + string(data) + "\n\n"
	}
	item := func(args string) map[string]any {
		return map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "REA__binary_session", "arguments": args}
	}
	const pathArgs = `{"path":"us/bin"}`
	added := event("response.output_item.added", map[string]any{"item": item("{}")})
	delta := event("response.function_call_arguments.delta", map[string]any{"item_id": "fc_1", "delta": pathArgs})
	done := event("response.function_call_arguments.done", map[string]any{"item_id": "fc_1", "arguments": pathArgs})
	emptyDone := event("response.function_call_arguments.done", map[string]any{"item_id": "fc_1", "arguments": "{}"})
	itemDone := event("response.output_item.done", map[string]any{"item": item(pathArgs)})
	completed := event("response.completed", map[string]any{"response": map[string]any{"output": []any{item(pathArgs)}}})
	stop := event("response.completed", map[string]any{"response": map[string]any{}})
	for _, tc := range []struct{ name, stream string }{
		{"final_arguments_done", added + done + stop},
		{"initial_object_and_deltas", added + delta + done + stop},
		{"output_item_done", added + itemDone + stop},
		{"completed_output", added + completed},
		{"all_final_events_once", added + emptyDone + itemDone + completed},
		{"only_completed_output", completed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				toolJSON, _ := json.Marshal(body["tools"])
				if !strings.Contains(string(toolJSON), `"path":{"type":"string"}`) {
					t.Errorf("MCP schema lost path: %s", toolJSON)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, tc.stream+"data: [DONE]\n\n")
			}))
			defer httpServer.Close()
			provider := providers.NewOpenAIResponsesCompatibleProvider(httpServer.URL, "", "test-model", "test", 0, false, "")
			sink := providers.NewSilentOutputSink()
			result, err := provider.StreamChat(ctx, []types.Message{{Role: "user", Content: "open us/bin"}}, listed, sink)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call_1" {
				t.Fatalf("wrong call count/identity: %+v", result.ToolCalls)
			}
			spy := &responsesMCPApproval{}
			tools.SetInteractor(spy)
			session := &ChatSession{MCPManager: manager, RestrictedRoot: t.TempDir(), RunCtx: ctx}
			appendAssistantStreamResult(session, result, false, "test-model")
			handleToolCallsWithSink(session, sink, result.ToolCalls, true, false)
			want := map[string]any{"path": "us/bin"}
			select {
			case got := <-received:
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("MCP received %v, want %v", got, want)
				}
			default:
				t.Fatal("MCP call was not executed")
			}
			select {
			case got := <-received:
				t.Fatalf("MCP call executed twice: %v", got)
			default:
			}
			if len(spy.requests) != 1 || spy.requests[0].Title != "MCP tool: REA__binary_session\nArgs: map[path:us/bin]" {
				t.Fatalf("wrong approval: %+v", spy.requests)
			}
			req := spy.requests[0]
			view := (ui.SelectorModel{Title: req.Title, Options: req.Options, SuggestIdx: req.SuggestionIndex}).View()
			if !strings.Contains(view, "path:us/bin") {
				t.Fatalf("path missing in rendered approval: %q", view)
			}
			if len(session.ChatHistory) != 2 || session.ChatHistory[1].ToolCallID != "call_1" || session.ChatHistory[1].Content != "session opened" {
				t.Fatalf("wrong tool history: %+v", session.ChatHistory)
			}
			if !reflect.DeepEqual(session.ChatHistory[0].ToolCalls[0].Function.Arguments, want) {
				t.Fatal("history lost final arguments")
			}
		})
	}
}
