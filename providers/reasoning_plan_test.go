package providers

import (
	"encoding/json"
	"testing"

	"fuckyourcli/reasoning"
)

func TestLegacyReasoningIntent(t *testing.T) {
	if got := legacyReasoningIntent(false, "high"); got.Mode != reasoning.ModeDefault {
		t.Fatalf("disabled should map to the defensive default, got %v", got.Mode)
	}
	if got := legacyReasoningIntent(true, ""); got.Mode != reasoning.ModeAdaptive {
		t.Fatalf("enabled with no effort should be adaptive, got %v", got.Mode)
	}
	if got := legacyReasoningIntent(true, "high"); got.Mode != reasoning.ModeEffort || got.Effort != "high" {
		t.Fatalf("enabled+effort should map to ModeEffort high, got %+v", got)
	}
}

func TestApplyReasoningPlanNestsAndDeletes(t *testing.T) {
	// A DeepSeek-style plan: nested toggle plus a top-level effort, and a field
	// that must be dropped.
	plan, err := reasoning.Translate(
		reasoning.Intent{Mode: reasoning.ModeOff},
		reasoning.Profile{TogglePath: "thinking.type", ToggleDisabledValue: "disabled"},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan.Omit = append(plan.Omit, "temperature")

	body, err := applyReasoningPlan([]byte(`{"model":"x","temperature":0.7}`), plan)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["temperature"]; ok {
		t.Fatalf("temperature should have been omitted: %s", body)
	}
	thinking, _ := m["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("nested thinking.type not applied: %s", body)
	}
}

func TestApplyReasoningPlanNoop(t *testing.T) {
	in := []byte(`{"model":"x"}`)
	out, err := applyReasoningPlan(in, reasoning.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Fatalf("an empty plan must not rewrite the body: %s", out)
	}
}

func TestAddResponsesReasoning(t *testing.T) {
	body := map[string]any{}
	addResponsesReasoning(body, legacyReasoningIntent(true, "high"), nil, "gpt-5.5", "")
	r, _ := body["reasoning"].(map[string]any)
	if r["effort"] != "high" {
		t.Fatalf("expected reasoning.effort=high, got %v", body)
	}
	if _, ok := body["include"]; !ok {
		t.Fatalf("expected include reasoning.encrypted_content, got %v", body)
	}

	// Thinking off: nothing is sent (no reasoning, no include).
	off := map[string]any{}
	addResponsesReasoning(off, legacyReasoningIntent(false, ""), nil, "gpt-5.5", "")
	if len(off) != 0 {
		t.Fatalf("thinking off must send nothing, got %v", off)
	}
}

// TestAddResponsesReasoningOverrideWins pins the contract of the per-provider
// override: it replaces auto-detection entirely, so a placement the built-in
// tables do not know (a custom path) is sent verbatim.
func TestAddResponsesReasoningOverrideWins(t *testing.T) {
	custom := &reasoning.Profile{
		EffortPath:   "custom.settings.depth",
		EffortValues: []string{"deep"},
	}
	body := map[string]any{}
	addResponsesReasoning(body, reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "deep"}, custom, "mystery-model", "")

	nested, _ := body["custom"].(map[string]any)
	settings, _ := nested["settings"].(map[string]any)
	if settings["depth"] != "deep" {
		t.Fatalf("the override path was not used: %v", body)
	}
	if _, ok := body["reasoning"]; ok {
		t.Fatalf("the built-in table must not be consulted when an override is set: %v", body)
	}
	if _, ok := body["include"]; !ok {
		t.Fatalf("an active control must still request the encrypted state: %v", body)
	}

	// The default mode contributes nothing, override or not.
	off := map[string]any{}
	addResponsesReasoning(off, reasoning.Intent{}, custom, "mystery-model", "")
	if len(off) != 0 {
		t.Fatalf("the defensive default must send nothing, got %v", off)
	}
}

// TestAddResponsesReasoningLocalHost pins the host threading of the Responses
// path: without it a local endpoint that speaks /v1/responses (Ollama does) is
// not recognised, the request goes out with no thinking field at all and the
// model answers without reasoning — which looks exactly like "this model cannot
// think". It also pins that the encrypted-reasoning state is only requested from
// the endpoints that produce it.
func TestAddResponsesReasoningLocalHost(t *testing.T) {
	const local = "http://localhost:11434/v1"

	body := map[string]any{}
	addResponsesReasoning(body, reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "high"}, nil, "gemma4:31b-cloud", local)
	if got := nestedString(body, "reasoning", "effort"); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high (%v)", got, body)
	}
	if _, ok := body["include"]; ok {
		t.Fatalf("a local server must not be asked for the encrypted state: %v", body)
	}

	off := map[string]any{}
	addResponsesReasoning(off, reasoning.Intent{Mode: reasoning.ModeOff}, nil, "gemma4:31b-cloud", local)
	if off["think"] != false {
		t.Fatalf("off should send think=false on a local server, got %v", off)
	}

	hosted := map[string]any{}
	addResponsesReasoning(hosted, reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "high"}, nil, "gpt-5.5", "")
	if _, ok := hosted["include"]; !ok {
		t.Fatalf("a hosted model must still request the encrypted state: %v", hosted)
	}
}

// TestAddResponsesReasoningLocalHostKnownModel pins that a local host selects the
// tolerant Local profile even when the model name matches a vendor family
// (Ollama + "qwen3:8b" yields Family=Qwen, Provider=Local): the encrypted state
// must not be requested there, and the effort must still be sent.
func TestAddResponsesReasoningLocalHostKnownModel(t *testing.T) {
	const local = "http://localhost:11434/v1"

	body := map[string]any{}
	addResponsesReasoning(body, reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "high"}, nil, "qwen3:8b", local)
	if got := nestedString(body, "reasoning", "effort"); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high (%v)", got, body)
	}
	if _, ok := body["include"]; ok {
		t.Fatalf("a local server must not be asked for the encrypted state: %v", body)
	}
}

func nestedString(body map[string]any, path ...string) string {
	var cur any = body
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}
