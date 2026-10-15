package main

import (
	"testing"
	"time"

	"fuckyourcli/agui"
	"fuckyourcli/types"
)

// TestHITLExecCarriesTheDetails checks the "See details" addition end to end on
// the wire: an exec approval emits seeDetails/detailsTitle/details while title
// stays the complete command (a client that ignores the new fields is
// unaffected).
func TestHITLExecCarriesTheDetails(t *testing.T) {
	dir := t.TempDir()
	broker := agui.NewBroker(5 * time.Second)
	defer broker.Close()

	call := types.ToolCall{
		ID: "c1",
		Function: types.ToolCallFunction{
			Name:      "sh",
			Arguments: map[string]interface{}{"command": "echo hello"},
		},
	}
	out, _ := startToolCall(t, broker, true, dir, call)

	_, payload := out.waitInteraction(t)
	if payload["seeDetails"] != true {
		t.Fatalf("expected seeDetails=true for an exec prompt, got %+v", payload)
	}
	if payload["title"] != "Execute:\necho hello" {
		t.Fatalf("title must stay the complete command, got %q", payload["title"])
	}
	if payload["detailsTitle"] != "Execution details" {
		t.Fatalf("expected the details title, got %+v", payload["detailsTitle"])
	}
	details, _ := payload["details"].([]interface{})
	if len(details) == 0 || details[0] != "Command:" {
		t.Fatalf("expected the sandbox envelope in the payload, got %+v", payload["details"])
	}
}
