package reasoning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/models"
)

// TestMain disables the catalog source: the unit tests must not depend on a
// models.json that happens to exist on the machine. The overlay is exercised
// explicitly by the tests below, with a stub.
func TestMain(m *testing.M) {
	capabilitySource = nil
	os.Exit(m.Run())
}

// withCaps installs a stub capability source for the duration of a test.
func withCaps(t *testing.T, caps models.Capabilities, ok bool) {
	t.Helper()
	prev := capabilitySource
	capabilitySource = func(string, string) (models.Capabilities, bool) { return caps, ok }
	t.Cleanup(func() { capabilitySource = prev })
}

func TestOverlayEffortNarrowsToCatalog(t *testing.T) {
	withCaps(t, models.Capabilities{Reasoning: true, Effort: []string{"low", "high"}}, true)

	p, _ := Lookup(ChatCompletions, "", "deepseek-v4-pro", nil)
	if got := strings.Join(p.EffortValues, ","); got != "low,high" {
		t.Fatalf("EffortValues = %q, want low,high", got)
	}
}

func TestOverlayEffortKeepsWireLabels(t *testing.T) {
	// Gemini spells its levels in upper case; the overlay must map, not copy.
	withCaps(t, models.Capabilities{Reasoning: true, Effort: []string{"low", "high"}}, true)

	p, _ := Lookup(GeminiGenerateContent, "", "gemini-3-pro", nil)
	if got := strings.Join(p.EffortValues, ","); got != "LOW,HIGH" {
		t.Fatalf("EffortValues = %q, want LOW,HIGH", got)
	}
}

func TestOverlayIgnoresUnknownLabels(t *testing.T) {
	// "default" is not a label the DeepSeek table knows: sending it blindly
	// would guess, so it must be dropped.
	withCaps(t, models.Capabilities{Reasoning: true, Effort: []string{"default", "high"}}, true)

	p, _ := Lookup(ChatCompletions, "", "deepseek-v4-pro", nil)
	if got := strings.Join(p.EffortValues, ","); got != "high" {
		t.Fatalf("EffortValues = %q, want high", got)
	}
}

func TestOverlayBudgetTightensRange(t *testing.T) {
	withCaps(t, models.Capabilities{
		Reasoning: true, HasBudget: true, BudgetMin: -1, BudgetMax: 8192,
		Adaptive: true, ForbidsTemperature: true,
	}, true)

	p, _ := Lookup(AnthropicMessages, "", "claude-sonnet-4-5", nil)
	if p.BudgetMin != 1024 {
		t.Fatalf("BudgetMin = %d, want the table's 1024 (negative min is not a bound)", p.BudgetMin)
	}
	if p.BudgetMax != 8192 {
		t.Fatalf("BudgetMax = %d, want 8192 (catalog maximum)", p.BudgetMax)
	}
	if p.AdaptiveBudget != -1 {
		t.Fatalf("AdaptiveBudget = %d, want -1 (dynamic)", p.AdaptiveBudget)
	}
	if !p.ForbidsTemperature {
		t.Fatal("ForbidsTemperature must be raised by the catalog")
	}
}

func TestOverlayBudgetMinRaisedOnlyWhenCannotDisable(t *testing.T) {
	// Gemini Flash can be turned off with a zero budget, so a documented
	// minimum must not make "off" impossible.
	keepZero := models.Capabilities{Reasoning: true, HasBudget: true, BudgetMin: 1024, BudgetMax: 32768, CanDisable: true}
	withCaps(t, keepZero, true)
	p, _ := Lookup(GeminiGenerateContent, "", "gemini-2.5-flash", nil)
	if p.BudgetMin != 0 {
		t.Fatalf("BudgetMin = %d, want 0 for a model that can be disabled", p.BudgetMin)
	}

	raise := models.Capabilities{Reasoning: true, HasBudget: true, BudgetMin: 4096}
	withCaps(t, raise, true)
	p, _ = Lookup(AnthropicMessages, "", "claude-haiku-4-5", nil)
	if p.BudgetMin != 4096 {
		t.Fatalf("BudgetMin = %d, want 4096", p.BudgetMin)
	}
}

func TestOverlayNoOptionsOnlyWithoutToggle(t *testing.T) {
	withCaps(t, models.Capabilities{Reasoning: true, NoOptions: true}, true)

	// Mistral exposes an effort knob only: nothing can disable it.
	p, _ := Lookup(ChatCompletions, "", "magistral-small", nil)
	if !p.CannotDisable {
		t.Fatalf("CannotDisable not raised for a model with no documented control: %+v", p)
	}

	// Qwen has a transport toggle: a documented absence must not break it.
	p, _ = Lookup(ChatCompletions, "", "qwen3-32b-thinking", nil)
	if p.CannotDisable || p.TogglePath == "" {
		t.Fatalf("a table toggle must survive the overlay: %+v", p)
	}
}

func TestOverlayInertWithoutCatalog(t *testing.T) {
	capabilitySource = nil // TestMain default, restated for clarity

	got, _ := Lookup(ChatCompletions, "", "deepseek-v4-pro", nil)
	want := builtin(ChatCompletions, FamilyDeepSeek, "deepseek-v4-pro")
	if !equalProfile(got, want) {
		t.Fatalf("profile without a catalog =\n%+v\nwant\n%+v", got, want)
	}

	// An unknown model stays unknown (defensive default).
	got, det := Lookup(ChatCompletions, "", "totally-unknown", nil)
	if det.Confident || got.TogglePath != "" || len(got.EffortValues) != 0 {
		t.Fatalf("unknown model must keep the defensive default: %+v (%+v)", got, det)
	}
}

func TestOverlaySkipsForcedProfile(t *testing.T) {
	withCaps(t, models.Capabilities{Reasoning: true, Effort: []string{"low"}}, true)

	override := &Profile{EffortPath: "reasoning_effort", EffortValues: []string{"high"}}
	p, det := Lookup(ChatCompletions, "", "deepseek-v4-pro", override)
	if det.Source != "override" {
		t.Fatalf("source = %q, want override", det.Source)
	}
	if strings.Join(p.EffortValues, ",") != "high" {
		t.Fatalf("a forced profile must never be overlaid: %+v", p)
	}
}

// TestCatalogCapabilitiesPrefersFirstParty pins the provider resolution: without
// a base URL the model family must win over an aggregator listed earlier.
func TestCatalogCapabilitiesPrefersFirstParty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	models.Invalidate()
	t.Cleanup(models.Invalidate)

	const doc = `{
	  "302ai":{"id":"302ai","models":{
	    "claude-opus-4-5":{"id":"claude-opus-4-5","reasoning":false},
	    "gemini-3-pro":{"id":"gemini-3-pro","reasoning":false}}},
	  "anthropic":{"id":"anthropic","models":{
	    "claude-opus-4-5":{"id":"claude-opus-4-5","reasoning":true,
	      "reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "google":{"id":"google","api":"https://proxy.internal/v1","models":{
	    "gemini-3-pro":{"id":"gemini-3-pro","reasoning":true,
	      "reasoning_options":[{"type":"effort","values":["low","high","max"]}]}}}
	}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	caps, ok := CatalogCapabilities("", "claude-opus-4-5")
	if !ok || caps.ProviderID != "anthropic" || !caps.Reasoning {
		t.Fatalf("family hint not applied: %+v ok=%v", caps, ok)
	}

	// A documented base URL wins over the family hint.
	caps, ok = CatalogCapabilities("https://proxy.internal/v1", "gemini-3-pro")
	if !ok || caps.ProviderID != "google" || strings.Join(caps.Effort, ",") != "low,high,max" {
		t.Fatalf("base URL resolution failed: %+v ok=%v", caps, ok)
	}

	if _, ok := CatalogCapabilities("", "nope"); ok {
		t.Fatal("an unknown model must not resolve")
	}
}

// TestLookupOverlayEndToEnd drives the overlay through a catalog on disk: the
// only test of this package that reads the file.
func TestLookupOverlayEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	models.Invalidate()
	t.Cleanup(models.Invalidate)

	const doc = `{"deepseek":{"id":"deepseek","api":"https://api.deepseek.com","models":{
	  "deepseek-v4-pro":{"id":"deepseek-v4-pro","reasoning":true,
	    "reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high"]}]}}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	// TestMain disables the source: re-enable the real one for this test only.
	prev := capabilitySource
	capabilitySource = func(baseURL, model string) (models.Capabilities, bool) {
		return CatalogCapabilities(baseURL, model)
	}
	t.Cleanup(func() { capabilitySource = prev })

	p, _ := Lookup(ChatCompletions, "https://api.deepseek.com/v1", "deepseek-v4-pro", nil)
	if got := strings.Join(p.EffortValues, ","); got != "low,high" {
		t.Fatalf("EffortValues = %q, want low,high (from the catalog)", got)
	}
}

// equalProfile compares the data fields of two Profiles (maps included).
func equalProfile(a, b Profile) bool {
	return a.Transport == b.Transport && a.Model == b.Model &&
		a.TogglePath == b.TogglePath && a.CannotDisable == b.CannotDisable &&
		a.EffortPath == b.EffortPath &&
		strings.Join(a.EffortValues, ",") == strings.Join(b.EffortValues, ",") &&
		a.BudgetPath == b.BudgetPath && a.BudgetMin == b.BudgetMin &&
		a.BudgetMax == b.BudgetMax && a.AdaptiveBudget == b.AdaptiveBudget &&
		a.ForbidsTemperature == b.ForbidsTemperature
}
