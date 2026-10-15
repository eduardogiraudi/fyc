package reasoning

import (
	"strings"
	"testing"
)

// apply is a convenience: translate then merge into a fresh body.
func apply(t *testing.T, in Intent, p Profile) map[string]any {
	t.Helper()
	plan, err := Translate(in, p)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	body := map[string]any{}
	plan.Apply(body)
	return body
}

// at walks a nested body by keys, returning nil when a step is missing.
func at(body map[string]any, path ...string) any {
	var cur any = body
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func TestDeepSeekChatCompletions(t *testing.T) {
	p := builtin(ChatCompletions, FamilyDeepSeek, "deepseek-v4-pro")

	// Defensive default: nothing but the Always fields (none here).
	if body := apply(t, Intent{Mode: ModeDefault}, p); len(body) != 0 {
		t.Fatalf("default mode should send nothing, got %v", body)
	}

	// Effort is remapped: medium -> high.
	body := apply(t, Intent{Mode: ModeEffort, Effort: "medium"}, p)
	if got := at(body, "reasoning_effort"); got != "high" {
		t.Fatalf("medium should map to high, got %v", got)
	}
	if got := at(body, "extra_body", "thinking", "type"); got != nil {
		t.Fatalf("effort mode should not set the toggle, got %v", got)
	}

	// xhigh -> high as well.
	if got := at(apply(t, Intent{Mode: ModeEffort, Effort: "xhigh"}, p), "reasoning_effort"); got != "high" {
		t.Fatalf("xhigh should map to high, got %v", got)
	}

	// Off uses the thinking toggle.
	off := apply(t, Intent{Mode: ModeOff}, p)
	if got := at(off, "thinking", "type"); got != "disabled" {
		t.Fatalf("off should disable via thinking.type, got %v", got)
	}
}

func TestGLMAlwaysOn(t *testing.T) {
	p := builtin(ChatCompletions, FamilyGLM, "glm-5.3")

	plan, _ := Translate(Intent{Mode: ModeOff}, p)
	if len(plan.Set) != 0 {
		t.Fatalf("GLM-5.3 cannot be disabled, expected no fields, got %v", plan.Set)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected a warning when disabling an always-on model")
	}

	body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
	if got := at(body, "reasoning_effort"); got != "high" {
		t.Fatalf("got %v", got)
	}
}

func TestGLM52Remap(t *testing.T) {
	p := builtin(ChatCompletions, FamilyGLM, "glm-5.2")
	body := apply(t, Intent{Mode: ModeEffort, Effort: "none"}, p)
	if got := at(body, "reasoning_effort"); got != "stop" {
		t.Fatalf("none should map to stop, got %v", got)
	}
	body = apply(t, Intent{Mode: ModeEffort, Effort: "xhigh"}, p)
	if got := at(body, "reasoning_effort"); got != "max" {
		t.Fatalf("xhigh should map to max, got %v", got)
	}
}

func TestQwenNestedToggle(t *testing.T) {
	p := builtin(ChatCompletions, FamilyQwen, "qwen3-32b")
	body := apply(t, Intent{Mode: ModeOff}, p)
	if got := at(body, "chat_template_kwargs", "enable_thinking"); got != false {
		t.Fatalf("expected chat_template_kwargs.enable_thinking=false, got %v", got)
	}
}

// TestLocalServerUsesReasoningEffort pins the local-server defaults: a host that
// is a local inference server (Ollama, vLLM, SGLang, llama.cpp) with a model
// name nobody recognises still gets a usable profile instead of the empty one.
// Ollama documents reasoning_effort as the control of its OpenAI-compatible
// endpoint; the native "think" field is not part of that API.
func TestLocalServerUsesReasoningEffort(t *testing.T) {
	p, det := Lookup(ChatCompletions, "http://localhost:11434/v1", "unknown-llm", nil)
	if det.Confident {
		t.Fatalf("a local host must not be confident, got %+v", det)
	}
	if det.Family != FamilyLocal || p.EffortPath != "reasoning_effort" {
		t.Fatalf("expected the local profile, got family=%q profile=%+v", det.Family, p)
	}

	body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
	if got := at(body, "reasoning_effort"); got != "high" {
		t.Fatalf("effort should be sent as reasoning_effort, got %v", got)
	}
	if got := at(body, "chat_template_kwargs", "enable_thinking"); got != nil {
		t.Fatalf("effort mode must not also set the toggle, got %v", got)
	}

	// max/xhigh clamp onto the labels Ollama accepts.
	if got := at(apply(t, Intent{Mode: ModeEffort, Effort: "max"}, p), "reasoning_effort"); got != "high" {
		t.Fatalf("max should map to high, got %v", got)
	}

	// Off keeps the nested toggle, which is what the other local servers read.
	off := apply(t, Intent{Mode: ModeOff}, p)
	if got := at(off, "chat_template_kwargs", "enable_thinking"); got != false {
		t.Fatalf("off should disable via the nested toggle, got %v", got)
	}

	// The defensive default still sends nothing at all.
	if d := apply(t, Intent{Mode: ModeDefault}, p); len(d) != 0 {
		t.Fatalf("default must send nothing, got %v", d)
	}
}

// TestLocalHostKnownModelUsesLocalProfile pins that the local host, not the model
// brand, selects the profile: Ollama serving "qwen3:8b" yields Family=Qwen but
// Provider=Local, and must get the tolerant Local profile (reasoning_effort)
// instead of the Qwen profile, which exposes no effort control at all.
func TestLocalHostKnownModelUsesLocalProfile(t *testing.T) {
	p, det := Lookup(ChatCompletions, "http://localhost:11434/v1", "qwen3:8b", nil)
	if det.Provider != FamilyLocal || det.Family != FamilyQwen {
		t.Fatalf("expected Family=Qwen Provider=Local, got %+v", det)
	}
	if p.EffortPath != "reasoning_effort" {
		t.Fatalf("expected the Local profile, got %+v", p)
	}
	body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
	if got := at(body, "reasoning_effort"); got != "high" {
		t.Fatalf("effort should be sent as reasoning_effort, got %v", body)
	}
}

func TestAnthropicPhase1Budget(t *testing.T) {
	p := builtin(AnthropicMessages, FamilyAnthropic, "claude-sonnet-4-5")

	body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
	if got := at(body, "thinking", "type"); got != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %v", got)
	}
	if got := at(body, "thinking", "budget_tokens"); got != 16000 {
		t.Fatalf("expected budget 16000, got %v", got)
	}

	// Off is an explicit disabled block.
	body = apply(t, Intent{Mode: ModeOff}, p)
	if got := at(body, "thinking", "type"); got != "disabled" {
		t.Fatalf("expected thinking.type=disabled, got %v", got)
	}
}

// TestAnthropicPhase1AdaptiveSendsNothing pins that a profile with a toggle path
// has no budget-encoded adaptive mode: emitting thinking.budget_tokens with the
// zero (or a catalog-derived negative) adaptive budget would be rejected, so
// nothing is sent and a warning is recorded.
func TestAnthropicPhase1AdaptiveSendsNothing(t *testing.T) {
	for _, p := range []Profile{
		builtin(AnthropicMessages, FamilyAnthropic, "claude-sonnet-4-5"),
		{TogglePath: "thinking.type", BudgetPath: "thinking.budget_tokens", AdaptiveBudget: -1},
	} {
		plan, _ := Translate(Intent{Mode: ModeAdaptive}, p)
		if len(plan.Set) != 0 {
			t.Fatalf("adaptive with a toggle path must send nothing, got %v", plan.Set)
		}
		if !hasWarning(plan.Warnings, "adaptive") {
			t.Fatalf("expected an adaptive warning, got %v", plan.Warnings)
		}
	}
}

// TestProfileSupportsAdaptive pins the shared condition used by Translate and the
// /reasoning menu: adaptive is offered (and encoded) only when the profile can
// actually express it, even if a catalog-derived AdaptiveBudget is present.
func TestProfileSupportsAdaptive(t *testing.T) {
	cases := []struct {
		name string
		p    Profile
		want bool
	}{
		{"phase 1 (budget only)", builtin(AnthropicMessages, FamilyAnthropic, "claude-sonnet-4-5"), false},
		{"phase 2 (adaptive toggle)", builtin(AnthropicMessages, FamilyAnthropic, "claude-opus-4-6"), true},
		{"phase 3 (adaptive toggle)", builtin(AnthropicMessages, FamilyAnthropic, "claude-fable-5"), true},
		{"gemini 2.5 (budget -1)", builtin(GeminiGenerateContent, FamilyGemini, "gemini-2.5-flash"), true},
		{"gemini 3 (effort only)", builtin(GeminiGenerateContent, FamilyGemini, "gemini-3-pro"), false},
		{"toggle path with a catalog budget", Profile{TogglePath: "thinking.type", BudgetPath: "thinking.budget_tokens", AdaptiveBudget: -1}, false},
	}
	for _, tc := range cases {
		if got := tc.p.SupportsAdaptive(); got != tc.want {
			t.Errorf("%s: SupportsAdaptive() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAnthropicPhase1TemperatureOnlyForbiddenWithThinking pins the phase-1/2
// rule ("temperature is incompatible with thinking"): the field is dropped only
// when the request actually enables thinking, so "off" and "default" keep the
// user's value (and the request is not rejected with a 400).
func TestAnthropicPhase1TemperatureOnlyForbiddenWithThinking(t *testing.T) {
	p := builtin(AnthropicMessages, FamilyAnthropic, "claude-sonnet-4-5")
	if !p.ForbidsTemperatureWithThinking {
		t.Fatal("phase-1 model should forbid temperature while thinking")
	}
	if p.ForbidsTemperature {
		t.Fatal("phase-1 model must not forbid temperature unconditionally")
	}

	for _, mode := range []Intent{{Mode: ModeEffort, Effort: "high"}, {Mode: ModeBudget, Budget: 4096}} {
		plan, _ := Translate(mode, p)
		if !hasOmit(plan, "temperature") {
			t.Fatalf("mode %v must omit temperature, got %v", mode.Mode, plan.Omit)
		}
	}
	for _, mode := range []Intent{{Mode: ModeOff}, {Mode: ModeDefault}} {
		plan, _ := Translate(mode, p)
		if hasOmit(plan, "temperature") {
			t.Fatalf("mode %v must keep temperature, got %v", mode.Mode, plan.Omit)
		}
	}
}

func hasOmit(plan Plan, key string) bool {
	for _, o := range plan.Omit {
		if o == key {
			return true
		}
	}
	return false
}

func TestAnthropicBudgetClampedToMinimum(t *testing.T) {
	p := builtin(AnthropicMessages, FamilyAnthropic, "claude-sonnet-4-5")
	body := apply(t, Intent{Mode: ModeBudget, Budget: 500}, p)
	if got := at(body, "thinking", "budget_tokens"); got != 1024 {
		t.Fatalf("budget below the minimum should clamp to 1024, got %v", got)
	}
}

func TestAnthropicAdaptiveOnlyForbidsTemperature(t *testing.T) {
	p := builtin(AnthropicMessages, FamilyAnthropic, "claude-fable-5")
	if !p.ForbidsTemperature {
		t.Fatal("fable should forbid temperature")
	}
	body := apply(t, Intent{Mode: ModeAdaptive}, p)
	if got := at(body, "thinking", "type"); got != "adaptive" {
		t.Fatalf("expected adaptive, got %v", got)
	}

	plan, _ := Translate(Intent{Mode: ModeOff}, p)
	if len(plan.Set) != 0 || len(plan.Warnings) == 0 {
		t.Fatalf("adaptive-only model must not send a disabled toggle: set=%v warn=%v", plan.Set, plan.Warnings)
	}
	if len(plan.Omit) == 0 || plan.Omit[0] != "temperature" {
		t.Fatalf("expected temperature in Omit, got %v", plan.Omit)
	}
}

func TestGemini25DynamicAndDisable(t *testing.T) {
	p := builtin(GeminiGenerateContent, FamilyGemini, "gemini-2.5-flash")

	body := apply(t, Intent{Mode: ModeAdaptive}, p)
	if got := at(body, "generationConfig", "thinkingConfig", "thinkingBudget"); got != -1 {
		t.Fatalf("adaptive should set thinkingBudget=-1, got %v", got)
	}
	if got := at(body, "generationConfig", "thinkingConfig", "includeThoughts"); got != true {
		t.Fatalf("includeThoughts should always be true, got %v", got)
	}

	body = apply(t, Intent{Mode: ModeOff}, p)
	if got := at(body, "generationConfig", "thinkingConfig", "thinkingBudget"); got != 0 {
		t.Fatalf("off should set thinkingBudget=0 on Flash, got %v", got)
	}
}

func TestGemini3ThinkingLevel(t *testing.T) {
	p := builtin(GeminiGenerateContent, FamilyGemini, "gemini-3-pro")
	body := apply(t, Intent{Mode: ModeEffort, Effort: "max"}, p)
	if got := at(body, "generationConfig", "thinkingConfig", "thinkingLevel"); got != "HIGH" {
		t.Fatalf("max should map to HIGH, got %v", got)
	}
	if got := at(body, "generationConfig", "thinkingConfig", "thinkingBudget"); got != nil {
		t.Fatalf("Gemini 3 must not send thinkingBudget, got %v", got)
	}
}

func TestGeminiDefaultKeepsThoughts(t *testing.T) {
	p := builtin(GeminiGenerateContent, FamilyGemini, "gemini-2.5-pro")
	body := apply(t, Intent{Mode: ModeDefault}, p)
	if got := at(body, "generationConfig", "thinkingConfig", "includeThoughts"); got != true {
		t.Fatalf("default must still request thoughts, got %v", got)
	}
	if got := at(body, "generationConfig", "thinkingConfig", "thinkingBudget"); got != nil {
		t.Fatalf("default must not set a budget, got %v", got)
	}
}

func TestAnthropicDefaultSendsNothing(t *testing.T) {
	p := builtin(AnthropicMessages, FamilyAnthropic, "claude-sonnet-4-5")
	if plan, _ := Translate(Intent{Mode: ModeDefault}, p); len(plan.Set) != 0 {
		t.Fatalf("default Anthropic must send nothing, got %v", plan.Set)
	}
}

func TestResponsesSummaryOnlyWithControl(t *testing.T) {
	p := builtin(OpenAIResponses, FamilyOpenAI, "gpt-5.5")

	body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
	if got := at(body, "reasoning", "effort"); got != "high" {
		t.Fatalf("expected reasoning.effort=high, got %v", got)
	}
	if got := at(body, "reasoning", "summary"); got != "auto" {
		t.Fatalf("summary must accompany the effort, got %v", got)
	}

	// Default: nothing at all, in particular no reasoning.summary.
	if off := apply(t, Intent{Mode: ModeDefault}, p); len(off) != 0 {
		t.Fatalf("default Responses must send nothing, got %v", off)
	}
}

func TestExtraBodyOverridesEverything(t *testing.T) {
	p := builtin(ChatCompletions, FamilyUnknown, "mystery-model")
	in := Intent{Mode: ModeEffort, Effort: "high", ExtraBody: map[string]any{"reasoning_effort": "custom"}}
	body := apply(t, in, p)
	if got := at(body, "reasoning_effort"); got != "custom" {
		t.Fatalf("extra_body must win, got %v", got)
	}
}

func TestApplyDeletesOmittedTopLevelKey(t *testing.T) {
	plan := Plan{Omit: []string{"temperature"}}
	body := map[string]any{"temperature": 0.7, "model": "x"}
	plan.Apply(body)
	if _, ok := body["temperature"]; ok {
		t.Fatal("Omit should delete temperature")
	}
	if body["model"] != "x" {
		t.Fatal("Apply must not touch other keys")
	}
}

func TestApplyDeletesOmittedNestedPath(t *testing.T) {
	plan := Plan{Omit: []string{"generationConfig.temperature", "missing.temperature", "model.temperature"}}
	body := map[string]any{
		"model": "x",
		"generationConfig": map[string]any{
			"temperature":    0.7,
			"thinkingConfig": map[string]any{"includeThoughts": true},
		},
	}
	plan.Apply(body)
	generation := body["generationConfig"].(map[string]any)
	if _, ok := generation["temperature"]; ok {
		t.Fatal("Omit should delete nested temperature")
	}
	if at(body, "generationConfig", "thinkingConfig", "includeThoughts") != true {
		t.Fatal("Omit must preserve sibling fields")
	}
	if _, ok := body["missing"]; ok || body["model"] != "x" {
		t.Fatal("Omit must not create or overwrite intermediate objects")
	}
}

func TestApplyMergesNestedObjects(t *testing.T) {
	p := builtin(GeminiGenerateContent, FamilyGemini, "gemini-3-pro")
	plan, err := Translate(Intent{Mode: ModeEffort, Effort: "high"}, p)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"generationConfig": map[string]any{
			"temperature":     0.7,
			"maxOutputTokens": 4096,
			"thinkingConfig": map[string]any{
				"includeThoughts": false,
				"custom":          true,
			},
		},
	}
	plan.Apply(body)
	if at(body, "generationConfig", "temperature") != 0.7 ||
		at(body, "generationConfig", "maxOutputTokens") != 4096 ||
		at(body, "generationConfig", "thinkingConfig", "custom") != true {
		t.Fatalf("merge lost existing generation settings: %v", body)
	}
	if at(body, "generationConfig", "thinkingConfig", "includeThoughts") != true ||
		at(body, "generationConfig", "thinkingConfig", "thinkingLevel") != "HIGH" {
		t.Fatalf("merge did not apply reasoning fields: %v", body)
	}
}

func TestForbiddenTemperatureUsesTransportPath(t *testing.T) {
	for _, transport := range []Transport{ChatCompletions, OpenAIResponses, AnthropicMessages, GeminiGenerateContent} {
		t.Run(string(transport), func(t *testing.T) {
			path := "temperature"
			if transport == GeminiGenerateContent {
				path = "generationConfig.temperature"
			}
			for _, mode := range []Mode{ModeDefault, ModeOff, ModeEffort} {
				p := Profile{Transport: transport, ForbidsTemperature: true}
				plan, err := Translate(Intent{Mode: mode, Effort: "high", ExtraBody: map[string]any{path: 0.9}}, p)
				if err != nil {
					t.Fatal(err)
				}
				if !hasOmit(plan, path) {
					t.Fatalf("mode %v must omit %s, got %v", mode, path, plan.Omit)
				}
				body := map[string]any{}
				setInMap(body, strings.Split(path, "."), 0.7)
				plan.Apply(body)
				if got := at(body, strings.Split(path, ".")...); got != nil {
					t.Fatalf("forbidden temperature survived Apply: %v", body)
				}
			}
		})
	}
}

func TestUnknownModelSendsNothing(t *testing.T) {
	plan, err := Translate(Intent{Mode: ModeEffort, Effort: "high"}, builtin(ChatCompletions, FamilyUnknown, "mystery"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Set) != 0 {
		t.Fatalf("unknown model must send nothing, got %v", plan.Set)
	}
	if !hasWarning(plan.Warnings, "does not expose an effort") {
		t.Fatalf("expected a warning, got %v", plan.Warnings)
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestLocalServerResponsesRoute pins the local /v1/responses defaults.
func TestLocalServerResponsesRoute(t *testing.T) {
	p, det := Lookup(OpenAIResponses, "http://localhost:11434/v1", "gemma4:31b-cloud", nil)
	if !det.Confident || det.Family != FamilyGemma || det.Provider != FamilyLocal {
		t.Fatalf("expected Gemma on the local endpoint, got %+v", det)
	}

	body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
	if got := at(body, "reasoning", "effort"); got != "high" {
		t.Fatalf("expected reasoning.effort=high, got %v", got)
	}
	if got := at(body, "think"); got != nil {
		t.Fatalf("effort mode must not also set think, got %v", got)
	}

	off := apply(t, Intent{Mode: ModeOff}, p)
	if got := at(off, "think"); got != false {
		t.Fatalf("off should disable via think=false, got %v", got)
	}

	if d := apply(t, Intent{Mode: ModeDefault}, p); len(d) != 0 {
		t.Fatalf("default must send nothing, got %v", d)
	}
}
