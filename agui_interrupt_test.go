package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fuckyourcli/agui"
	"fuckyourcli/config"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// ── level-2 human-in-the-loop (standard AG-UI interrupts), real tool path ─────
//
// These tests exercise the whole chain for the interrupt lifecycle:
// handleToolCalls → tools dispatch → permission prompt → hitlInteractor →
// agui.Broker (interrupt mode) → suspended run context, and then the replay of
// the pending tool call performed by the resumed run.

// interruptHarness installs the interrupt-mode broker and the HITL interactor.
func interruptHarness(t *testing.T, timeout time.Duration) *agui.Broker {
	t.Helper()
	broker := agui.NewBroker(timeout)
	broker.SetInterruptMode(true)
	tools.SetInteractor(newHITLInteractor(broker, "deny", true))
	t.Cleanup(func() { tools.SetInteractor(nil) })
	tools.SetPermissionPersistence(false)
	t.Cleanup(func() { tools.SetPermissionPersistence(true) })

	// The write tool must really ask: no auto mode, no persistent grant.
	config.SetNoAsk(false)
	config.SetConfigAllowFileWrite(false, true)
	t.Cleanup(func() {
		config.SetNoAsk(false)
		config.SetConfigAllowFileWrite(false, true)
	})
	return broker
}

func TestInterruptSuspendsTheToolCallAndTheResumeReplaysIt(t *testing.T) {
	dir := t.TempDir()
	broker := interruptHarness(t, 5*time.Minute)

	call := writeCall("c1", "hello.txt", "hi there")

	// ── run 1: the write tool asks, the run is suspended ────────────────────
	out := &lockedBuffer{}
	sink := agui.NewSink(out, nil, "t1", "r1")
	runCtx := agui.WithRun(context.Background(), sink, "t1", "r1")
	s := &ChatSession{
		RestrictedRoot: dir,
		Sink:           sink,
		RunCtx:         runCtx,
		ChatHistory: []types.Message{
			{Role: "user", Content: "please write hello.txt"},
			{Role: "assistant", ToolCalls: []types.ToolCall{call}},
		},
	}

	handleToolCalls(s, sink, []types.ToolCall{call}, true)

	interrupted := agui.RunInterrupted(runCtx)
	if interrupted == nil {
		t.Fatal("the run was not suspended on the approval prompt")
	}
	if len(interrupted.Interrupts) != 1 {
		t.Fatalf("expected one interrupt, got %d", len(interrupted.Interrupts))
	}
	intr := interrupted.Interrupts[0]
	if intr.Reason != agui.ReasonToolCall {
		t.Fatalf("expected reason %q, got %q", agui.ReasonToolCall, intr.Reason)
	}
	if intr.ToolCallID != "c1" {
		t.Fatalf("the interrupt must be bound to the tool call, got %q", intr.ToolCallID)
	}
	if !strings.Contains(intr.Message, "hello.txt") {
		t.Fatalf("unexpected interrupt message %q", intr.Message)
	}
	metadata, _ := intr.Metadata["fyc"].(agui.InteractionRequest)
	if metadata.Title == "" || metadata.Kind != "approval" {
		t.Fatalf("the fyc payload must travel in metadata, got %+v", intr.Metadata)
	}

	// The tool never ran and its result was not recorded: the call is pending,
	// so the resumed run owns it.
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("the file must not exist while the approval is pending (err=%v)", err)
	}
	if len(s.ChatHistory) != 2 {
		t.Fatalf("no tool result may be appended by a suspended run, got %d messages", len(s.ChatHistory))
	}
	for _, ev := range out.events(t) {
		if ev["type"] == agui.EvToolCallResult {
			t.Fatalf("a suspended run must not emit TOOL_CALL_RESULT: %s", out.String())
		}
	}

	// ── the client answers by resuming the thread ───────────────────────────
	if err := broker.Resume("t1", []agui.ResumeEntry{{
		InterruptID: intr.ID,
		Status:      agui.ResumeStatusResolved,
		Payload:     json.RawMessage(`{"approved":true}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if open := broker.OpenInterrupts("t1"); len(open) != 0 {
		t.Fatalf("the interrupt must be answered, got %+v", open)
	}

	// ── run 2: the pending call is replayed, its result is emitted ──────────
	pending := pendingToolCalls(s.ChatHistory)
	if len(pending) != 1 || pending[0].ID != "c1" {
		t.Fatalf("expected the pending tool call, got %+v", pending)
	}

	out2 := &lockedBuffer{}
	sink2 := agui.NewSink(out2, nil, "t1", "r2")
	runCtx2 := agui.WithRun(context.Background(), sink2, "t1", "r2")
	s2 := &ChatSession{
		RestrictedRoot: dir,
		Sink:           sink2,
		RunCtx:         runCtx2,
		ChatHistory:    append([]types.Message(nil), s.ChatHistory...),
	}
	handleToolCallsWithSink(s2, sink2, pending, true, false)

	if agui.RunInterrupted(runCtx2) != nil {
		t.Fatal("the replayed call must not suspend the run again")
	}
	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatalf("the approved write did not happen: %v", err)
	}
	if string(data) != "hi there" {
		t.Fatalf("unexpected file content %q", data)
	}
	if res := out2.toolResult(t); !strings.Contains(res, "written") {
		t.Fatalf("unexpected tool result %q", res)
	}
	// The protocol's audit rule: no second TOOL_CALL_START on the resumed run.
	for _, ev := range out2.events(t) {
		if ev["type"] == agui.EvToolCallStart || ev["type"] == agui.EvToolCallArgs {
			t.Fatalf("a resumed run must not re-announce the tool call: %s", out2.String())
		}
	}
	if len(s2.ChatHistory) != 3 || s2.ChatHistory[2].Role != "tool" {
		t.Fatalf("the tool result must be recorded in the history, got %+v", s2.ChatHistory[2:])
	}
	if pending := pendingToolCalls(s2.ChatHistory); len(pending) != 0 {
		t.Fatalf("no tool call must stay pending after the replay, got %+v", pending)
	}
}

func TestInterruptDeniedByTheClientRecordsTheRefusal(t *testing.T) {
	dir := t.TempDir()
	broker := interruptHarness(t, 5*time.Minute)

	call := writeCall("c1", "denied.txt", "nope")
	out := &lockedBuffer{}
	sink := agui.NewSink(out, nil, "t1", "r1")
	runCtx := agui.WithRun(context.Background(), sink, "t1", "r1")
	s := &ChatSession{RestrictedRoot: dir, Sink: sink, RunCtx: runCtx}

	handleToolCalls(s, sink, []types.ToolCall{call}, true)
	intr := agui.RunInterrupted(runCtx).Interrupts[0]

	if err := broker.Resume("t1", []agui.ResumeEntry{{
		InterruptID: intr.ID,
		Status:      agui.ResumeStatusResolved,
		Payload:     json.RawMessage(`{"approved":false}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	out2 := &lockedBuffer{}
	sink2 := agui.NewSink(out2, nil, "t1", "r2")
	runCtx2 := agui.WithRun(context.Background(), sink2, "t1", "r2")
	s2 := &ChatSession{RestrictedRoot: dir, Sink: sink2, RunCtx: runCtx2}
	handleToolCallsWithSink(s2, sink2, []types.ToolCall{call}, true, false)

	if _, err := os.Stat(filepath.Join(dir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("a denied write must not create the file (err=%v)", err)
	}
	if res := out2.toolResult(t); !strings.Contains(res, "denied") {
		t.Fatalf("unexpected tool result %q", res)
	}
}

func TestInterruptModeWithHITLDisabledKeepsThePolicy(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)
	tools.SetInteractor(newHITLInteractor(broker, "deny", false))
	t.Cleanup(func() { tools.SetInteractor(nil) })
	tools.SetPermissionPersistence(false)
	t.Cleanup(func() { tools.SetPermissionPersistence(true) })
	config.SetNoAsk(false)
	config.SetConfigAllowFileWrite(false, true)

	out := &lockedBuffer{}
	sink := agui.NewSink(out, nil, "t1", "r1")
	runCtx := agui.WithRun(context.Background(), sink, "t1", "r1")
	s := &ChatSession{RestrictedRoot: dir, Sink: sink, RunCtx: runCtx}

	handleToolCalls(s, sink, []types.ToolCall{writeCall("c1", "nope.txt", "nope")}, true)

	if agui.RunInterrupted(runCtx) != nil {
		t.Fatal("with --hitl=false nothing may suspend the run")
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.txt")); !os.IsNotExist(err) {
		t.Fatalf("the deterministic policy must deny the write (err=%v)", err)
	}
	if res := out.toolResult(t); !strings.Contains(res, "denied") {
		t.Fatalf("unexpected tool result %q", res)
	}
}

func TestPendingToolCalls(t *testing.T) {
	callA := writeCall("c1", "a.txt", "a")
	callB := writeCall("c2", "b.txt", "b")

	cases := []struct {
		name    string
		history []types.Message
		want    []string
	}{
		{"empty", nil, nil},
		{"no tool calls", []types.Message{{Role: "user", Content: "hi"}}, nil},
		{
			"all results recorded",
			[]types.Message{
				{Role: "assistant", ToolCalls: []types.ToolCall{callA, callB}},
				{Role: "tool", ToolCallID: "c1", Content: "ok"},
				{Role: "tool", ToolCallID: "c2", Content: "ok"},
			},
			nil,
		},
		{
			"second call pending",
			[]types.Message{
				{Role: "assistant", ToolCalls: []types.ToolCall{callA, callB}},
				{Role: "tool", ToolCallID: "c1", Content: "ok"},
			},
			[]string{"c2"},
		},
		{
			"interrupted on the first call",
			[]types.Message{{Role: "assistant", ToolCalls: []types.ToolCall{callA, callB}}},
			[]string{"c1", "c2"},
		},
		{
			"older assistant turn complete",
			[]types.Message{
				{Role: "assistant", ToolCalls: []types.ToolCall{callA}},
				{Role: "tool", ToolCallID: "c1", Content: "ok"},
				{Role: "assistant", Content: "done"},
			},
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingToolCalls(tc.history)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %v, got %+v", tc.want, got)
			}
			for i, want := range tc.want {
				if got[i].ID != want {
					t.Fatalf("expected %v, got %+v", tc.want, got)
				}
			}
		})
	}
}

func TestClosePendingToolCallsKeepsTheHistoryValid(t *testing.T) {
	s := &ChatSession{ChatHistory: []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{writeCall("c1", "a.txt", "a")}},
	}}
	closePendingToolCalls(s, pendingToolCalls(s.ChatHistory))

	if len(s.ChatHistory) != 2 {
		t.Fatalf("expected a closing tool message, got %+v", s.ChatHistory)
	}
	last := s.ChatHistory[1]
	if last.Role != "tool" || last.ToolCallID != "c1" || !strings.Contains(last.Content, "interrupted") {
		t.Fatalf("unexpected closing message: %+v", last)
	}
	if pending := pendingToolCalls(s.ChatHistory); len(pending) != 0 {
		t.Fatalf("nothing may stay pending after the cleanup, got %+v", pending)
	}
}
