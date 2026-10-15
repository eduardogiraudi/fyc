package main

import (
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/modes"
)

func visibleToolNames(s *ChatSession) map[string]bool {
	out := map[string]bool{}
	for _, tool := range s.getToolsForAI() {
		out[tool.Function.Name] = true
	}
	return out
}

// TestModeNarrowsToolsNeverReenables covers the mode tool policy end to end: a
// mode only removes tools, a user-disabled tool is never re-enabled, and
// returning to chat restores the full toolset.
func TestModeNarrowsToolsNeverReenables(t *testing.T) {
	session := &ChatSession{AvailableTools: availableTools}
	session.gitInRepo.Store(true)

	// Plain chat exposes the mutating tools.
	base := visibleToolNames(session)
	for _, name := range []string{"write", "replace_anchors", "sh", "read"} {
		if !base[name] {
			t.Fatalf("plain chat must expose %q", name)
		}
	}

	// Debug mode hides the mutating tools, keeps the read-only ones and sh.
	session.CurrentMode = &modes.DebugMode
	dbg := visibleToolNames(session)
	for _, name := range []string{"write", "replace_anchors", "delete", "make_dir", "copy", "move", "rename"} {
		if dbg[name] {
			t.Errorf("debug mode must hide %q", name)
		}
	}
	for _, name := range []string{"read", "grep", "ls", "get_anchors", "context_snapshot", "sh"} {
		if !dbg[name] {
			t.Errorf("debug mode must expose %q", name)
		}
	}

	// A tool the user disabled is never re-enabled by the mode.
	config.SetToolEnabled("grep", false)
	if visibleToolNames(session)["grep"] {
		t.Error("debug mode re-enabled a disabled tool")
	}
	config.SetToolEnabled("grep", true)

	// Deep research exposes only its allowlist, and spawn_subagents stays off
	// because it is disabled by default (never force-enabled).
	session.CurrentMode = &modes.DeepResearchMode
	deep := visibleToolNames(session)
	if !deep["web_search"] {
		t.Error("deep research must expose web_search")
	}
	for _, name := range []string{"read", "write", "sh", "ls", "spawn_subagents"} {
		if deep[name] {
			t.Errorf("deep research must hide %q", name)
		}
	}

	// Tutorial mode is conversation-only and exposes no tools.
	session.CurrentMode = &modes.TutorialMode
	if got := visibleToolNames(session); len(got) != 0 {
		t.Errorf("tutorial mode exposed %d tools, want none", len(got))
	}

	// Back to chat: the full toolset returns.
	session.CurrentMode = nil
	if !visibleToolNames(session)["write"] {
		t.Error("returning to chat must restore the mutating tools")
	}
}

// TestOneShotModeOverride pins the per-turn override: a one-shot prefix mode
// applies for a single turn and its policy disappears when cleared.
func TestOneShotModeOverride(t *testing.T) {
	session := &ChatSession{AvailableTools: availableTools}
	if !visibleToolNames(session)["write"] {
		t.Fatal("precondition: write visible in chat")
	}
	session.turnModeOverride = &modes.DebugMode
	if visibleToolNames(session)["write"] {
		t.Error("the one-shot override must apply the mode policy")
	}
	session.turnModeOverride = nil
	if !visibleToolNames(session)["write"] {
		t.Error("clearing the one-shot override must restore the tools")
	}
}

// TestActiveModePrefersOneShot ensures the transient override wins over the
// persistent /mode selection.
func TestActiveModePrefersOneShot(t *testing.T) {
	session := &ChatSession{CurrentMode: &modes.PlanMode}
	if session.activeMode() != &modes.PlanMode {
		t.Error("activeMode must return the persistent mode when no override is set")
	}
	session.turnModeOverride = &modes.DebugMode
	if session.activeMode() != &modes.DebugMode {
		t.Error("activeMode must prefer the one-shot override")
	}
}

// TestPersistentModeInstructionsInjectedOnce pins the context-inflation fix: a
// persistent /mode injects its instructions only on the first turn, while a
// one-shot prefix mode injects on every turn it is used. Clearing the flag
// (done when the mode is (re)selected) makes a persistent mode inject again.
func TestPersistentModeInstructionsInjectedOnce(t *testing.T) {
	session := &ChatSession{}

	if !session.claimModeInstructions(&modes.DebugMode, false) {
		t.Fatal("the first turn of a persistent mode must inject its instructions")
	}
	if session.claimModeInstructions(&modes.DebugMode, false) {
		t.Error("a persistent mode must not re-inject its instructions on every turn")
	}

	// A one-shot prefix mode injects on every turn it is used.
	if !session.claimModeInstructions(&modes.DebugMode, true) {
		t.Error("a one-shot mode must inject on its first turn")
	}
	if !session.claimModeInstructions(&modes.DebugMode, true) {
		t.Error("a one-shot mode must inject again on the next turn")
	}

	// Re-selecting the mode resets the flag, so the instructions are re-appended.
	session.persistentModeInjected = ""
	if !session.claimModeInstructions(&modes.DebugMode, false) {
		t.Error("re-selecting a persistent mode must inject its instructions again")
	}
}

func TestShToolTimeoutSchema(t *testing.T) {
	for _, tool := range availableTools {
		if tool.Function.Name != "sh" {
			continue
		}
		property, ok := tool.Function.Parameters.Properties["timeout_seconds"]
		if !ok || property.Type != "integer" {
			t.Fatal("sh must expose the optional integer timeout_seconds parameter")
		}
		for _, name := range tool.Function.Parameters.Required {
			if name == "timeout_seconds" {
				t.Fatal("timeout_seconds must remain optional")
			}
		}
		return
	}
	t.Fatal("sh tool missing")
}
