package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/agui"
	"fuckyourcli/types"
)

func responsesToolEvent(kind string, fields map[string]any) string {
	event := map[string]any{"type": kind}
	for key, value := range fields {
		event[key] = value
	}
	data, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return "data: " + string(data) + "\n\n"
}

func responsesToolItem(args string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": "fc_1", "call_id": "call_1",
		"name": "REA__binary_session", "arguments": args,
	}
}

func TestResponsesToolCallFinalArguments(t *testing.T) {
	const pathArgs = `{"path":"us/bin"}`
	added := func(args string) string {
		return responsesToolEvent("response.output_item.added", map[string]any{"item": responsesToolItem(args)})
	}
	delta := func(args string) string {
		return responsesToolEvent("response.function_call_arguments.delta", map[string]any{"item_id": "fc_1", "delta": args})
	}
	done := func(args string) string {
		fields := map[string]any{"item_id": "fc_1"}
		if args != "" {
			fields["arguments"] = args
		}
		return responsesToolEvent("response.function_call_arguments.done", fields)
	}
	itemDone := responsesToolEvent("response.output_item.done", map[string]any{"item": responsesToolItem(pathArgs)})
	completed := responsesToolEvent("response.completed", map[string]any{"response": map[string]any{"output": []any{responsesToolItem(pathArgs)}}})
	stop := responsesToolEvent("response.completed", map[string]any{"response": map[string]any{}})
	cases := []struct {
		name, stream, wantRaw string
	}{
		{"standard", added("") + delta(`{"path":`) + delta(`"us/bin"}`) + done(pathArgs) + itemDone + completed, pathArgs},
		{"initial_object_then_done", added("{}") + done(pathArgs) + stop, pathArgs},
		{"initial_object_then_deltas", added("{}") + delta(pathArgs) + done(pathArgs) + stop, pathArgs},
		{"deltas_without_final_arguments", added("{}") + delta(pathArgs) + done("") + stop, pathArgs},
		{"final_replaces_partial_delta", added("") + delta(`{"path":"us/`) + done(pathArgs) + stop, pathArgs},
		{"final_replaces_stale_arguments", added("") + delta(`{"path":"wrong"}`) + done(pathArgs) + stop, pathArgs},
		{"output_item_done", added("") + itemDone + stop, pathArgs},
		{"output_item_done_without_added", itemDone + stop, pathArgs},
		{"completed_output", added("{}") + completed, pathArgs},
		{"completed_output_without_added", completed, pathArgs},
		{"item_corrects_earlier_empty_done", added("") + done("{}") + itemDone + completed, pathArgs},
		{"completed_corrects_earlier_empty_done", added("") + done("{}") + completed, pathArgs},
		{"initial_arguments_fallback", added(pathArgs) + stop, pathArgs},
		{"delta_arguments_fallback", added("{}") + delta(pathArgs) + stop, pathArgs},
		{"intentional_empty_final_object", added(pathArgs) + done("{}") + stop, "{}"},
		{"empty_final_string_fallback", added("") + delta(pathArgs) + responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_1", "arguments": ""}) + stop, pathArgs},
		{"final_name_without_added", responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_1", "call_id": "call_1", "name": "REA__binary_session", "arguments": pathArgs}) + stop, pathArgs},
		{"call_id_alias", added("{}") + responsesToolEvent("response.function_call_arguments.done", map[string]any{"call_id": "call_1", "arguments": pathArgs}) + itemDone + completed, pathArgs},
		{"completed_alias", added("{}") + responsesToolEvent("response.complete", map[string]any{"response": map[string]any{"output": []any{responsesToolItem(pathArgs)}}}), pathArgs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseCodexStreamResponse(context.Background(), io.NopCloser(strings.NewReader(tc.stream+"data: [DONE]\n\n")), false, NewSilentOutputSink())
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ToolCalls) != 1 {
				t.Fatalf("expected exactly one call, got %+v", result.ToolCalls)
			}
			call := result.ToolCalls[0]
			if call.ID != "call_1" || call.Function.Name != "REA__binary_session" {
				t.Fatalf("wrong call identity: %+v", call)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.wantRaw), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(call.Function.Arguments, want) || call.Function.ArgumentsRaw != tc.wantRaw {
				t.Fatalf("arguments = %v, raw = %q; want %v / %q", call.Function.Arguments, call.Function.ArgumentsRaw, want, tc.wantRaw)
			}
			input := convertMessagesToCodexInput([]types.Message{{Role: "assistant", ToolCalls: result.ToolCalls}}, "probe")
			if len(input) != 1 || input[0]["call_id"] != "call_1" || input[0]["arguments"] != tc.wantRaw {
				t.Fatalf("replay lost final arguments or call ID: %+v", input)
			}
		})
	}
}

func TestResponsesToolCallsParallelFinalOutput(t *testing.T) {
	first := responsesToolItem(`{"path":"us/bin","options":{"enabled":true,"count":2},"items":["a","b"]}`)
	second := responsesToolItem(`{"path":"second"}`)
	second["id"], second["call_id"] = "fc_2", "call_2"
	initialFirst := responsesToolItem("{}")
	initialSecond := responsesToolItem("{}")
	initialSecond["id"], initialSecond["call_id"] = "fc_2", "call_2"
	stream := responsesToolEvent("response.output_item.added", map[string]any{"item": initialFirst}) +
		responsesToolEvent("response.output_item.added", map[string]any{"item": initialSecond}) +
		responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_2", "arguments": "{}"}) +
		responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_1", "arguments": "{}"}) +
		responsesToolEvent("response.output_item.done", map[string]any{"item": second}) +
		responsesToolEvent("response.completed", map[string]any{"response": map[string]any{"output": []any{first, second}}})
	result, err := parseCodexStreamResponse(context.Background(), io.NopCloser(strings.NewReader(stream)), false, NewSilentOutputSink())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("duplicate or missing calls: %+v", result.ToolCalls)
	}
	byID := map[string]types.ToolCall{}
	for _, call := range result.ToolCalls {
		byID[call.ID] = call
	}
	for _, item := range []map[string]any{first, second} {
		var want map[string]any
		if err := json.Unmarshal([]byte(item["arguments"].(string)), &want); err != nil {
			t.Fatal(err)
		}
		if got := byID[item["call_id"].(string)].Function.Arguments; !reflect.DeepEqual(got, want) {
			t.Fatalf("call %s: arguments %v, want %v", item["call_id"], got, want)
		}
	}
}

func TestResponsesToolCallMetadataFallbacks(t *testing.T) {
	const args = `{"path":"us/bin"}`
	for _, tc := range []struct {
		name, stream, id string
	}{
		{
			"item_id_only",
			responsesToolEvent("response.output_item.done", map[string]any{"item": map[string]any{
				"type": "function_call", "id": "fc_1", "name": "REA__binary_session", "arguments": args,
			}}),
			"fc_1",
		},
		{
			"call_id_only",
			responsesToolEvent("response.completed", map[string]any{"response": map[string]any{"output": []any{map[string]any{
				"type": "function_call", "call_id": "call_1", "name": "REA__binary_session", "arguments": args,
			}}}}),
			"call_1",
		},
		{
			"delta_before_metadata",
			responsesToolEvent("response.function_call_arguments.delta", map[string]any{"item_id": "fc_1", "delta": args}) +
				responsesToolEvent("response.output_item.added", map[string]any{"item": responsesToolItem("{}")}) +
				responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_1"}),
			"call_1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseCodexStreamResponse(context.Background(), io.NopCloser(strings.NewReader(tc.stream+"data: [DONE]\n\n")), false, NewSilentOutputSink())
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ToolCalls) != 1 {
				t.Fatalf("wrong call count: %+v", result.ToolCalls)
			}
			call := result.ToolCalls[0]
			if call.ID != tc.id || call.Function.Name != "REA__binary_session" || call.Function.Arguments["path"] != "us/bin" {
				t.Fatalf("metadata or arguments lost: %+v", call)
			}
		})
	}
}

func TestResponsesToolCallsFinalFlushOrder(t *testing.T) {
	var added, deltas, metadata string
	for _, id := range []string{"3", "1", "2"} {
		args := `{"path":"path_` + id + `"}`
		item := responsesToolItem(args)
		item["id"], item["call_id"] = "fc_"+id, "call_"+id
		added += responsesToolEvent("response.output_item.added", map[string]any{"item": item})
		added += responsesToolEvent("response.function_call_arguments.delta", map[string]any{"call_id": "call_" + id, "delta": ""})
		deltas += responsesToolEvent("response.function_call_arguments.delta", map[string]any{"item_id": "fc_" + id, "delta": args})
		initial := responsesToolItem("{}")
		initial["id"], initial["call_id"] = "fc_"+id, "call_"+id
		metadata = responsesToolEvent("response.output_item.added", map[string]any{"item": initial}) + metadata
	}
	for _, completion := range []string{"response.completed", "response.complete"} {
		t.Run(completion, func(t *testing.T) {
			for _, tc := range []struct {
				name, stream string
				want         []string
			}{
				{"initial_arguments_and_aliases", added, []string{"call_3", "call_1", "call_2"}},
				{"deltas_before_reversed_metadata", deltas + metadata, []string{"call_3", "call_1", "call_2"}},
				{
					"preserve_already_finalized_position",
					added + responsesToolEvent("response.function_call_arguments.done", map[string]any{"item_id": "fc_1"}),
					[]string{"call_1", "call_3", "call_2"},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					stream := tc.stream + responsesToolEvent(completion, map[string]any{"response": map[string]any{}})
					for run := 0; run < 32; run++ {
						result, err := parseCodexStreamResponse(context.Background(), io.NopCloser(strings.NewReader(stream)), false, NewSilentOutputSink())
						if err != nil {
							t.Fatal(err)
						}
						if len(result.ToolCalls) != len(tc.want) {
							t.Fatalf("duplicate or missing calls: %+v", result.ToolCalls)
						}
						for i, wantID := range tc.want {
							call := result.ToolCalls[i]
							path := "path_" + strings.TrimPrefix(wantID, "call_")
							if call.ID != wantID || call.Function.Name != "REA__binary_session" || call.Function.Arguments["path"] != path {
								t.Fatalf("run %d, call %d = %+v; want %s with path %s", run, i, call, wantID, path)
							}
						}
					}
				})
			}
		})
	}
}

type responsesDiffCraftUpdate struct {
	label    string
	add, del int
}

type responsesDiffCraftSink struct {
	*SilentOutputSink
	updates         []responsesDiffCraftUpdate
	stops, finishes int
}

func (s *responsesDiffCraftSink) WriteDiffCraftFor(label string, add, del int) {
	s.updates = append(s.updates, responsesDiffCraftUpdate{label, add, del})
}

func (s *responsesDiffCraftSink) StopSpinner()     { s.stops++ }
func (s *responsesDiffCraftSink) FinishDiffCraft() { s.finishes++ }

func TestResponsesDiffCraftDeduplicatesPerCall(t *testing.T) {
	const args = `{"path":"main.go","start_anchor":"A:1","end_anchor":"A:1","replacement":"x"}`
	const corrected = `{"path":"other.go","start_anchor":"A:1","end_anchor":"B:2","replacement":"x\ny"}`
	item := func(id, name, raw string) map[string]any {
		return map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": "call_" + id, "name": name, "arguments": raw}
	}
	added := func(id, name, raw string) string {
		return responsesToolEvent("response.output_item.added", map[string]any{"item": item(id, name, raw)})
	}
	delta := func(id, raw string) string {
		return responsesToolEvent("response.function_call_arguments.delta", map[string]any{"item_id": "fc_" + id, "delta": raw})
	}
	done := func(id, raw string) string {
		return responsesToolEvent("response.function_call_arguments.done", map[string]any{"call_id": "call_" + id, "arguments": raw})
	}
	itemDone := func(id, name, raw string) string {
		return responsesToolEvent("response.output_item.done", map[string]any{"item": item(id, name, raw)})
	}
	completed := func(items ...any) string {
		return responsesToolEvent("response.completed", map[string]any{"response": map[string]any{"output": items}})
	}
	first := responsesDiffCraftUpdate{"patching file main.go", 1, 1}
	second := responsesDiffCraftUpdate{"patching file other.go", 2, 2}
	initial := added("1", "replace_anchors", "")
	final := item("1", "replace_anchors", args)
	for _, tc := range []struct {
		name, stream string
		want         []responsesDiffCraftUpdate
	}{
		{"delta_and_all_final_snapshots", initial + delta("1", args) + delta("1", "") + done("1", args) + itemDone("1", "replace_anchors", args) + completed(final) + completed(final), []responsesDiffCraftUpdate{first}},
		{"done_without_deltas", initial + done("1", args) + itemDone("1", "replace_anchors", args) + completed(final), []responsesDiffCraftUpdate{first}},
		{"initial_arguments_flush", added("1", "replace_anchors", args) + completed() + completed(), []responsesDiffCraftUpdate{first}},
		{"item_corrects_arguments", initial + delta("1", args) + done("1", args) + itemDone("1", "replace_anchors", corrected) + completed(item("1", "replace_anchors", corrected)), []responsesDiffCraftUpdate{first, second}},
		{"completed_corrects_arguments", initial + done("1", args) + completed(item("1", "replace_anchors", corrected)), []responsesDiffCraftUpdate{first, second}},
		{"done_corrects_partial_delta", initial + delta("1", `{"path":"main.go","replacement":"x`) + done("1", args) + completed(final), []responsesDiffCraftUpdate{{"patching file main.go", 1, 0}, first}},
		{"empty_raw_is_not_already_notified", initial + delta("1", "") + delta("1", "") + done("1", "") + completed(), []responsesDiffCraftUpdate{{"patching file", 0, 0}}},
		{"name_arrives_after_delta", delta("1", args) + itemDone("1", "replace_anchors", args) + completed(final), []responsesDiffCraftUpdate{first}},
		{"identical_calls_remain_distinct", initial + added("2", "replace_anchors", "") + delta("1", args) + delta("2", args) + done("1", args) + done("2", args) + completed(final, item("2", "replace_anchors", args)), []responsesDiffCraftUpdate{first, first}},
		{"other_tools_are_ignored", added("1", "REA__binary_session", args) + delta("1", args) + done("1", args) + itemDone("1", "REA__binary_session", args) + completed(item("1", "REA__binary_session", args)), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &responsesDiffCraftSink{SilentOutputSink: NewSilentOutputSink()}
			if _, err := parseCodexStreamResponse(context.Background(), io.NopCloser(strings.NewReader(tc.stream)), false, sink); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sink.updates, tc.want) {
				t.Fatalf("updates = %+v; want %+v", sink.updates, tc.want)
			}
			wantLifecycle := 0
			if len(tc.want) > 0 {
				wantLifecycle = 1
			}
			if sink.stops != wantLifecycle || sink.finishes != wantLifecycle {
				t.Fatalf("spinner stops = %d, finishes = %d; want %d each", sink.stops, sink.finishes, wantLifecycle)
			}

			var output bytes.Buffer
			aguiSink := agui.NewSink(&output, nil, "thread", "run")
			if _, err := parseCodexStreamResponse(context.Background(), io.NopCloser(strings.NewReader(tc.stream)), false, aguiSink); err != nil {
				t.Fatal(err)
			}
			if err := aguiSink.Err(); err != nil {
				t.Fatal(err)
			}
			var events []map[string]int
			for _, line := range strings.Split(output.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Type  string         `json:"type"`
					Name  string         `json:"name"`
					Value map[string]int `json:"value"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type != agui.EvCustom || event.Name != "fyc.replace_anchors" {
					t.Fatalf("unexpected AG-UI event: %s", line)
				}
				events = append(events, event.Value)
			}
			if len(events) != len(tc.want) {
				t.Fatalf("AG-UI events = %+v; want %d events", events, len(tc.want))
			}
			for i, want := range tc.want {
				if !reflect.DeepEqual(events[i], map[string]int{"add": want.add, "del": want.del}) {
					t.Fatalf("AG-UI event %d = %+v; want %+v", i, events[i], want)
				}
			}
		})
	}
}
