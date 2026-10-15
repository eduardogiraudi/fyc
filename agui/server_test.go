package agui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// fakeRunner emits a scripted turn: one text message, one tool call, stats.
type fakeRunner struct{}

func (fakeRunner) Run(_ context.Context, _ RunRequest, sink types.OutputSink) error {
	sink.Reset()
	sink.WriteText("Ciao")
	sink.WriteNewline()
	if ts, ok := sink.(types.ToolEventSink); ok {
		ts.ToolCallStarted("c1", "read", map[string]interface{}{"path": "a.go"})
		ts.ToolCallFinished("c1", "read", "content")
	}
	sink.WriteStats("prov", "tokens")
	return nil
}

func newTestServer() *httptest.Server {
	return httptest.NewServer(NewServer(Options{Token: "s3cr3t"}, fakeRunner{}).Handler())
}

func TestHealthz(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRunRequiresToken(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// missing token
	resp, err := http.Post(ts.URL+"/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// wrong token
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer nope")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", resp.StatusCode)
	}
}

func TestRunEventSequence(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	body := `{"threadId":"t1","runId":"r1","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", ct)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	events := decodeSSE(t, string(raw))
	got := strings.Join(eventTypes(events), ",")
	want := strings.Join([]string{
		EvRunStarted,
		EvTextMessageStart, EvTextMessageContent, EvTextMessageEnd,
		EvToolCallStart, EvToolCallArgs, EvToolCallEnd, EvToolCallResult,
		EvRunFinished,
	}, ",")
	if got != want {
		t.Fatalf("unexpected sequence:\n got: %s\nwant: %s", got, want)
	}

	finished := events[len(events)-1]
	if finished["threadId"] != "t1" || finished["runId"] != "r1" {
		t.Fatalf("unexpected RUN_FINISHED: %+v", finished)
	}
	md, _ := finished["metadata"].(map[string]interface{})
	if md["tokenStats"] != "tokens" {
		t.Fatalf("expected token stats metadata, got %+v", md)
	}
}

func TestRunRejectsInvalidJSON(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(`{not json`))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
