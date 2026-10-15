package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fuckyourcli/agui"
	"fuckyourcli/config"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// ── level-1 human-in-the-loop, real tool path ────────────────────────────────
//
// These tests exercise the whole chain: handleToolCalls → tools dispatch →
// permission prompt → hitlInteractor → agui.Broker → SSE sink. No HTTP server is
// involved (agui's own tests cover the transport); the client answer is injected
// straight into the broker.

// lockedBuffer is the run's SSE destination; the tool goroutine writes to it
// while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) events(t *testing.T) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(l.String(), "\n") {
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

// customEvents returns the payloads of every CUSTOM event with the given name.
func (l *lockedBuffer) customEvents(t *testing.T, name string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, ev := range l.events(t) {
		if ev["type"] == agui.EvCustom && ev["name"] == name {
			payload, _ := ev["value"].(map[string]interface{})
			out = append(out, payload)
		}
	}
	return out
}

// waitInteraction waits until the run emits a fyc.interaction_required event.
func (l *lockedBuffer) waitInteraction(t *testing.T) (string, map[string]interface{}) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range l.customEvents(t, agui.CustomInteractionRequired) {
			if id, _ := p["interactionId"].(string); id != "" {
				return id, p
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no %s event:\n%s", agui.CustomInteractionRequired, l.String())
	return "", nil
}

// toolResult returns the TOOL_CALL_RESULT content of the run.
func (l *lockedBuffer) toolResult(t *testing.T) string {
	t.Helper()
	for _, ev := range l.events(t) {
		if ev["type"] == agui.EvToolCallResult {
			s, _ := ev["content"].(string)
			return s
		}
	}
	t.Fatalf("no TOOL_CALL_RESULT event:\n%s", l.String())
	return ""
}

// startToolCall installs the HITL interactor, runs one tool call on its own
// goroutine (it parks on the interaction) and returns the run's sink buffer.
func startToolCall(t *testing.T, broker *agui.Broker, hitl bool, dir string, call types.ToolCall) (*lockedBuffer, chan struct{}) {
	t.Helper()
	tools.SetInteractor(newHITLInteractor(broker, "deny", hitl))
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

	out := &lockedBuffer{}
	sink := agui.NewSink(out, nil, "t1", "r1")
	s := &ChatSession{
		RestrictedRoot: dir,
		Sink:           sink,
		RunCtx:         agui.WithRun(context.Background(), sink, "t1", "r1"),
	}
	done := make(chan struct{})
	go func() {
		handleToolCalls(s, sink, []types.ToolCall{call}, true)
		close(done)
	}()
	return out, done
}

func writeCall(id, path, content string) types.ToolCall {
	return types.ToolCall{
		ID: id,
		Function: types.ToolCallFunction{
			Name:      "write",
			Arguments: map[string]interface{}{"path": path, "content": content},
		},
	}
}

func TestHITLApprovalRoundTripWritesFile(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Second)
	out, done := startToolCall(t, broker, true, dir, writeCall("c1", "hello.txt", "hi there"))

	// The client sees the approval request while the tool is parked.
	id, payload := out.waitInteraction(t)
	if payload["kind"] != "approval" {
		t.Fatalf("expected an approval request, got %+v", payload)
	}
	title, _ := payload["title"].(string)
	if !strings.Contains(title, "Write file:") || !strings.Contains(title, "hello.txt") {
		t.Fatalf("unexpected title %q", title)
	}
	options, _ := payload["options"].([]interface{})
	if len(options) < 3 {
		t.Fatalf("expected the full option list (Allow/Deny/Allow always), got %+v", options)
	}

	allow := 0
	if err := broker.Resolve("r1", id, agui.InteractionAnswer{Choice: &allow, Remember: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the tool call did not resume after the answer")
	}

	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatalf("the approved write did not happen: %v", err)
	}
	if string(data) != "hi there" {
		t.Fatalf("unexpected file content %q", data)
	}
	if res := out.toolResult(t); !strings.Contains(res, "written") {
		t.Fatalf("unexpected tool result %q", res)
	}
}

func TestHITLRemembersTheApprovalForTheThread(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Second)
	path := "hello.txt"

	out, done := startToolCall(t, broker, true, dir, writeCall("c1", path, "first"))
	id, _ := out.waitInteraction(t)
	allow := 0
	if err := broker.Resolve("r1", id, agui.InteractionAnswer{Choice: &allow, Remember: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	<-done

	// Same thread, same target: the second call must not ask again.
	out2, done2 := startToolCall(t, broker, true, dir, writeCall("c2", path, "second"))
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("the remembered approval did not auto-approve the second write")
	}
	if n := len(out2.customEvents(t, agui.CustomInteractionRequired)); n != 0 {
		t.Fatalf("a remembered approval must not ask again, got %d requests", n)
	}
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "second" {
		t.Fatalf("unexpected file content %q", data)
	}
}

func TestHITLDenyBlocksTheTool(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Second)
	out, done := startToolCall(t, broker, true, dir, writeCall("c1", "denied.txt", "nope"))

	id, _ := out.waitInteraction(t)
	deny := 1
	if err := broker.Resolve("r1", id, agui.InteractionAnswer{Choice: &deny}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	<-done

	if _, err := os.Stat(filepath.Join(dir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("a denied write must not create the file (err=%v)", err)
	}
	if res := out.toolResult(t); !strings.Contains(res, "denied") {
		t.Fatalf("unexpected tool result %q", res)
	}
}

func TestHITLTimeoutDeniesTheTool(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(40 * time.Millisecond)
	out, done := startToolCall(t, broker, true, dir, writeCall("c1", "timeout.txt", "nope"))
	out.waitInteraction(t)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the parked interaction never timed out")
	}

	if _, err := os.Stat(filepath.Join(dir, "timeout.txt")); !os.IsNotExist(err) {
		t.Fatalf("a timed-out write must not create the file (err=%v)", err)
	}
	if res := out.toolResult(t); !strings.Contains(res, "denied") {
		t.Fatalf("unexpected tool result %q", res)
	}
	resolved := out.customEvents(t, agui.CustomInteractionResolved)
	if len(resolved) != 1 || resolved[0]["status"] != "timeout" {
		t.Fatalf("expected a timeout notification for the client, got %+v", resolved)
	}
}

func TestHITLDisabledKeepsTheDeterministicPolicy(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Second)
	out, done := startToolCall(t, broker, false, dir, writeCall("c1", "nope.txt", "nope"))

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("with --hitl=false the call must not park")
	}

	if n := len(out.customEvents(t, agui.CustomInteractionRequired)); n != 0 {
		t.Fatalf("no interaction may be requested when HITL is disabled, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.txt")); !os.IsNotExist(err) {
		t.Fatalf("the default policy must deny the write (err=%v)", err)
	}
	if res := out.toolResult(t); !strings.Contains(res, "denied") {
		t.Fatalf("unexpected tool result %q", res)
	}
}

func TestHITLUpdateCarriesTheDiff(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("old line\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	broker := agui.NewBroker(5 * time.Second)
	out, done := startToolCall(t, broker, true, dir, writeCall("c1", "existing.txt", "new line\n"))

	id, payload := out.waitInteraction(t)
	if payload["seeChanges"] != true {
		t.Fatalf("expected seeChanges=true for an update, got %+v", payload)
	}
	if payload["diffFilename"] != target {
		t.Fatalf("expected the diff to reference %s, got %+v", target, payload["diffFilename"])
	}
	diff, _ := payload["diff"].([]interface{})
	if len(diff) == 0 {
		t.Fatalf("expected the diff lines in the payload, got %+v", payload)
	}
	stats, _ := payload["diffStats"].(string)
	if !strings.Contains(stats, "+1") || !strings.Contains(stats, "-1") {
		t.Fatalf("expected the diff stats to travel too, got %q", stats)
	}

	allow := 0
	if err := broker.Resolve("r1", id, agui.InteractionAnswer{Choice: &allow}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	<-done

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "new line\n" {
		t.Fatalf("unexpected file content %q", data)
	}
}

// TestHITLAllowsAnotherThreadWhileOneIsParked checks the executeMu mitigation:
// an interaction parked on one thread must not freeze tool execution globally.
func TestHITLAllowsAnotherThreadWhileOneIsParked(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Second)

	// Thread 1 parks on an approval it will never answer quickly.
	out1, done1 := startToolCall(t, broker, true, dir, writeCall("c1", "first.txt", "one"))
	out1.waitInteraction(t)

	// While it is parked, a read-only tool call on another thread must run.
	out2 := &lockedBuffer{}
	sink2 := agui.NewSink(out2, nil, "t2", "r2")
	s2 := &ChatSession{RestrictedRoot: dir, Sink: sink2}
	done2 := make(chan struct{})
	go func() {
		handleToolCalls(s2, sink2, []types.ToolCall{{
			ID:       "c2",
			Function: types.ToolCallFunction{Name: "ls", Arguments: map[string]interface{}{"path": "."}},
		}}, true)
		close(done2)
	}()

	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("a parked interaction must not block other threads")
	}

	// Clean up the parked thread (remember=true avoids the follow-up
	// "Remember choice?" prompt).
	allow := 0
	if err := broker.Resolve("r1", out1.lastInteractionID(t), agui.InteractionAnswer{Choice: &allow, Remember: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	<-done1
}

// lastInteractionID returns the id of the most recent interaction request.
func (l *lockedBuffer) lastInteractionID(t *testing.T) string {
	t.Helper()
	reqs := l.customEvents(t, agui.CustomInteractionRequired)
	if len(reqs) == 0 {
		t.Fatal("no interaction was requested")
	}
	id, _ := reqs[len(reqs)-1]["interactionId"].(string)
	return id
}
