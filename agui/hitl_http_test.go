package agui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fuckyourcli/types"
)

// hitlRunner asks the client to approve a write, then reports the outcome. It
// only uses the runner contract: the broker reaches the run through the context
// the HTTP handler attached.
type hitlRunner struct {
	broker *Broker
	asked  int
}

func (r *hitlRunner) Run(ctx context.Context, _ RunRequest, sink types.OutputSink) error {
	r.asked++
	ans, err := r.broker.Request(ctx, InteractionRequest{
		Kind:       "approval",
		Title:      "Write file: /tmp/approved.txt",
		Options:    []string{"Allow", "Deny"},
		AllowIndex: 0,
		DenyIndex:  1,
	}, "approval|Write file: /tmp/approved.txt")
	if err != nil {
		return err
	}
	verdict := "denied"
	if ans.Choice != nil && *ans.Choice == 0 {
		verdict = "approved"
	}
	sink.WriteText(verdict)
	sink.WriteNewline()
	return nil
}

// TestInteractionRoundTripOverHTTP drives the full level-1 flow: the run parks on
// a pending interaction, the client answers on the resolve endpoint while the SSE
// stream is still open, and the run completes.
func TestInteractionRoundTripOverHTTP(t *testing.T) {
	broker := NewBroker(5 * time.Second)
	runner := &hitlRunner{broker: broker}
	ts := httptest.NewServer(NewServer(Options{Token: "s3cr3t", Interactions: broker}, runner).Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/",
		strings.NewReader(`{"threadId":"t1","runId":"r1","messages":[{"role":"user","content":"go"}]}`))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer resp.Body.Close()

	var (
		interactionID string
		verdict       string
		finished      bool
	)
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
		switch ev["type"] {
		case EvCustom:
			if ev["name"] != CustomInteractionRequired {
				t.Fatalf("unexpected CUSTOM event: %+v", ev)
			}
			payload, _ := ev["value"].(map[string]interface{})
			interactionID, _ = payload["interactionId"].(string)
			if interactionID == "" || payload["runId"] != "r1" {
				t.Fatalf("unexpected interaction payload: %+v", payload)
			}
			if n := broker.Pending(); n != 1 {
				t.Fatalf("expected the interaction to be parked, got %d pending", n)
			}
			// Answer while the run is still parked.
			answerURL := fmt.Sprintf("%s/runs/r1/interactions/%s", ts.URL, interactionID)
			areq, _ := http.NewRequest(http.MethodPost, answerURL, strings.NewReader(`{"choice":0}`))
			areq.Header.Set("Authorization", "Bearer s3cr3t")
			aresp, err := http.DefaultClient.Do(areq)
			if err != nil {
				t.Fatalf("answer: %v", err)
			}
			aresp.Body.Close()
			if aresp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204 from the resolve endpoint, got %d", aresp.StatusCode)
			}
		case EvTextMessageContent:
			if d, _ := ev["delta"].(string); d != "" {
				verdict += d
			}
		case EvRunFinished:
			finished = true
		}
		if finished {
			break
		}
	}

	if interactionID == "" {
		t.Fatal("no interaction was requested")
	}
	if !finished {
		t.Fatalf("the run never finished (verdict=%q)", verdict)
	}
	if verdict != "approved" {
		t.Fatalf("expected the approval to reach the tool, got %q", verdict)
	}
	if n := broker.Pending(); n != 0 {
		t.Fatalf("expected no interaction left parked, got %d", n)
	}
}

func TestResolveEndpointRejectsUnknownInteraction(t *testing.T) {
	broker := NewBroker(time.Second)
	ts := httptest.NewServer(NewServer(Options{Token: "s3cr3t", Interactions: broker}, &hitlRunner{broker: broker}).Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/runs/r1/interactions/int_missing", strings.NewReader(`{"choice":0}`))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestResolveEndpointRequiresToken(t *testing.T) {
	broker := NewBroker(time.Second)
	ts := httptest.NewServer(NewServer(Options{Token: "s3cr3t", Interactions: broker}, &hitlRunner{broker: broker}).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/runs/r1/interactions/int_x", "application/json", strings.NewReader(`{"choice":0}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
