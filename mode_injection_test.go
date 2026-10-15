package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/modes"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// TestClearResetsModeAndInjectionFlag covers the /clear fix: clearing the
// context must drop the persistent mode and its injection flag, otherwise the
// mode's tool policy would linger while its instructions are never re-injected.
func TestClearResetsModeAndInjectionFlag(t *testing.T) {
	sink := &recordSink{}
	s := &ChatSession{
		Sink:                   sink,
		CurrentMode:            &modes.DebugMode,
		persistentModeInjected: modes.DebugMode.Name,
		ChatHistory: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "[INSTRUCTIONS]: ..."},
			{Role: "user", Content: "hello"},
		},
	}

	handled, _ := handleSlashCommand("/clear", s)
	if !handled {
		t.Fatal("/clear was not handled")
	}
	if s.persistentModeInjected != "" {
		t.Errorf("persistentModeInjected = %q, want empty", s.persistentModeInjected)
	}
	if s.CurrentMode != nil {
		t.Errorf("CurrentMode = %v, want nil", s.CurrentMode)
	}
	for _, m := range s.ChatHistory {
		if m.Role != "system" {
			t.Errorf("non-system message survived /clear: %+v", m)
		}
	}
}

func TestClearInvalidatesAnchors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tools.ResetAnchorState)
	call := types.ToolCall{Function: types.ToolCallFunction{
		Name: "get_anchors", Arguments: map[string]interface{}{"path": "file.txt"},
	}}
	annotated := tools.ExecuteToolWithSpinner(context.Background(), call, nil, root, nil).Text
	anchor, _, ok := strings.Cut(annotated, "\u00a7")
	if !ok {
		t.Fatalf("get_anchors: %s", annotated)
	}
	s := &ChatSession{Sink: &recordSink{}}
	handleSlashCommand("/clear", s)
	call.Function = types.ToolCallFunction{Name: "replace_anchors", Arguments: map[string]interface{}{
		"path": "file.txt", "start_anchor": anchor, "end_anchor": anchor, "replacement": "agent",
	}}
	res := tools.ExecuteToolWithSpinner(context.Background(), call, nil, root, nil).Text
	if !strings.Contains(res, "file not loaded") {
		t.Fatalf("old anchors survived /clear: %s", res)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original\n" {
		t.Fatalf("file changed after /clear: %q, %v", data, err)
	}
}

// TestStartNewChatResetsModeAndFlag covers the /sessions new path: a new chat
// starts as plain chat, dropping the runtime mode and its injection flag.
func TestStartNewChatResetsModeAndFlag(t *testing.T) {
	testWorkspace(t)
	s := &ChatSession{
		RestrictedRoot:         "/w",
		CurrentMode:            &modes.DebugMode,
		persistentModeInjected: modes.DebugMode.Name,
		ChatHistory: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "[INSTRUCTIONS]: ..."},
			{Role: "user", Content: "hello"},
		},
	}

	if err := startNewChat(s); err != nil {
		t.Fatalf("startNewChat: %v", err)
	}
	if s.persistentModeInjected != "" {
		t.Errorf("persistentModeInjected = %q, want empty", s.persistentModeInjected)
	}
	if s.CurrentMode != nil {
		t.Errorf("CurrentMode = %v, want nil", s.CurrentMode)
	}
	if len(s.ChatHistory) != 1 || s.ChatHistory[0].Role != "system" {
		t.Errorf("history not reset: %+v", s.ChatHistory)
	}
}

// TestSwitchChatSessionResetsModeAndFlag covers /sessions switch: the loaded
// conversation starts as plain chat, dropping the runtime mode and its
// injection flag.
func TestSwitchChatSessionResetsModeAndFlag(t *testing.T) {
	testWorkspace(t)

	a := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{
		{Role: "system", Content: "sys"}, {Role: "user", Content: "alpha"},
	}}
	if err := saveChatSession(a); err != nil {
		t.Fatal(err)
	}

	b := &ChatSession{
		RestrictedRoot:         "/w",
		CurrentMode:            &modes.DebugMode,
		persistentModeInjected: modes.DebugMode.Name,
		ChatHistory: []types.Message{
			{Role: "system", Content: "sys"}, {Role: "user", Content: "beta"},
		},
	}
	if err := saveChatSession(b); err != nil {
		t.Fatal(err)
	}

	if err := switchChatSession(b, a.SessionID); err != nil {
		t.Fatalf("switchChatSession: %v", err)
	}
	if b.persistentModeInjected != "" {
		t.Errorf("persistentModeInjected = %q, want empty", b.persistentModeInjected)
	}
	if b.CurrentMode != nil {
		t.Errorf("CurrentMode = %v, want nil", b.CurrentMode)
	}
	if b.SessionID != a.SessionID {
		t.Errorf("SessionID = %q, want %q", b.SessionID, a.SessionID)
	}
}

// TestClaimModeInstructionsReinjectsAfterFlagReset documents the contract the
// fixes rely on: once the injection flag is cleared, an active persistent mode
// claims its instructions again.
func TestClaimModeInstructionsReinjectsAfterFlagReset(t *testing.T) {
	s := &ChatSession{CurrentMode: &modes.DebugMode}
	if !s.claimModeInstructions(&modes.DebugMode, false) {
		t.Fatal("first claim should inject")
	}
	if s.claimModeInstructions(&modes.DebugMode, false) {
		t.Fatal("second claim must not re-inject")
	}
	s.persistentModeInjected = ""
	if !s.claimModeInstructions(&modes.DebugMode, false) {
		t.Fatal("claim after reset must inject again")
	}
}
