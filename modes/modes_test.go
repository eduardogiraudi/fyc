package modes

import (
	"testing"

	"fuckyourcli/types"
)

func toolNames(tools []types.Tool) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		out[t.Function.Name] = true
	}
	return out
}

func fakeTools(names ...string) []types.Tool {
	out := make([]types.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, types.Tool{Function: types.ToolFunction{Name: n}})
	}
	return out
}

// TestPlanAndDebugDenyWriteTools pins the "no write tools, but sh stays" policy.
func TestPlanAndDebugDenyWriteTools(t *testing.T) {
	for _, m := range []*Mode{&PlanMode, &DebugMode} {
		for _, denied := range []string{"write", "replace_anchors", "delete", "make_dir", "copy", "move", "rename"} {
			if m.ExposesTool(denied) {
				t.Errorf("%s must deny workspace-mutating tool %q", m.Name, denied)
			}
		}
		for _, allowed := range []string{"read", "grep", "ls", "get_anchors", "context_snapshot", "sh", "web_search", "spawn_subagents"} {
			if !m.ExposesTool(allowed) {
				t.Errorf("%s must expose %q", m.Name, allowed)
			}
		}
	}
}

// TestDeepResearchAllowlist pins the exclusive allowlist of deep research.
func TestDeepResearchAllowlist(t *testing.T) {
	m := &DeepResearchMode
	for _, allowed := range []string{"web_search", "spawn_subagents"} {
		if !m.ExposesTool(allowed) {
			t.Errorf("deep research must expose %q", allowed)
		}
	}
	for _, denied := range []string{"read", "write", "sh", "grep", "questionary"} {
		if m.ExposesTool(denied) {
			t.Errorf("deep research must hide %q", denied)
		}
	}
}

// TestFilterToolsNarrowsOnly proves the mode layer only ever removes tools.
func TestFilterToolsNarrowsOnly(t *testing.T) {
	all := fakeTools("read", "write", "sh", "web_search")

	seen := toolNames((&DebugMode).FilterTools(all))
	if seen["write"] {
		t.Error("FilterTools must remove denied tools")
	}
	if !seen["read"] || !seen["sh"] {
		t.Error("FilterTools must keep allowed tools")
	}

	// A mode with no policy and a nil mode leave the input untouched.
	if got := (&Mode{Name: "Chat"}).FilterTools(all); len(got) != len(all) {
		t.Errorf("a policy-less mode changed the tool count: %d", len(got))
	}
	if got := (*Mode)(nil).FilterTools(all); len(got) != len(all) {
		t.Errorf("a nil mode must return the input untouched, got %d", len(got))
	}
}

func TestNilModeExposesEverything(t *testing.T) {
	if !(*Mode)(nil).ExposesTool("anything") {
		t.Error("a nil mode must expose every tool")
	}
}

func TestTutorialModeDisablesAllTools(t *testing.T) {
	m := &TutorialMode
	for _, name := range []string{"read", "write", "sh", "web_search", "questionary"} {
		if m.ExposesTool(name) {
			t.Errorf("tutorial mode must hide %q", name)
		}
	}
	if got := m.FilterTools(fakeTools("read", "write")); len(got) != 0 {
		t.Errorf("tutorial mode exposed %d tools, want none", len(got))
	}
	if tutorialDocs == "" {
		t.Error("tutorial documentation must be embedded")
	}
}

// TestGetModeResolvesPrefixStem pins that "/mode deep" resolves Deep Research,
// not only its full name.
func TestGetModeResolvesPrefixStem(t *testing.T) {
	cases := map[string]string{
		"plan":          "Plan",
		"debug":         "Debug",
		"deep":          "Deep Research",
		"deep research": "Deep Research",
		"tutorial":      "Tutorial",
		"Deep Research": "Deep Research",
	}
	for in, want := range cases {
		got := GetMode(in)
		if got == nil {
			t.Errorf("GetMode(%q) = nil, want %q", in, want)
			continue
		}
		if got.Name != want {
			t.Errorf("GetMode(%q) = %q, want %q", in, got.Name, want)
		}
	}
	if GetMode("nope") != nil {
		t.Error("GetMode must return nil for an unknown mode")
	}
}
