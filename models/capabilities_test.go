package models

import (
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

// TestModelCapabilities pins the derivation from a catalog entry to the
// reasoning capabilities, using the shapes observed in the real dump.
func TestModelCapabilities(t *testing.T) {
	cases := []struct {
		name string
		m    Model
		want Capabilities
	}{
		{
			name: "deepseek toggle+effort",
			m: Model{
				ID:        "deepseek/deepseek-v4-pro",
				Reasoning: true,
				ReasoningOptions: []ReasoningOption{
					{Type: "toggle"},
					{Type: "effort", Values: []string{"low", "high", "max"}},
				},
				Interleaved: &Interleaved{Field: "reasoning_content"},
			},
			want: Capabilities{
				ModelID: "deepseek/deepseek-v4-pro", Reasoning: true,
				Toggle: true, Effort: []string{"low", "high", "max"},
				CanDisable: true, InterleavedField: "reasoning_content",
			},
		},
		{
			name: "claude opus effort+budget, temperature true",
			m: Model{
				ID:        "claude-opus-4-5",
				Reasoning: true,
				ReasoningOptions: []ReasoningOption{
					{Type: "effort", Values: []string{"low", "medium", "high"}},
					{Type: "budget_tokens", Min: intPtr(1024)},
				},
				Temperature: boolPtr(true),
			},
			want: Capabilities{
				ModelID: "claude-opus-4-5", Reasoning: true,
				Effort:    []string{"low", "medium", "high"},
				HasBudget: true, BudgetMin: 1024,
			},
		},
		{
			name: "qwen3.5-flash toggle+budget cap",
			m: Model{
				ID:        "alibaba/qwen3.5-flash",
				Reasoning: true,
				ReasoningOptions: []ReasoningOption{
					{Type: "toggle"},
					{Type: "budget_tokens", Max: intPtr(81920)},
				},
			},
			want: Capabilities{
				ModelID: "alibaba/qwen3.5-flash", Reasoning: true,
				Toggle: true, HasBudget: true, BudgetMax: 81920, CanDisable: true,
			},
		},
		{
			name: "gpt-5.4-pro effort, temperature false",
			m: Model{
				ID:               "openai/gpt-5.4-pro",
				Reasoning:        true,
				ReasoningOptions: []ReasoningOption{{Type: "effort", Values: []string{"medium", "high", "xhigh"}}},
				Temperature:      boolPtr(false),
			},
			want: Capabilities{
				ModelID: "openai/gpt-5.4-pro", Reasoning: true,
				Effort: []string{"medium", "high", "xhigh"}, ForbidsTemperature: true,
			},
		},
		{
			name: "gemini dynamic budget (-1)",
			m: Model{
				ID:               "gemini-2.5-flash",
				Reasoning:        true,
				ReasoningOptions: []ReasoningOption{{Type: "budget_tokens", Min: intPtr(-1), Max: intPtr(32768)}},
			},
			want: Capabilities{
				ModelID: "gemini-2.5-flash", Reasoning: true,
				HasBudget: true, BudgetMin: -1, BudgetMax: 32768, Adaptive: true,
			},
		},
		{
			name: "reasons with no documented control",
			m:    Model{ID: "mystery-thinking", Reasoning: true},
			want: Capabilities{ModelID: "mystery-thinking", Reasoning: true, NoOptions: true},
		},
		{
			name: "zero budget disables",
			m: Model{
				ID:               "gemini-2.5-flash-lite",
				Reasoning:        true,
				ReasoningOptions: []ReasoningOption{{Type: "budget_tokens", Min: intPtr(0), Max: intPtr(24576)}},
			},
			want: Capabilities{
				ModelID: "gemini-2.5-flash-lite", Reasoning: true,
				HasBudget: true, BudgetMin: 0, BudgetMax: 24576, CanDisable: true,
			},
		},
		{
			name: "normalizes effort labels",
			m: Model{
				ID:               "anomalous",
				Reasoning:        true,
				ReasoningOptions: []ReasoningOption{{Type: "effort", Values: []string{"None", "high", "HIGH", " low "}}},
			},
			want: Capabilities{
				ModelID: "anomalous", Reasoning: true,
				Effort: []string{"none", "low", "high"}, CanDisable: true,
			},
		},
		{
			name: "budget capped to the output limit",
			m: Model{
				ID:               "capped",
				Reasoning:        true,
				ReasoningOptions: []ReasoningOption{{Type: "budget_tokens", Min: intPtr(1024), Max: intPtr(393216)}},
				Limit:            Limit{Output: 65536},
			},
			want: Capabilities{
				ModelID: "capped", Reasoning: true,
				HasBudget: true, BudgetMin: 1024, BudgetMax: 65536, MaxOutput: 65536,
			},
		},
		{
			name: "deprecated status is carried",
			m:    Model{ID: "legacy", Reasoning: true, Status: "deprecated"},
			want: Capabilities{ModelID: "legacy", Reasoning: true, NoOptions: true, Status: "deprecated"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.m.Capabilities()
			if got.ProviderID != "" {
				t.Fatalf("Capabilities() must not set ProviderID: %q", got.ProviderID)
			}
			got.ProviderID = ""
			if !equalCaps(got, tc.want) {
				t.Fatalf("Capabilities() =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// TestCapabilitiesFromJSON covers the JSON decoding of the tri-state temperature
// and the status field, which a plain bool could not express.
func TestCapabilitiesFromJSON(t *testing.T) {
	const doc = `{"p":{"id":"p","models":{
		"keeps-temp":{"id":"keeps-temp","reasoning":true,"temperature":true},
		"no-temp":{"id":"no-temp","reasoning":true,"temperature":false},
		"silent":{"id":"silent","reasoning":true,"status":"beta"}
	}}}`
	c := parseBytes([]byte(doc))

	got, ok := c.CapabilitiesFor("keeps-temp")
	if !ok || got.ForbidsTemperature {
		t.Fatalf("temperature:true must not forbid temperature: %+v ok=%v", got, ok)
	}
	if got, _ := c.CapabilitiesFor("no-temp"); !got.ForbidsTemperature {
		t.Fatalf("temperature:false must forbid temperature: %+v", got)
	}
	// An absent field means "undocumented", never "rejects".
	got, _ = c.CapabilitiesFor("silent")
	if got.ForbidsTemperature || got.Status != "beta" {
		t.Fatalf("absent temperature wrongly forbids it: %+v", got)
	}
}

func TestCapabilitiesLookup(t *testing.T) {
	c := parseBytes([]byte(sampleCatalog))

	got, ok := c.CapabilitiesFor("deepseek-v4-pro")
	if !ok || got.ProviderID != "deepseek" || strings.Join(got.Effort, ",") != "low,high,max" {
		t.Fatalf("CapabilitiesFor = %+v ok=%v", got, ok)
	}
	if _, ok := c.CapabilitiesFor("does-not-exist"); ok {
		t.Fatal("unknown model must not be found")
	}

	// The provider hint wins when it holds the entry, and is ignored otherwise.
	if got, ok := c.CapabilitiesForProvider("deepseek", "deepseek-v4-pro"); !ok || got.ProviderID != "deepseek" {
		t.Fatalf("CapabilitiesForProvider = %+v ok=%v", got, ok)
	}
	if got, ok := c.CapabilitiesForProvider("no-such-provider", "deepseek-v4-pro"); !ok || got.ProviderID != "deepseek" {
		t.Fatalf("provider hint must fall back to a global lookup: %+v ok=%v", got, ok)
	}
}

func TestProviderIDForBaseURL(t *testing.T) {
	c := parseBytes([]byte(`{
		"deepseek":{"id":"deepseek","api":"https://api.deepseek.com","models":{}},
		"alibaba":{"id":"alibaba","api":"https://dashscope-intl.aliyuncs.com/compatible-mode/v1","models":{}},
		"anthropic":{"id":"anthropic","models":{}}
	}`))
	cases := map[string]string{
		"https://api.deepseek.com/v1":                            "deepseek",
		"api.deepseek.com":                                       "deepseek",
		"https://dashscope-intl.aliyuncs.com/compatible-mode/v1": "alibaba",
		"https://api.openai.com/v1":                              "",
		"":                                                       "",
	}
	for in, want := range cases {
		if got := c.ProviderIDForBaseURL(in); got != want {
			t.Errorf("ProviderIDForBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func intPtr(v int) *int { return &v }

func equalCaps(a, b Capabilities) bool {
	if a.ProviderID != b.ProviderID || a.ModelID != b.ModelID ||
		a.Reasoning != b.Reasoning || a.Toggle != b.Toggle ||
		a.HasBudget != b.HasBudget || a.BudgetMin != b.BudgetMin ||
		a.BudgetMax != b.BudgetMax || a.Adaptive != b.Adaptive ||
		a.CanDisable != b.CanDisable || a.NoOptions != b.NoOptions ||
		a.MaxOutput != b.MaxOutput || a.ForbidsTemperature != b.ForbidsTemperature ||
		a.InterleavedField != b.InterleavedField || a.Status != b.Status {
		return false
	}
	return strings.Join(a.Effort, ",") == strings.Join(b.Effort, ",")
}
