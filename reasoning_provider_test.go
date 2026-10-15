package main

import (
	"strings"
	"testing"

	"fuckyourcli/config"
)

// TestApplyReasoningFieldEdit pins the mapping between a typed row value and the
// stored override, including the "empty clears" rule and the malformed inputs.
func TestApplyReasoningFieldEdit(t *testing.T) {
	var pc config.ReasoningProviderConfig
	set := func(key, value string) {
		t.Helper()
		if err := applyReasoningFieldEdit(&pc, key, value); err != nil {
			t.Fatalf("%s=%q: %v", key, value, err)
		}
	}

	set("toggle_path", "chat_template_kwargs.enable_thinking")
	set("toggle_on", "true")
	set("toggle_off", "false")
	set("toggle_adaptive", `"adaptive"`)
	set("toggle_from_control", "true")
	set("cannot_disable", "true")
	set("effort_path", "reasoning_effort")
	set("effort_values", "low, high ,high")
	set("effort_map", "minimal=low,medium=high")
	set("budget_path", "thinking.budget_tokens")
	set("budget_min", "1024")
	set("budget_max", "8192")
	set("budget_from_effort", "low=2048,high=16384")
	set("adaptive_budget", "-1")
	set("forbids_temperature", "true")
	set("always", `{"generationConfig.thinkingConfig.includeThoughts":true}`)
	set("alongside_control", `{"reasoning.summary":"auto"}`)

	p := pc.Profile
	if p == nil {
		t.Fatal("the profile must be created by the first edit")
	}
	if p.TogglePath != "chat_template_kwargs.enable_thinking" || p.ToggleEnabledValue != true || p.ToggleDisabledValue != false {
		t.Fatalf("toggle fields = %+v", p)
	}
	if p.ToggleAdaptiveValue != "adaptive" || !p.ToggleFromControl || !p.CannotDisable {
		t.Fatalf("toggle extras = %+v", p)
	}
	if p.EffortPath != "reasoning_effort" || strings.Join(p.EffortValues, "|") != "low|high" {
		t.Fatalf("effort = %+v", p)
	}
	if p.EffortMap["minimal"] != "low" || p.EffortMap["medium"] != "high" {
		t.Fatalf("effort map = %+v", p.EffortMap)
	}
	if p.BudgetPath != "thinking.budget_tokens" || p.BudgetMin != 1024 || p.BudgetMax != 8192 {
		t.Fatalf("budget = %+v", p)
	}
	if p.BudgetFromEffort["low"] != 2048 || p.BudgetFromEffort["high"] != 16384 || p.AdaptiveBudget != -1 {
		t.Fatalf("budget extras = %+v", p)
	}
	if !p.ForbidsTemperature || len(p.Always) != 1 || len(p.AlongsideControl) != 1 {
		t.Fatalf("flags/maps = %+v", p)
	}

	// The intent rows are independent of the placement.
	set("mode", "effort:high")
	if pc.Intent.Mode != "effort" || pc.Intent.Effort != "high" {
		t.Fatalf("mode=effort:high -> %+v", pc.Intent)
	}
	set("mode", "budget:8192")
	if pc.Intent.Mode != "budget" || pc.Intent.Budget != 8192 {
		t.Fatalf("mode=budget:8192 -> %+v", pc.Intent)
	}
	set("budget", "2048")
	if pc.Intent.Budget != 2048 {
		t.Fatalf("budget row -> %+v", pc.Intent)
	}
	set("mode", "inherit")
	if pc.Intent.Mode != "" || pc.Intent.Budget != 0 || pc.Intent.Effort != "" {
		t.Fatalf("inherit must clear the provider intent: %+v", pc.Intent)
	}

	// An empty value clears the field instead of storing "".
	set("toggle_path", "")
	set("effort_values", "")
	set("always", "")
	if p := pc.Profile; p.TogglePath != "" || p.EffortValues != nil || p.Always != nil {
		t.Fatalf("empty values must clear: %+v", p)
	}

	// Malformed input is reported, never stored.
	for _, bad := range []struct{ key, value string }{
		{"budget", "abc"},
		{"budget_min", "x"},
		{"effort_map", "nope"},
		{"budget_from_effort", "low=x"},
		{"always", "[1,2]"},
		{"mode", "sideways"},
		{"unknown_field", "1"},
	} {
		if err := applyReasoningFieldEdit(&pc, bad.key, bad.value); err == nil {
			t.Fatalf("%s=%q must be rejected", bad.key, bad.value)
		}
	}
}

func TestToggleReasoningField(t *testing.T) {
	pc := toggleReasoningField(config.ReasoningProviderConfig{}, "cannot_disable")
	if pc.Profile == nil || !pc.Profile.CannotDisable {
		t.Fatalf("cannot_disable not toggled on: %+v", pc)
	}
	pc = toggleReasoningField(pc, "cannot_disable")
	if pc.Profile.CannotDisable {
		t.Fatalf("cannot_disable not toggled off: %+v", pc)
	}
	pc = toggleReasoningField(pc, "forbids_temperature")
	if !pc.Profile.ForbidsTemperature {
		t.Fatalf("forbids_temperature not toggled: %+v", pc)
	}
}

// TestReasoningFieldRows checks the editor rows: one per field, current value
// inline, "(unset)" when empty.
func TestReasoningFieldRows(t *testing.T) {
	pc := config.ReasoningProviderConfig{
		Intent: config.ReasoningConfig{Mode: "effort", Effort: "high"},
		Profile: &config.ReasoningProfileConfig{
			TogglePath:   "thinking.type",
			EffortValues: []string{"low", "high"},
		},
	}
	labels := map[string]string{}
	for _, r := range reasoningFieldRows(pc) {
		labels[r.key] = r.label
	}
	for _, key := range []string{"mode", "toggle_path", "effort_path", "budget_path", "always"} {
		if _, ok := labels[key]; !ok {
			t.Fatalf("row %q missing from the editor: %v", key, labels)
		}
	}
	if !strings.Contains(labels["toggle_path"], "thinking.type") {
		t.Fatalf("toggle_path row does not show the value: %q", labels["toggle_path"])
	}
	if !strings.Contains(labels["mode"], "effort") {
		t.Fatalf("mode row does not show the value: %q", labels["mode"])
	}
	if !strings.Contains(labels["effort_values"], "low,high") {
		t.Fatalf("effort_values row does not show the value: %q", labels["effort_values"])
	}
	if !strings.Contains(labels["budget_path"], "(unset)") {
		t.Fatalf("an unset field must read (unset): %q", labels["budget_path"])
	}

	// A long value is truncated so it cannot break the selector layout.
	long := config.ReasoningProviderConfig{Profile: &config.ReasoningProfileConfig{TogglePath: strings.Repeat("x", 200)}}
	rows := reasoningFieldRows(long)
	for _, r := range rows {
		if len(r.label) > 100 {
			t.Fatalf("row label not truncated: %q", r.label)
		}
	}
}

// scriptedSink answers each SelectOption call with the next scripted index: the
// field editor is a loop, so a fixed index would never terminate.
type scriptedSink struct {
	*recordSink
	indices []int
}

func (s *scriptedSink) SelectOption(string, []string) int {
	if len(s.indices) == 0 {
		return -1
	}
	idx := s.indices[0]
	s.indices = s.indices[1:]
	return idx
}

// TestReasoningProviderFieldsMenu drives the editor end to end: pick a row, type
// a value, go back. The override must be stored and persisted immediately.
func TestReasoningProviderFieldsMenu(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	t.Cleanup(func() { config.ClearReasoningForProvider("DeepSeek") })

	origPrompt := promptText
	t.Cleanup(func() { promptText = origPrompt })
	promptText = func(string) (string, bool) { return "chat_template_kwargs.enable_thinking", true }

	rowIndex := func(key string) int {
		for i, r := range reasoningFieldRows(config.ReasoningProviderConfig{}) {
			if r.key == key {
				return i
			}
		}
		t.Fatalf("row %q missing", key)
		return -1
	}
	backIdx := len(reasoningFieldRows(config.ReasoningProviderConfig{})) + 1

	// 1. Edit a text field: the typed value lands in the profile.
	sink := &scriptedSink{recordSink: &recordSink{}, indices: []int{rowIndex("toggle_path"), backIdx}}
	reasoningProviderFieldsMenu(&ChatSession{Sink: sink}, "DeepSeek")
	pc, ok := config.ReasoningForProvider("DeepSeek")
	if !ok || pc.Profile == nil || pc.Profile.TogglePath != "chat_template_kwargs.enable_thinking" {
		t.Fatalf("text field not stored by the editor: %+v ok=%v", pc, ok)
	}

	// The editor saved to disk: reloading must see the same override.
	if err := config.LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if pc, ok := config.ReasoningForProvider("DeepSeek"); !ok || pc.Profile == nil {
		t.Fatalf("override not persisted: %+v ok=%v", pc, ok)
	}

	// 2. A boolean row is toggled in place, with no prompt.
	sink = &scriptedSink{recordSink: &recordSink{}, indices: []int{rowIndex("cannot_disable"), backIdx}}
	reasoningProviderFieldsMenu(&ChatSession{Sink: sink}, "DeepSeek")
	if pc, _ := config.ReasoningForProvider("DeepSeek"); pc.Profile == nil || !pc.Profile.CannotDisable {
		t.Fatalf("boolean row not toggled: %+v", pc)
	}

	// 3. "Reset" removes the whole override, and Back leaves the editor.
	resetIdx := len(reasoningFieldRows(config.ReasoningProviderConfig{}))
	sink = &scriptedSink{recordSink: &recordSink{}, indices: []int{resetIdx, backIdx}}
	reasoningProviderFieldsMenu(&ChatSession{Sink: sink}, "DeepSeek")
	if _, ok := config.ReasoningForProvider("DeepSeek"); ok {
		t.Fatal("Reset must remove the provider override")
	}
}
