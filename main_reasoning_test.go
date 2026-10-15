package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"fuckyourcli/models"
	"fuckyourcli/reasoning"
)

func TestParseExtraValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"custom", "custom"},
		{"4096", float64(4096)},
		{"true", true},
		{`{"a":1}`, map[string]any{"a": float64(1)}},
		{"", ""},
	}
	for _, tc := range cases {
		got := parseExtraValue(tc.in)
		if !equalAny(got, tc.want) {
			t.Errorf("parseExtraValue(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

// equalAny is a tiny deep-equal for the JSON-decoded shapes used above.
func equalAny(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !equalAny(v, bv[k]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func TestHostFromURL(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/v1": "api.deepseek.com",
		"http://localhost:8000/v1":    "localhost:8000",
		"api.z.ai/api/paas/v4":        "api.z.ai",
		"":                            "",
	}
	for in, want := range cases {
		if got := hostFromURL(in); got != want {
			t.Errorf("hostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReasoningSubmenu(t *testing.T) {
	keep := map[string]any{"foo": "bar"}
	ctx := models.Capabilities{}

	origPrompt := promptText
	t.Cleanup(func() { promptText = origPrompt })
	setPrompt := func(value string, ok bool) {
		promptText = func(string) (string, bool) { return value, ok }
	}

	// Direct modes set the mode and never go back.
	for key, want := range map[string]string{"default": "default", "off": "off", "adaptive": "adaptive"} {
		rc, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, key, keep, ctx)
		if back || rc.Mode != want {
			t.Fatalf("%s: rc=%+v back=%v", key, rc, back)
		}
		if rc.ExtraBody["foo"] != "bar" {
			t.Fatalf("%s: extra body not preserved: %+v", key, rc.ExtraBody)
		}
	}

	// A graded level is one key, "effort:<level>": no sub-selector.
	rc, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, "effort:low", keep, ctx)
	if back || rc.Mode != "effort" || rc.Effort != "low" {
		t.Fatalf("level pick: rc=%+v back=%v", rc, back)
	}

	// Custom effort: typed label; 'back' (and Esc) return to the mode menu.
	setPrompt("ultra", true)
	if rc, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, "effort_custom", keep, ctx); back || rc.Effort != "ultra" {
		t.Fatalf("custom effort: rc=%+v back=%v", rc, back)
	}
	setPrompt("back", true)
	if _, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, "effort_custom", keep, ctx); !back {
		t.Fatal("'back' custom effort must go back")
	}
	setPrompt("", false) // Esc / Ctrl+C
	if _, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, "effort_custom", keep, ctx); !back {
		t.Fatal("a cancelled custom effort must go back, not quit")
	}

	// Budget: numeric selector. Options: 0=Back, 1=1024, 2=2048, 3=4096, ...
	if rc, _ := reasoningSubmenu(&ChatSession{Sink: &recordSink{selectIndex: 3}}, "budget", keep, ctx); rc.Budget != 4096 {
		t.Fatalf("budget = %d, want 4096", rc.Budget)
	}
	// A documented range replaces the fallback: 1024, 2048, 4096, 8192, plus the
	// capped maximum.
	bcaps := models.Capabilities{HasBudget: true, BudgetMax: 8192}
	if rc, _ := reasoningSubmenu(&ChatSession{Sink: &recordSink{selectIndex: 4}}, "budget", keep, bcaps); rc.Budget != 8192 {
		t.Fatalf("catalog budget = %d, want 8192", rc.Budget)
	}
	if _, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{selectIndex: 0}}, "budget", keep, ctx); !back {
		t.Fatal("the \"← Back\" row must return to the mode menu")
	}
	if _, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{selectIndex: -1}}, "budget", keep, ctx); !back {
		t.Fatal("a dismissed budget selector must go back to the mode menu")
	}

	// Extra body: 'back' returns to the mode menu, Esc too.
	setPrompt("back", true)
	if _, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, "extra", keep, ctx); !back {
		t.Fatal("'back' extra body must go back")
	}
	setPrompt("", false)
	if _, back := reasoningSubmenu(&ChatSession{Sink: &recordSink{}}, "extra", keep, ctx); !back {
		t.Fatal("a cancelled extra body must go back, not quit")
	}
}

// TestReasoningMenus pins the menu generation: the fixed fallbacks, the
// catalog-driven effort/budget lists and the "Adaptive" hint.
func TestReasoningMenus(t *testing.T) {

	if got := strings.Join(intsToStrings(budgetChoices(models.Capabilities{})), ","); got != "1024,2048,4096,8192,16384,32768" {
		t.Fatalf("fallback budgets = %v", got)
	}
	// Documented range 1024..8192: the minimum plus the base values in range.
	got := budgetChoices(models.Capabilities{HasBudget: true, BudgetMin: 1024, BudgetMax: 8192})
	if strings.Join(intsToStrings(got), ",") != "1024,2048,4096,8192" {
		t.Fatalf("capped budgets = %v", got)
	}
	// min == -1 is "dynamic thinking", not a lower bound.
	got = budgetChoices(models.Capabilities{HasBudget: true, BudgetMin: -1, BudgetMax: 81920})
	if strings.Join(intsToStrings(got), ",") != "1024,2048,4096,8192,16384,32768,81920" {
		t.Fatalf("dynamic budgets = %v", got)
	}

	// The wizard lists only what the resolved profile can express: the default,
	// off/adaptive when supported, then one row per graded level, with the raw
	// editors behind "Advanced".
	effortProfile := reasoning.Profile{
		TogglePath:          "thinking.type",
		ToggleEnabledValue:  "enabled",
		ToggleDisabledValue: "disabled",
		EffortPath:          "reasoning_effort",
		EffortValues:        []string{"low", "high"},
	}
	if keys := strings.Join(optionKeys(reasoningModeOptions(effortProfile)), ","); keys != "default,off,effort:low,effort:high,advanced,cancel" {
		t.Fatalf("effort-only options = %v", keys)
	}
	adaptiveProfile := effortProfile
	adaptiveProfile.ToggleAdaptiveValue = "adaptive"
	if keys := strings.Join(optionKeys(reasoningModeOptions(adaptiveProfile)), ","); keys != "default,off,adaptive,effort:low,effort:high,advanced,cancel" {
		t.Fatalf("adaptive options = %v", keys)
	}
	// An unknown model (empty profile) offers nothing but the default.
	if keys := strings.Join(optionKeys(reasoningModeOptions(reasoning.Profile{})), ","); keys != "default,advanced,cancel" {
		t.Fatalf("unknown-model options = %v", keys)
	}
}

func optionKeys(opts []reasoningModeOption) []string {
	keys := make([]string, len(opts))
	for i, o := range opts {
		keys[i] = o.key
	}
	return keys
}

func intsToStrings(vals []int) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strconv.Itoa(v)
	}
	return out
}

func TestLoadUserPreferencesUsesConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	if got := loadUserPreferences(); got != "" {
		t.Fatalf("missing preferences = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "PREFERENCES.txt"), []byte("concise\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadUserPreferences(); got != "concise" {
		t.Fatalf("preferences = %q, want concise", got)
	}
}
