package agui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fuckyourcli/types"
)

// syncBuffer is an io.Writer safe to read while another goroutine (the parked
// tool call) keeps writing: the broker emits events concurrently with the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hitlHarness wires a broker to an in-memory SSE sink attached to a context,
// exactly like the HTTP handler does for a real run.
type hitlHarness struct {
	broker *Broker
	sink   *Sink
	out    *syncBuffer
}

func newHITLHarness(timeout time.Duration) *hitlHarness {
	out := &syncBuffer{}
	sink := NewSink(out, nil, "t1", "r1")
	return &hitlHarness{broker: NewBroker(timeout), sink: sink, out: out}
}

func (h *hitlHarness) ctx(parent context.Context) context.Context {
	return WithRun(parent, h.sink, "t1", "r1")
}

// events decodes every SSE frame written so far.
func (h *hitlHarness) events(t *testing.T) []map[string]interface{} {
	t.Helper()
	return decodeSSE(t, h.out.String())
}

// waitInteraction waits for a CUSTOM interaction_required event and returns its
// interaction id and payload.
func (h *hitlHarness) waitInteraction(t *testing.T) (string, map[string]interface{}) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range h.events(t) {
			if ev["type"] != EvCustom || ev["name"] != CustomInteractionRequired {
				continue
			}
			payload, _ := ev["value"].(map[string]interface{})
			id, _ := payload["interactionId"].(string)
			if id != "" {
				return id, payload
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no %s event was emitted:\n%s", CustomInteractionRequired, h.out.String())
	return "", nil
}

func (h *hitlHarness) countEvents(name string) int {
	n := 0
	for _, ev := range decodeSSEAny(h.out.String()) {
		if ev["type"] == EvCustom && ev["name"] == name {
			n++
		}
	}
	return n
}

// decodeSSEAny is decodeSSE without the *testing.T dependency.
func decodeSSEAny(raw string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func approvalRequest() InteractionRequest {
	return InteractionRequest{
		Kind:       "approval",
		Title:      "Write file: /tmp/x.txt",
		Options:    []string{"Allow", "Deny", "Allow always", "Deny with suggestion"},
		AllowIndex: 0,
		DenyIndex:  1,
		DiffStats:  "+1 -0",
	}
}

func TestBrokerRequestWithoutRunReturnsErrNoRun(t *testing.T) {
	h := newHITLHarness(time.Second)
	if _, err := h.broker.Request(context.Background(), approvalRequest(), ""); !errors.Is(err, ErrNoRun) {
		t.Fatalf("expected ErrNoRun, got %v", err)
	}
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("expected no pending interaction, got %d", n)
	}
}

func TestBrokerEmitsRequestAndResumesWithAnswer(t *testing.T) {
	h := newHITLHarness(time.Second)
	ctx := h.ctx(context.Background())

	type result struct {
		ans InteractionAnswer
		err error
	}
	results := make(chan result, 1)
	go func() {
		ans, err := h.broker.Request(ctx, approvalRequest(), "")
		results <- result{ans, err}
	}()

	id, payload := h.waitInteraction(t)
	if payload["runId"] != "r1" || payload["threadId"] != "t1" {
		t.Fatalf("unexpected run identifiers in payload: %+v", payload)
	}
	if payload["kind"] != "approval" || payload["title"] != "Write file: /tmp/x.txt" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload["allowIndex"].(float64) != 0 || payload["denyIndex"].(float64) != 1 {
		t.Fatalf("unexpected indices in payload: %+v", payload)
	}
	if payload["timeoutMs"].(float64) != 1000 {
		t.Fatalf("expected the timeout to travel in the payload, got %+v", payload["timeoutMs"])
	}

	allow := 0
	if err := h.broker.Resolve("r1", id, InteractionAnswer{Choice: &allow, Text: "ok"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := <-results
	if got.err != nil {
		t.Fatalf("request failed: %v", got.err)
	}
	if got.ans.Choice == nil || *got.ans.Choice != 0 || got.ans.Text != "ok" {
		t.Fatalf("unexpected answer: %+v", got.ans)
	}
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("expected the interaction to be unregistered, got %d pending", n)
	}
}

func TestBrokerTimesOutAndReportsItToTheClient(t *testing.T) {
	h := newHITLHarness(30 * time.Millisecond)
	_, err := h.broker.Request(h.ctx(context.Background()), approvalRequest(), "")
	if !errors.Is(err, ErrInteractionTimeout) {
		t.Fatalf("expected ErrInteractionTimeout, got %v", err)
	}
	resolved := decodeSSEAny(h.out.String())
	last := resolved[len(resolved)-1]
	if last["name"] != CustomInteractionResolved {
		t.Fatalf("expected a %s event, got %+v", CustomInteractionResolved, last)
	}
	payload, _ := last["value"].(map[string]interface{})
	if payload["status"] != "timeout" {
		t.Fatalf("expected status=timeout, got %+v", payload)
	}
}

func TestBrokerCancellationDeniesTheInteraction(t *testing.T) {
	h := newHITLHarness(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.broker.Request(h.ctx(ctx), approvalRequest(), "")
		done <- err
	}()

	h.waitInteraction(t)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrInteractionCanceled) {
			t.Fatalf("expected ErrInteractionCanceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the parked interaction did not return after cancellation")
	}
}

func TestBrokerRemembersPerThread(t *testing.T) {
	h := newHITLHarness(time.Second)
	ctx := h.ctx(context.Background())
	key := "approval|Write file: /tmp/x.txt"

	done := make(chan InteractionAnswer, 1)
	errs := make(chan error, 1)
	go func() {
		ans, err := h.broker.Request(ctx, approvalRequest(), key)
		done <- ans
		errs <- err
	}()
	id, _ := h.waitInteraction(t)
	allow := 0
	if err := h.broker.Resolve("r1", id, InteractionAnswer{Choice: &allow, Remember: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if ans := <-done; !ans.Remember {
		t.Fatalf("expected the remember flag to be preserved, got %+v", ans)
	}

	before := h.countEvents(CustomInteractionRequired)
	second, err := h.broker.Request(ctx, approvalRequest(), key)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if second.Choice == nil || *second.Choice != 0 || !second.Remember {
		t.Fatalf("expected an auto-approved answer, got %+v", second)
	}
	if after := h.countEvents(CustomInteractionRequired); after != before {
		t.Fatalf("a remembered approval must not ask again (%d → %d events)", before, after)
	}

	// Another thread has its own memory: it must be asked again.
	otherCtx, cancelOther := context.WithCancel(WithRun(context.Background(), h.sink, "t2", "r2"))
	defer cancelOther()
	otherDone := make(chan error, 1)
	go func() {
		_, err := h.broker.Request(otherCtx, approvalRequest(), key)
		otherDone <- err
	}()
	h.waitInteraction(t)
	cancelOther()
	<-otherDone
	if after := h.countEvents(CustomInteractionRequired); after == before {
		t.Fatalf("another thread must not inherit the remembered approval")
	}
}

func TestBrokerResolveRejectsUnknownInteractions(t *testing.T) {
	h := newHITLHarness(time.Second)
	if err := h.broker.Resolve("r1", "int_missing", InteractionAnswer{}); !errors.Is(err, ErrInteractionUnknown) {
		t.Fatalf("expected ErrInteractionUnknown, got %v", err)
	}

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := h.broker.Request(h.ctx(ctx), approvalRequest(), "")
		done <- err
	}()
	id, _ := h.waitInteraction(t)

	// Wrong run id.
	if err := h.broker.Resolve("other-run", id, InteractionAnswer{}); !errors.Is(err, ErrInteractionUnknown) {
		t.Fatalf("expected ErrInteractionUnknown for a mismatching run id, got %v", err)
	}
	// A second answer for the same interaction is refused.
	allow := 0
	if err := h.broker.Resolve("r1", id, InteractionAnswer{Choice: &allow}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := h.broker.Resolve("r1", id, InteractionAnswer{Choice: &allow}); !errors.Is(err, ErrInteractionUnknown) {
		t.Fatalf("expected ErrInteractionUnknown for a duplicate answer, got %v", err)
	}
	<-done
}

func TestBrokerCloseUnblocksParkedRequests(t *testing.T) {
	h := newHITLHarness(time.Minute)
	done := make(chan error, 1)
	go func() {
		_, err := h.broker.Request(h.ctx(context.Background()), approvalRequest(), "")
		done <- err
	}()
	h.waitInteraction(t)
	h.broker.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrBrokerClosed) {
			t.Fatalf("expected ErrBrokerClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the parked interaction")
	}

	// A closed broker degrades to the deterministic policy.
	if _, err := h.broker.Request(h.ctx(context.Background()), approvalRequest(), ""); !errors.Is(err, ErrNoRun) {
		t.Fatalf("expected ErrNoRun after Close, got %v", err)
	}
}

func TestBrokerDiffPayloadTravelsWithTheRequest(t *testing.T) {
	h := newHITLHarness(time.Second)
	req := approvalRequest()
	req.SeeChanges = true
	req.Diff = []types.DiffLine{{Kind: types.DiffAdded, Content: "new line", LineNum: 2}}
	req.DiffFilename = "/tmp/x.txt"

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := h.broker.Request(h.ctx(ctx), req, "")
		done <- err
	}()
	_, payload := h.waitInteraction(t)
	if payload["seeChanges"] != true {
		t.Fatalf("expected seeChanges=true, got %+v", payload)
	}
	if payload["diffFilename"] != "/tmp/x.txt" {
		t.Fatalf("expected the diff filename in the payload, got %+v", payload)
	}
	diff, _ := payload["diff"].([]interface{})
	if len(diff) != 1 {
		t.Fatalf("expected the diff to travel in the payload, got %+v", payload["diff"])
	}
	first, _ := diff[0].(map[string]interface{})
	if first["content"] != "new line" {
		t.Fatalf("unexpected diff line: %+v", first)
	}
	cancel()
	<-done
}
