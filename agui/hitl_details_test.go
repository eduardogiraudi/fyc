package agui

import (
	"context"
	"testing"
	"time"
)

// TestBrokerDetailsPayloadIsAdditive checks the "See details" addition to the
// level-1 payload: the extra fields travel when present, and Title stays the
// complete, authoritative field (a client that ignores seeDetails is unaffected).
func TestBrokerDetailsPayloadIsAdditive(t *testing.T) {
	h := newHITLHarness(time.Second)
	req := approvalRequest()
	req.SeeDetails = true
	req.DetailsTitle = "Execution details"
	req.Details = []string{"Command:", "  echo hi"}

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := h.broker.Request(h.ctx(ctx), req, "")
		done <- err
	}()

	_, payload := h.waitInteraction(t)
	if payload["seeDetails"] != true {
		t.Fatalf("expected seeDetails=true, got %+v", payload)
	}
	if payload["detailsTitle"] != "Execution details" {
		t.Fatalf("expected detailsTitle in the payload, got %+v", payload)
	}
	details, _ := payload["details"].([]interface{})
	if len(details) != 2 || details[0] != "Command:" || details[1] != "  echo hi" {
		t.Fatalf("expected the details to travel in the payload, got %+v", payload["details"])
	}
	if payload["title"] != req.Title {
		t.Fatalf("title must stay complete and verbatim, got %+v", payload["title"])
	}
	if payload["options"] == nil {
		t.Fatalf("the addition must not change the option list, got %+v", payload)
	}

	h.broker.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the parked interaction")
	}
}

// TestBrokerPayloadOmitsDetailsWhenAbsent keeps the addition opt-in: a request
// without details must not grow the payload with empty fields.
func TestBrokerPayloadOmitsDetailsWhenAbsent(t *testing.T) {
	h := newHITLHarness(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = h.broker.Request(h.ctx(ctx), approvalRequest(), "") }()

	_, payload := h.waitInteraction(t)
	for _, k := range []string{"seeDetails", "detailsTitle", "details"} {
		if _, ok := payload[k]; ok {
			t.Fatalf("expected %q to be omitted, got %+v", k, payload)
		}
	}
	h.broker.Close()
}
