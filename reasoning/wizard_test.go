package reasoning

import (
	"strings"
	"testing"
)

func choiceKeys(p Profile) string {
	var keys []string
	for _, c := range Choices(p) {
		keys = append(keys, c.Key)
	}
	return strings.Join(keys, ",")
}

func TestChoicesPerProfile(t *testing.T) {
	cases := []struct {
		name string
		p    Profile
		want string
	}{
		{"unknown model", Profile{}, "default"},
		{"deepseek chat completions", chatCompletionsProfile(FamilyDeepSeek, "deepseek-chat"), "default,off,effort:minimal,effort:low,effort:medium,effort:high,effort:xhigh,effort:max"},
		{"glm-5.3 cannot disable", chatCompletionsProfile(FamilyGLM, "glm-5.3"), "default,effort:minimal,effort:low,effort:medium,effort:high,effort:xhigh,effort:max"},
		{"gemini 2.5 budget", geminiProfile("gemini-2.5-flash"), "default,off,adaptive,effort:minimal,effort:low,effort:medium,effort:high,effort:xhigh,effort:max"},
		{"anthropic phase 3", anthropicProfile("claude-opus-4-7"), "default,adaptive,effort:low,effort:medium,effort:high,effort:xhigh,effort:max"},
	}
	for _, tc := range cases {
		if got := choiceKeys(tc.p); got != tc.want {
			t.Errorf("%s: choices = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEffortLevels(t *testing.T) {
	// Mapping keys are the user-facing labels, ordered shallow-to-deep.
	if got := strings.Join(EffortLevels(chatCompletionsProfile(FamilyDeepSeek, "deepseek-chat")), ","); got != "minimal,low,medium,high,xhigh,max,ultra" {
		t.Errorf("deepseek levels = %q", got)
	}
	// Gemini 3 (uppercase wire labels) maps through the same labels.
	if got := strings.Join(EffortLevels(geminiProfile("gemini-3-pro")), ","); got != "minimal,low,medium,high,xhigh,max" {
		t.Errorf("gemini3 levels = %q", got)
	}
	// Gemini 2.5 has no effort field: the levels come from the budget mapping.
	if got := strings.Join(EffortLevels(geminiProfile("gemini-2.5-flash")), ","); got != "minimal,low,medium,high,xhigh,max" {
		t.Errorf("gemini2.5 levels = %q", got)
	}
	// A profile with no effort surface yields nothing (the UI falls back).
	if got := EffortLevels(Profile{}); len(got) != 0 {
		t.Errorf("empty profile levels = %v", got)
	}
	// The bare OpenAI effort list is used as-is when there is no mapping.
	if got := strings.Join(EffortLevels(chatCompletionsProfile(FamilyOpenAI, "gpt-5")), ","); got != "minimal,low,medium,high" {
		t.Errorf("openai levels = %q", got)
	}
}

func TestLevels(t *testing.T) {
	// A profile that accepts arbitrary labels gets the full canonical scale.
	if got := strings.Join(Levels(Profile{EffortPath: "reasoning_effort"}), ","); got != "minimal,low,medium,high,xhigh,max" {
		t.Errorf("open-ended levels = %q", got)
	}
	// The scale narrows to what the profile accepts...
	if got := strings.Join(Levels(chatCompletionsProfile(FamilyOpenAI, "gpt-5")), ","); got != "minimal,low,medium,high" {
		t.Errorf("openai levels = %q", got)
	}
	// ...and the model's own aliases collapse onto it (deepseek "ultra" -> max).
	if got := strings.Join(Levels(chatCompletionsProfile(FamilyDeepSeek, "deepseek-chat")), ","); got != "minimal,low,medium,high,xhigh,max" {
		t.Errorf("deepseek levels = %q", got)
	}
	// A toggle-only profile has no depth at all.
	if got := Levels(Profile{TogglePath: "enable_thinking", ToggleEnabledValue: true, ToggleDisabledValue: false}); len(got) != 0 {
		t.Errorf("toggle-only levels = %v", got)
	}
}

func TestLevelLabel(t *testing.T) {
	// A named effort shows the wire field it produces.
	if got := levelLabel("low", chatCompletionsProfile(FamilyDeepSeek, "deepseek-chat")); got != "Low — reasoning_effort=low" {
		t.Errorf("deepseek label = %q", got)
	}
	// A budget-derived level shows the token count it resolves to.
	if got := levelLabel("low", geminiProfile("gemini-2.5-flash")); got != "Low — generationConfig.thinkingConfig.thinkingBudget=2048" {
		t.Errorf("gemini label = %q", got)
	}
}

func TestPreview(t *testing.T) {
	p := chatCompletionsProfile(FamilyDeepSeek, "deepseek-chat")
	if got := Preview(Intent{Mode: ModeEffort, Effort: "high"}, p); got != "reasoning_effort=high" {
		t.Errorf("effort preview = %q", got)
	}
	if got := Preview(Intent{Mode: ModeOff}, p); got != "thinking.type=disabled" {
		t.Errorf("off preview = %q", got)
	}
	// The defensive default contributes nothing for a table with no Always.
	if got := Preview(Intent{Mode: ModeDefault}, p); got != "" {
		t.Errorf("default preview = %q, want empty", got)
	}
	// Always-on fields are shown even when no control is emitted.
	gp := geminiProfile("gemini-3-pro")
	want := "generationConfig.thinkingConfig.includeThoughts=true, generationConfig.thinkingConfig.thinkingLevel=LOW"
	if got := Preview(Intent{Mode: ModeEffort, Effort: "low"}, gp); got != want {
		t.Errorf("gemini preview = %q, want %q", got, want)
	}
	// Omitted top-level fields are surfaced as "omit <name>".
	ap := anthropicProfile("claude-sonnet-4")
	if got := Preview(Intent{Mode: ModeBudget, Budget: 8192}, ap); !strings.Contains(got, "omit temperature") {
		t.Errorf("anthropic preview = %q, want an omit-temperature marker", got)
	}
}

func TestProfileSummary(t *testing.T) {
	p := chatCompletionsProfile(FamilyDeepSeek, "deepseek-chat")
	want := "thinking.type(enabled|disabled) · reasoning_effort(minimal|low|medium|high|xhigh|max|ultra)"
	if got := p.Summary(); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if got := (Profile{}).Summary(); got != "" {
		t.Errorf("empty summary = %q, want empty", got)
	}
}
