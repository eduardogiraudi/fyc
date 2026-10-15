package agui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// startRun builds a run context exactly like the HTTP handler does.
func startRun(threadID, runID string) (context.Context, *syncBuffer, *Sink) {
	buf := &syncBuffer{}
	sink := NewSink(buf, nil, threadID, runID)
	return WithRun(context.Background(), sink, threadID, runID), buf, sink
}

func approval(title string) InteractionRequest {
	return InteractionRequest{
		Kind:            KindApproval,
		Title:           title,
		Options:         []string{"Allow", "Deny", "Allow always", "Deny with suggestion"},
		AllowIndex:      0,
		DenyIndex:       1,
		SuggestionIndex: 3,
	}
}

func intPtr(v int) *int { return &v }

func TestInterruptSuspendsTheRunAndResumesIt(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)
	if !broker.InterruptMode() {
		t.Fatal("the broker must be in interrupt mode")
	}

	ctx, _, _ := startRun("t1", "r1")
	ctx = WithToolCall(ctx, "call_1", "write")

	req := approval("Write file: /tmp/hello.txt")
	if _, err := broker.Request(ctx, req, "approval|file|/tmp/hello.txt"); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("the run context must be canceled when the run is suspended")
	}

	interrupted := RunInterrupted(ctx)
	if interrupted == nil {
		t.Fatal("no InterruptError attached to the run context")
	}
	if interrupted.ThreadID != "t1" || interrupted.RunID != "r1" {
		t.Fatalf("unexpected interrupt error: %+v", interrupted)
	}
	if len(interrupted.Interrupts) != 1 {
		t.Fatalf("expected exactly one interrupt, got %d", len(interrupted.Interrupts))
	}

	intr := interrupted.Interrupts[0]
	if intr.ID == "" {
		t.Fatal("the interrupt has no id")
	}
	if intr.Reason != ReasonToolCall {
		t.Fatalf("expected reason %q, got %q", ReasonToolCall, intr.Reason)
	}
	if intr.ToolCallID != "call_1" {
		t.Fatalf("expected the tool call id, got %q", intr.ToolCallID)
	}
	if intr.Message != "Write file: /tmp/hello.txt" {
		t.Fatalf("unexpected message %q", intr.Message)
	}
	if intr.ExpiresAt == "" {
		t.Fatal("the interrupt must carry an expiry")
	}
	if _, ok := intr.ResponseSchema["properties"]; !ok {
		t.Fatalf("the interrupt must carry a response schema, got %+v", intr.ResponseSchema)
	}
	// The fyc payload travels as metadata.fyc, in the level-1 shape.
	fyc, ok := intr.Metadata["fyc"].(InteractionRequest)
	if !ok {
		t.Fatalf("expected the fyc payload in metadata, got %T", intr.Metadata["fyc"])
	}
	if fyc.InteractionID != intr.ID || fyc.RunID != "r1" || fyc.ThreadID != "t1" {
		t.Fatalf("unexpected fyc payload: %+v", fyc)
	}
	if len(fyc.Options) != 4 || fyc.SuggestionIndex != 3 {
		t.Fatalf("the option list must travel in the payload: %+v", fyc)
	}

	// Contract rules: the interrupt is open for its own thread only.
	if open := broker.OpenInterrupts("t1"); len(open) != 1 || open[0].ID != intr.ID {
		t.Fatalf("expected one open interrupt on t1, got %+v", open)
	}
	if open := broker.OpenInterrupts("t2"); len(open) != 0 {
		t.Fatalf("another thread must not see it, got %+v", open)
	}

	// The client resumes the run.
	entries := []ResumeEntry{{
		InterruptID: intr.ID,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"approved":true,"remember":true}`),
	}}
	if err := broker.Resume("t1", entries); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if open := broker.OpenInterrupts("t1"); len(open) != 0 {
		t.Fatalf("the interrupt must be answered, got %+v", open)
	}

	// The resumed run replays the very same tool call: the answer is ready and the
	// run is not suspended again.
	ctx2, _, _ := startRun("t1", "r2")
	ctx2 = WithToolCall(ctx2, "call_1", "write")
	ans, err := broker.Request(ctx2, req, "approval|file|/tmp/hello.txt")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if ans.Choice == nil || *ans.Choice != 0 || !ans.Remember {
		t.Fatalf("unexpected replayed answer: %+v", ans)
	}
	if RunInterrupted(ctx2) != nil {
		t.Fatal("the replayed call must not suspend the run again")
	}

	// Rule 5: replaying the same resume is a no-op.
	if err := broker.Resume("t1", entries); err != nil {
		t.Fatalf("idempotent resume: %v", err)
	}
	// A conflicting answer for an already applied interrupt is an error.
	conflict := []ResumeEntry{{InterruptID: intr.ID, Status: ResumeStatusResolved, Payload: json.RawMessage(`{"approved":false}`)}}
	if err := broker.Resume("t1", conflict); !errors.Is(err, ErrInterruptConflict) {
		t.Fatalf("expected ErrInterruptConflict, got %v", err)
	}
}

func TestInterruptDenyAndSuggestionPayloads(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantChoice int
		wantText   string
	}{
		{"approved", `{"approved":true}`, 0, ""},
		{"denied", `{"approved":false}`, 1, ""},
		{"explicit choice", `{"approved":false,"choice":2}`, 2, ""},
		{"deny with suggestion", `{"approved":false,"text":"use a temp file"}`, 3, "use a temp file"},
		// A client that answers with the option index of "Deny with suggestion"
		// (published as metadata.fyc.suggestionIndex) must keep its text too.
		{"deny with suggestion via choice", `{"approved":false,"choice":3,"text":"use a temp file"}`, 3, "use a temp file"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := NewBroker(5 * time.Minute)
			broker.SetInterruptMode(true)
			ctx, _, _ := startRun("t1", "r1")
			ctx = WithToolCall(ctx, "call_1", "write")
			if _, err := broker.Request(ctx, approval("Write file: /tmp/x"), ""); !errors.Is(err, ErrInterrupted) {
				t.Fatalf("expected ErrInterrupted, got %v", err)
			}
			id := RunInterrupted(ctx).Interrupts[0].ID
			if err := broker.Resume("t1", []ResumeEntry{{
				InterruptID: id,
				Status:      ResumeStatusResolved,
				Payload:     json.RawMessage(tc.payload),
			}}); err != nil {
				t.Fatalf("resume: %v", err)
			}

			ctx2, _, _ := startRun("t1", "r2")
			ctx2 = WithToolCall(ctx2, "call_1", "write")
			ans, err := broker.Request(ctx2, approval("Write file: /tmp/x"), "")
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if ans.Choice == nil || *ans.Choice != tc.wantChoice {
				t.Fatalf("expected choice %d, got %v", tc.wantChoice, ans.Choice)
			}
			if ans.Text != tc.wantText {
				t.Fatalf("expected text %q, got %q", tc.wantText, ans.Text)
			}
		})
	}
}

func TestInterruptQuestionAndTypedConfirm(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)

	// questionary -> reason input_required, answers come back in order
	ctx, _, _ := startRun("t1", "r1")
	ctx = WithToolCall(ctx, "call_q", "questionary")
	q := InteractionRequest{Kind: KindQuestion, Questions: []InteractionQuestion{
		{Question: "Which environment?"},
		{Question: "Which branch?"},
	}}
	if _, err := broker.Request(ctx, q, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	intr := RunInterrupted(ctx).Interrupts[0]
	if intr.Reason != ReasonInputRequired {
		t.Fatalf("expected reason %q, got %q", ReasonInputRequired, intr.Reason)
	}
	if !strings.Contains(intr.Message, "Which environment?") {
		t.Fatalf("the questions must be the fallback message, got %q", intr.Message)
	}

	// typed confirmation -> reason confirmation, the text must round-trip
	ctxTyped, _, _ := startRun("t2", "r1")
	ctxTyped = WithToolCall(ctxTyped, "call_t", "git_reset")
	typed := InteractionRequest{Kind: KindTypedConfirm, Title: "Discard everything", Expect: "RESET"}
	if _, err := broker.Request(ctxTyped, typed, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	typedIntr := RunInterrupted(ctxTyped).Interrupts[0]
	if typedIntr.Reason != ReasonConfirmation {
		t.Fatalf("expected reason %q, got %q", ReasonConfirmation, typedIntr.Reason)
	}

	if err := broker.Resume("t1", []ResumeEntry{{
		InterruptID: intr.ID,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"answers":["prod","main"]}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := broker.Resume("t2", []ResumeEntry{{
		InterruptID: typedIntr.ID,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"text":"RESET"}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	ctxQ2, _, _ := startRun("t1", "r2")
	ctxQ2 = WithToolCall(ctxQ2, "call_q", "questionary")
	ans, err := broker.Request(ctxQ2, q, "")
	if err != nil {
		t.Fatalf("replay question: %v", err)
	}
	if len(ans.Answers) != 2 || ans.Answers[0] != "prod" || ans.Answers[1] != "main" {
		t.Fatalf("unexpected answers %v", ans.Answers)
	}

	ctxT2, _, _ := startRun("t2", "r2")
	ctxT2 = WithToolCall(ctxT2, "call_t", "git_reset")
	ansTyped, err := broker.Request(ctxT2, typed, "")
	if err != nil {
		t.Fatalf("replay typed confirm: %v", err)
	}
	if ansTyped.Text != "RESET" {
		t.Fatalf("unexpected text %q", ansTyped.Text)
	}
}

func TestInterruptCancelledDeniesAndAbandonsQuestions(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)

	// An approval abandoned by the client denies.
	ctx, _, _ := startRun("t1", "r1")
	ctx = WithToolCall(ctx, "call_1", "write")
	if _, err := broker.Request(ctx, approval("Write file: /tmp/a"), ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	id := RunInterrupted(ctx).Interrupts[0].ID
	if err := broker.Resume("t1", []ResumeEntry{{InterruptID: id, Status: ResumeStatusCancelled}}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	ctx2, _, _ := startRun("t1", "r2")
	ctx2 = WithToolCall(ctx2, "call_1", "write")
	ans, err := broker.Request(ctx2, approval("Write file: /tmp/a"), "")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if ans.Choice == nil || *ans.Choice != 1 || ans.Remember {
		t.Fatalf("a cancelled interrupt must deny, got %+v", ans)
	}

	// A cancelled questionary is an error for the tool.
	ctxQ, _, _ := startRun("t2", "r1")
	ctxQ = WithToolCall(ctxQ, "call_q", "questionary")
	q := InteractionRequest{Kind: KindQuestion, Questions: []InteractionQuestion{{Question: "Which?"}}}
	if _, err := broker.Request(ctxQ, q, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	qID := RunInterrupted(ctxQ).Interrupts[0].ID
	if err := broker.Resume("t2", []ResumeEntry{{InterruptID: qID, Status: ResumeStatusCancelled}}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	ctxQ2, _, _ := startRun("t2", "r2")
	ctxQ2 = WithToolCall(ctxQ2, "call_q", "questionary")
	if _, err := broker.Request(ctxQ2, q, ""); !errors.Is(err, ErrInterruptAbandoned) {
		t.Fatalf("expected ErrInterruptAbandoned, got %v", err)
	}
}

func TestInterruptResumeRejectsBadRequests(t *testing.T) {
	broker := NewBroker(50 * time.Millisecond)
	broker.SetInterruptMode(true)

	ctx, _, _ := startRun("t1", "r1")
	ctx = WithToolCall(ctx, "call_1", "write")
	if _, err := broker.Request(ctx, approval("Write file: /tmp/a"), ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	id := RunInterrupted(ctx).Interrupts[0].ID

	if err := broker.Resume("t1", []ResumeEntry{{InterruptID: "int_nope", Status: ResumeStatusResolved}}); !errors.Is(err, ErrInterruptUnknown) {
		t.Fatalf("expected ErrInterruptUnknown, got %v", err)
	}
	if err := broker.Resume("t2", []ResumeEntry{{InterruptID: id, Status: ResumeStatusResolved}}); !errors.Is(err, ErrInterruptUnknown) {
		t.Fatalf("a resume from another thread must be rejected, got %v", err)
	}
	if err := broker.Resume("t1", []ResumeEntry{{InterruptID: id, Status: "maybe"}}); !errors.Is(err, ErrInterruptStatus) {
		t.Fatalf("expected ErrInterruptStatus, got %v", err)
	}
	if err := broker.Resume("t1", []ResumeEntry{{Status: ResumeStatusResolved}}); !errors.Is(err, ErrInterruptUnknown) {
		t.Fatalf("expected ErrInterruptUnknown for a missing id, got %v", err)
	}
	if err := broker.Resume("t1", []ResumeEntry{{InterruptID: id, Status: ResumeStatusResolved, Payload: json.RawMessage(`{`)}}); !errors.Is(err, ErrInterruptPayload) {
		t.Fatalf("expected ErrInterruptPayload, got %v", err)
	}

	// Rule 7: a resume past expiresAt fails, and the expired interrupt stops
	// blocking new input on the thread.
	time.Sleep(60 * time.Millisecond)
	if open := broker.OpenInterrupts("t1"); len(open) != 0 {
		t.Fatalf("an expired interrupt must not stay open, got %+v", open)
	}
	if err := broker.Resume("t1", []ResumeEntry{{InterruptID: id, Status: ResumeStatusResolved}}); !errors.Is(err, ErrInterruptExpired) {
		t.Fatalf("expected ErrInterruptExpired, got %v", err)
	}
}

func TestInterruptRememberAppliesToTheThread(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)

	ctx, _, _ := startRun("t1", "r1")
	ctx = WithToolCall(ctx, "call_1", "write")
	key := "approval|file|/tmp/remembered.txt"
	if _, err := broker.Request(ctx, approval("Write file: /tmp/remembered.txt"), key); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	id := RunInterrupted(ctx).Interrupts[0].ID
	if err := broker.Resume("t1", []ResumeEntry{{
		InterruptID: id,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"approved":true,"remember":true}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// The same target on the same thread no longer asks: another tool call gets
	// the remembered grant straight away.
	ctx2, _, _ := startRun("t1", "r2")
	ctx2 = WithToolCall(ctx2, "call_2", "write")
	ans, err := broker.Request(ctx2, approval("Write file: /tmp/remembered.txt"), key)
	if err != nil {
		t.Fatalf("remembered request: %v", err)
	}
	if ans.Choice == nil || *ans.Choice != 0 || !ans.Remember {
		t.Fatalf("unexpected remembered answer %+v", ans)
	}

	// Another thread is not affected.
	ctx3, _, _ := startRun("t3", "r1")
	ctx3 = WithToolCall(ctx3, "call_3", "write")
	if _, err := broker.Request(ctx3, approval("Write file: /tmp/remembered.txt"), key); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("the grant must be per thread, got %v", err)
	}

	// Forget drops the grant and the interrupt records.
	broker.Forget("t1")
	if open := broker.OpenInterrupts("t1"); len(open) != 0 {
		t.Fatalf("unexpected open interrupts after Forget: %+v", open)
	}
	if err := broker.Resume("t1", []ResumeEntry{{InterruptID: id, Status: ResumeStatusResolved}}); !errors.Is(err, ErrInterruptUnknown) {
		t.Fatalf("expected the records to be gone, got %v", err)
	}
}

func TestInterruptRunReportsEveryRaisedInterrupt(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)

	ctx, _, _ := startRun("t1", "r1")
	for i, title := range []string{"Write file: /tmp/a", "Write file: /tmp/b"} {
		callCtx := WithToolCall(ctx, "call_"+string(rune('1'+i)), "write")
		if _, err := broker.Request(callCtx, approval(title), ""); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("expected ErrInterrupted, got %v", err)
		}
	}
	ie := RunInterrupted(ctx)
	if len(ie.Interrupts) != 2 {
		t.Fatalf("the run must report every interrupt it raised, got %d", len(ie.Interrupts))
	}
	ids := []string{ie.Interrupts[0].ToolCallID, ie.Interrupts[1].ToolCallID}
	if strings.Join(ids, ",") != "call_1,call_2" {
		t.Fatalf("unexpected tool call ids: %v", ids)
	}
	if open := broker.OpenInterrupts("t1"); len(open) != 2 {
		t.Fatalf("expected two open interrupts, got %d", len(open))
	}
}

func TestInterruptModeOffKeepsLevelOne(t *testing.T) {
	broker := NewBroker(80 * time.Millisecond)
	if broker.InterruptMode() {
		t.Fatal("the interrupt lifecycle must be opt-in")
	}

	ctx, buf, _ := startRun("t1", "r1")
	ans, err := broker.Request(ctx, approval("Write file: /tmp/level1"), "")
	if !errors.Is(err, ErrInteractionTimeout) {
		t.Fatalf("level 1 must park and time out, got %v (%+v)", err, ans)
	}

	// Level 1 asks through the control-plane CUSTOM event and reports the
	// resolution, as before.
	var asked, resolved bool
	for _, ev := range decodeSSE(t, buf.String()) {
		if ev["type"] != EvCustom {
			continue
		}
		switch ev["name"] {
		case CustomInteractionRequired:
			asked = true
		case CustomInteractionResolved:
			resolved = true
		}
	}
	if !asked || !resolved {
		t.Fatalf("expected the level-1 control-plane events, got %s", buf.String())
	}
}

func TestRunFinishedOutcomeShapes(t *testing.T) {
	success, err := json.Marshal(NewRunFinished("t1", "r1", nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(success), `"outcome":{"type":"success"}`) {
		t.Fatalf("a completed run must declare the success outcome: %s", success)
	}

	intr := Interrupt{
		ID:         "int_1",
		Reason:     ReasonToolCall,
		Message:    "Write file: a.txt",
		ToolCallID: "call_1",
		ExpiresAt:  "2026-01-01T00:00:00Z",
		Metadata:   map[string]interface{}{"fyc": approval("Write file: a.txt")},
	}
	raw, err := json.Marshal(NewRunFinishedInterrupt("t1", "r1", nil, []Interrupt{intr}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var ev struct {
		Type    string `json:"type"`
		Outcome struct {
			Type       string      `json:"type"`
			Interrupts []Interrupt `json:"interrupts"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != EvRunFinished || ev.Outcome.Type != OutcomeInterrupt {
		t.Fatalf("unexpected RUN_FINISHED outcome: %s", raw)
	}
	if len(ev.Outcome.Interrupts) != 1 || ev.Outcome.Interrupts[0].ID != "int_1" {
		t.Fatalf("unexpected interrupts: %s", raw)
	}
	// The fyc payload must survive the JSON round trip with the level-1 field
	// names, so that a client can reuse its level-1 rendering.
	for _, want := range []string{`"toolCallId":"call_1"`, `"reason":"tool_call"`, `"expiresAt":"2026-01-01T00:00:00Z"`, `"interactionId"`, `"allowIndex":0`, `"title":"Write file: a.txt"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}

func TestMessagesSnapshotShape(t *testing.T) {
	raw, err := json.Marshal(NewMessagesSnapshot([]Message{
		{ID: "m1", Role: "user", Content: "write a file"},
		{ID: "m2", Role: "assistant", ToolCalls: []InputToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: InputToolFunction{Name: "write", Arguments: `{"path":"a.txt"}`},
		}}},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"type":"MESSAGES_SNAPSHOT"`,
		`"role":"user"`,
		`"type":"function"`,
		`"arguments":"{\"path\":\"a.txt\"}"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}

func TestInterruptReplayHandlesASecondDecisionInTheSameCall(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)

	// A single tool call (git_reset) asks twice: the git tier, then a typed
	// confirmation. Both must survive the replay of the call.
	tier := approval("Git write permission:\nreset --hard")
	typed := InteractionRequest{Kind: KindTypedConfirm, Title: "Discard everything", Expect: "RESET"}

	ctx, _, _ := startRun("t1", "r1")
	ctx = WithToolCall(ctx, "call_9", "git_reset")
	if _, err := broker.Request(ctx, tier, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	tierID := RunInterrupted(ctx).Interrupts[0].ID
	if err := broker.Resume("t1", []ResumeEntry{{
		InterruptID: tierID,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"approved":true}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// Resumed run: the tier answer is reused (not consumed) and the typed
	// confirmation raises a second interrupt.
	ctx2, _, _ := startRun("t1", "r2")
	ctx2 = WithToolCall(ctx2, "call_9", "git_reset")
	if _, err := broker.Request(ctx2, tier, ""); err != nil {
		t.Fatalf("the tier answer must be reused on replay: %v", err)
	}
	if _, err := broker.Request(ctx2, typed, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected a second interrupt for the typed confirmation, got %v", err)
	}
	typedID := RunInterrupted(ctx2).Interrupts[0].ID
	if typedID == tierID {
		t.Fatal("the two decisions must have distinct interrupt ids")
	}
	if err := broker.Resume("t1", []ResumeEntry{{
		InterruptID: typedID,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"text":"RESET"}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// Second replay: both answers are available, nothing is left open.
	ctx3, _, _ := startRun("t1", "r3")
	ctx3 = WithToolCall(ctx3, "call_9", "git_reset")
	if _, err := broker.Request(ctx3, tier, ""); err != nil {
		t.Fatalf("tier replay: %v", err)
	}
	ansTyped, err := broker.Request(ctx3, typed, "")
	if err != nil {
		t.Fatalf("typed replay: %v", err)
	}
	if ansTyped.Text != "RESET" {
		t.Fatalf("unexpected typed answer %q", ansTyped.Text)
	}
	if RunInterrupted(ctx3) != nil {
		t.Fatal("a fully answered call must not suspend the run")
	}
	if open := broker.OpenInterrupts("t1"); len(open) != 0 {
		t.Fatalf("no interrupt must stay open, got %+v", open)
	}
}

func TestInterruptIdentityFallbackWithoutToolCallIDs(t *testing.T) {
	broker := NewBroker(5 * time.Minute)
	broker.SetInterruptMode(true)

	// Providers that do not expose tool-call ids: the requests carry none, so the
	// broker matches them by identity — single use.
	req := approval("Write file: /tmp/no-id.txt")
	ctx, _, _ := startRun("t1", "r1")
	if _, err := broker.Request(ctx, req, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	id := RunInterrupted(ctx).Interrupts[0].ID
	if err := broker.Resume("t1", []ResumeEntry{{
		InterruptID: id,
		Status:      ResumeStatusResolved,
		Payload:     json.RawMessage(`{"approved":true}`),
	}}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	ctx2, _, _ := startRun("t1", "r2")
	ans, err := broker.Request(ctx2, req, "")
	if err != nil {
		t.Fatalf("the answer must be matched by identity: %v", err)
	}
	if ans.Choice == nil || *ans.Choice != 0 {
		t.Fatalf("unexpected answer %+v", ans)
	}

	// A different request (another target) is a new decision.
	if _, err := broker.Request(ctx2, approval("Write file: /tmp/other.txt"), ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("a different request must be asked again, got %v", err)
	}
}
