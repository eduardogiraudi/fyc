package agui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fuckyourcli/types"
)

// interruptRunner mirrors the real tool loop at level 2: it announces a tool
// call and asks the broker for approval (suspending the run), then, on the
// resumed run, replays the same call and reports its TOOL_CALL_RESULT.
type interruptRunner struct {
	broker   *Broker
	parallel int
	runs     int
	verdicts []string
}

func (r *interruptRunner) Run(ctx context.Context, req RunRequest, sink types.OutputSink) error {
	r.runs++
	n := r.parallel
	if n == 0 {
		n = 1
	}

	suspended := false
	for i := 0; i < n; i++ {
		toolCallID := fmt.Sprintf("call_%d", i+1)
		args := map[string]interface{}{"path": toolCallID + ".txt"}
		if !req.Resume {
			if ts, ok := sink.(types.ToolEventSink); ok {
				ts.ToolCallStarted(toolCallID, "write", args)
			}
		}

		callCtx := WithToolCall(ctx, toolCallID, "write")
		ask := InteractionRequest{
			Kind:       KindApproval,
			Title:      "Write file: " + toolCallID + ".txt",
			Options:    []string{"Allow", "Deny"},
			AllowIndex: 0,
			DenyIndex:  1,
		}
		ans, err := r.broker.Request(callCtx, ask, "approval|file|"+toolCallID+".txt")
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				suspended = true
				continue
			}
			return err
		}

		verdict := "denied"
		if ans.Choice != nil && *ans.Choice == 0 {
			verdict = "approved"
		}
		r.verdicts = append(r.verdicts, toolCallID+":"+verdict)
		if ts, ok := sink.(types.ToolEventSink); ok {
			ts.ToolCallFinished(toolCallID, "write", "wrote "+toolCallID+".txt")
		}
	}

	if suspended {
		// The protocol requires the state needed for the resume before
		// RUN_FINISHED: the runner fills the snapshot in as the real one does.
		interrupted := RunInterrupted(ctx)
		interrupted.Snapshot = []Message{
			{ID: "m1", Role: "user", Content: "write the file"},
			{ID: "m2", Role: "assistant", ToolCalls: []InputToolCall{{
				ID: "call_1", Type: "function",
				Function: InputToolFunction{Name: "write", Arguments: `{"path":"call_1.txt"}`},
			}}},
		}
		return interrupted
	}

	sink.WriteText(strings.Join(r.verdicts, ","))
	sink.WriteNewline()
	return nil
}

// postRun posts a RunAgentInput and returns the decoded SSE events.
func postRun(t *testing.T, url, body string) []map[string]interface{} {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer s3cr3t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	var out []map[string]interface{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("invalid event %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func findEvent(events []map[string]interface{}, typ string) map[string]interface{} {
	for _, ev := range events {
		if ev["type"] == typ {
			return ev
		}
	}
	return nil
}

func TestInterruptRoundTripOverHTTP(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)
	runner := &interruptRunner{broker: broker}
	ts := httptest.NewServer(NewServer(Options{Token: "s3cr3t", Interactions: broker}, runner).Handler())
	defer ts.Close()

	// Run 1: the tool call is announced, the approval cannot be answered inline,
	// so the run ends with the interrupt outcome.
	events := postRun(t, ts.URL, `{"threadId":"t1","runId":"r1","messages":[{"role":"user","content":"write the file"}]}`)

	if findEvent(events, EvRunError) != nil {
		t.Fatalf("a suspended run must not emit RUN_ERROR: %+v", events)
	}
	start, finish := findEvent(events, EvToolCallStart), findEvent(events, EvRunFinished)
	if start == nil || finish == nil {
		t.Fatalf("unexpected event sequence: %+v", events)
	}
	if start["toolCallId"] != "call_1" || start["toolCallName"] != "write" {
		t.Fatalf("unexpected tool call announcement: %+v", start)
	}

	outcome, _ := finish["outcome"].(map[string]interface{})
	if outcome["type"] != OutcomeInterrupt {
		t.Fatalf("expected an interrupt outcome, got %+v", finish["outcome"])
	}
	interrupts, _ := outcome["interrupts"].([]interface{})
	if len(interrupts) != 1 {
		t.Fatalf("expected one interrupt, got %+v", outcome)
	}
	intr, _ := interrupts[0].(map[string]interface{})
	if intr["reason"] != ReasonToolCall || intr["toolCallId"] != "call_1" {
		t.Fatalf("unexpected interrupt: %+v", intr)
	}
	if intr["expiresAt"] == "" || intr["responseSchema"] == nil {
		t.Fatalf("the interrupt must carry an expiry and a response schema: %+v", intr)
	}
	metadata, _ := intr["metadata"].(map[string]interface{})
	fyc, _ := metadata["fyc"].(map[string]interface{})
	if fyc == nil || fyc["title"] != "Write file: call_1.txt" || fyc["kind"] != KindApproval {
		t.Fatalf("the fyc payload must travel in metadata: %+v", metadata)
	}

	// The snapshot must precede RUN_FINISHED (protocol rule for the resume).
	var snapshotAt, finishAt = -1, -1
	for i, ev := range events {
		switch ev["type"] {
		case EvMessagesSnapshot:
			snapshotAt = i
			msgs, _ := ev["messages"].([]interface{})
			if len(msgs) != 2 {
				t.Fatalf("unexpected snapshot: %+v", ev)
			}
			assistant, _ := msgs[1].(map[string]interface{})
			if assistant["role"] != "assistant" || assistant["toolCalls"] == nil {
				t.Fatalf("the snapshot must carry the pending tool call: %+v", assistant)
			}
		case EvRunFinished:
			finishAt = i
		}
	}
	if snapshotAt < 0 || snapshotAt > finishAt {
		t.Fatalf("MESSAGES_SNAPSHOT must be emitted before RUN_FINISHED (%d vs %d)", snapshotAt, finishAt)
	}

	// Rule 4: a new run on the thread must resume the pending interrupt.
	events = postRun(t, ts.URL, `{"threadId":"t1","runId":"r2","messages":[{"role":"user","content":"write the file"}]}`)
	errEvent := findEvent(events, EvRunError)
	if errEvent == nil {
		t.Fatalf("expected RUN_ERROR while an interrupt is open, got %+v", events)
	}
	if msg, _ := errEvent["message"].(string); !strings.Contains(msg, "resume") {
		t.Fatalf("unexpected error message %q", msg)
	}
	if runner.runs != 1 {
		t.Fatalf("the rejected run must not reach the runner (runs=%d)", runner.runs)
	}

	// Rule 2: an unknown interrupt id is rejected too.
	events = postRun(t, ts.URL, `{"threadId":"t1","runId":"r3","resume":[{"interruptId":"int_nope","status":"resolved","payload":{"approved":true}}]}`)
	errEvent = findEvent(events, EvRunError)
	if errEvent == nil {
		t.Fatalf("expected RUN_ERROR for an unknown interrupt, got %+v", events)
	}
	if msg, _ := errEvent["message"].(string); !strings.Contains(msg, "unknown interrupt") {
		t.Fatalf("unexpected error message %q", msg)
	}

	// Run 2: the resume answers the interrupt and the tool call is replayed. Only
	// its TOOL_CALL_RESULT is emitted, never a second announcement.
	id, _ := intr["id"].(string)
	events = postRun(t, ts.URL, fmt.Sprintf(
		`{"threadId":"t1","runId":"r4","resume":[{"interruptId":"%s","status":"resolved","payload":{"approved":true}}]}`,
		id))

	if ev := findEvent(events, EvToolCallStart); ev != nil {
		t.Fatalf("a resumed run must not re-announce the tool call: %+v", ev)
	}
	result := findEvent(events, EvToolCallResult)
	if result == nil || result["toolCallId"] != "call_1" {
		t.Fatalf("expected the tool result of the replayed call, got %+v", events)
	}
	finish = findEvent(events, EvRunFinished)
	if finish == nil {
		t.Fatalf("the resumed run never finished: %+v", events)
	}
	outcome, _ = finish["outcome"].(map[string]interface{})
	if outcome["type"] != OutcomeSuccess {
		t.Fatalf("the resumed run must complete normally, got %+v", finish["outcome"])
	}
	if len(runner.verdicts) != 1 || runner.verdicts[0] != "call_1:approved" {
		t.Fatalf("the answer did not reach the tool: %v", runner.verdicts)
	}
	if open := broker.OpenInterrupts("t1"); len(open) != 0 {
		t.Fatalf("the interrupt must be closed, got %+v", open)
	}
}

func TestInterruptResumeMustCoverEveryInterrupt(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)
	runner := &interruptRunner{broker: broker, parallel: 2}
	ts := httptest.NewServer(NewServer(Options{Token: "s3cr3t", Interactions: broker}, runner).Handler())
	defer ts.Close()

	events := postRun(t, ts.URL, `{"threadId":"t1","runId":"r1","messages":[{"role":"user","content":"go"}]}`)
	finish := findEvent(events, EvRunFinished)
	outcome, _ := finish["outcome"].(map[string]interface{})
	interrupts, _ := outcome["interrupts"].([]interface{})
	if len(interrupts) != 2 {
		t.Fatalf("expected two interrupts, got %+v", outcome)
	}
	first, _ := interrupts[0].(map[string]interface{})
	firstID, _ := first["id"].(string)

	// Rule 3: a partial resume is refused.
	events = postRun(t, ts.URL, fmt.Sprintf(
		`{"threadId":"t1","runId":"r2","resume":[{"interruptId":"%s","status":"resolved","payload":{"approved":true}}]}`, firstID))
	errEvent := findEvent(events, EvRunError)
	if errEvent == nil {
		t.Fatalf("expected RUN_ERROR for a partial resume, got %+v", events)
	}
	if msg, _ := errEvent["message"].(string); !strings.Contains(msg, "every open interrupt") {
		t.Fatalf("unexpected error message %q", msg)
	}

	// A complete resume is accepted.
	events = postRun(t, ts.URL, fmt.Sprintf(
		`{"threadId":"t1","runId":"r3","resume":[{"interruptId":"%s","status":"resolved","payload":{"approved":true}},{"interruptId":"%s","status":"cancelled"}]}`,
		firstID, mustID(t, interrupts[1])))
	if ev := findEvent(events, EvRunError); ev != nil {
		t.Fatalf("the complete resume must be accepted, got %+v", ev)
	}
	if len(runner.verdicts) != 2 || runner.verdicts[0] != "call_1:approved" || runner.verdicts[1] != "call_2:denied" {
		t.Fatalf("unexpected verdicts: %v", runner.verdicts)
	}
}

func mustID(t *testing.T, raw interface{}) string {
	t.Helper()
	obj, _ := raw.(map[string]interface{})
	id, _ := obj["id"].(string)
	if id == "" {
		t.Fatalf("no interrupt id in %+v", raw)
	}
	return id
}
