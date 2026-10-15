package reasoning

import (
	"strings"

	"fuckyourcli/models"
)

// capabilitySource supplies the catalog-derived facts for a (base URL, model)
// pair. It is a variable so unit tests stay hermetic (a nil source disables the
// overlay entirely) and so the catalog remains an optional, best-effort input.
var capabilitySource = func(baseURL, model string) (models.Capabilities, bool) {
	return CatalogCapabilities(baseURL, model)
}

// CatalogCapabilities resolves a model's capabilities from the models.dev
// catalog, preferring the provider that owns baseURL and then the provider that
// publishes the model's family (native endpoints carry no base URL to match).
// It is best-effort: found is false when nothing plausible matches.
func CatalogCapabilities(baseURL, model string) (models.Capabilities, bool) {
	c := models.Cached()
	if pid := c.ProviderIDForBaseURL(baseURL); pid != "" {
		return c.CapabilitiesForProvider(pid, model)
	}
	provider := ProviderFromHost(baseURL)
	if provider == FamilyOllama || (provider == FamilyLocal &&
		(strings.HasSuffix(model, "-cloud") || strings.HasSuffix(model, ":cloud"))) {
		return c.CapabilitiesForProvider("ollama-cloud", model)
	}
	if pid := catalogProviderForFamily(FamilyFromModel(model)); pid != "" {
		if caps, ok := c.CapabilitiesForProvider(pid, model); ok {
			return caps, true
		}
	}
	return c.CapabilitiesFor(model)
}

// catalogProviderForFamily maps a family onto the models.dev provider that
// publishes it. Without this, a global lookup returns whichever aggregator
// (302ai, aihubmix…) happens to be listed first, whose metadata is usually a
// lossy copy of the first-party entry.
func catalogProviderForFamily(f Family) string {
	switch f {
	case FamilyAnthropic:
		return "anthropic"
	case FamilyGemini, FamilyGemma:
		return "google"
	case FamilyOllama:
		return "ollama-cloud"
	case FamilyOpenAI:
		return "openai"
	case FamilyDeepSeek:
		return "deepseek"
	case FamilyGLM:
		return "zai"
	case FamilyQwen:
		return "alibaba"
	case FamilyMistral:
		return "mistral"
	case FamilyKimi:
		return "moonshotai"
	case FamilyMiniMax:
		return "minimax"
	}
	return ""
}

// applyCatalog overlays the model-intrinsic facts the catalog documents onto the
// transport table. The overlay only ever narrows what the table allows: where a
// field lives on the wire (TogglePath, EffortPath, BudgetPath) is a property of
// the transport and stays the table's business.
func applyCatalog(p *Profile, host, model string) {
	if capabilitySource == nil {
		return
	}
	caps, ok := capabilitySource(host, model)
	if !ok {
		return
	}
	if caps.ForbidsTemperature {
		p.ForbidsTemperature = true
	}
	// Only when the model documents no control at all, and only if the
	// transport table does not already offer an explicit toggle (a documented
	// absence is not the same as "the wire rejects the field").
	if caps.NoOptions && p.TogglePath == "" {
		p.CannotDisable = true
	}
	overlayEffort(p, caps)
	overlayBudget(p, caps)
}

// overlayEffort narrows EffortValues to the levels the model documents, keeping
// the wire labels of the table (Gemini's levels are uppercase, for instance).
func overlayEffort(p *Profile, caps models.Capabilities) {
	if p.EffortPath == "" || len(caps.Effort) == 0 {
		return
	}
	upper := usesUpperLabels(p.EffortValues)
	out := make([]string, 0, len(caps.Effort))
	for _, label := range caps.Effort {
		wire := label
		if mapped, ok := p.EffortMap[label]; ok {
			wire = mapped
		}
		if upper {
			wire = strings.ToUpper(wire)
		}
		if len(p.EffortValues) > 0 && !hasFold(p.EffortValues, wire) {
			continue // the table does not know this label: sending it would guess
		}
		out = append(out, wire)
	}
	if len(out) > 0 {
		p.EffortValues = out
	}
}

// overlayBudget tightens the documented budget range and records the "let the
// model decide" value. The minimum is only raised for models that cannot be
// disabled, so zero-budget "off" (Gemini Flash) keeps working.
func overlayBudget(p *Profile, caps models.Capabilities) {
	if p.BudgetPath == "" || !caps.HasBudget {
		return
	}
	if caps.BudgetMin > p.BudgetMin && !caps.CanDisable {
		p.BudgetMin = caps.BudgetMin
	}
	if caps.BudgetMax > 0 && (p.BudgetMax == 0 || caps.BudgetMax < p.BudgetMax) {
		p.BudgetMax = caps.BudgetMax
	}
	if caps.Adaptive && p.AdaptiveBudget == 0 {
		p.AdaptiveBudget = -1
	}
}

// usesUpperLabels reports whether the table spells its effort levels in upper
// case (Gemini's thinkingLevel), so catalog labels can be mapped to the same
// namespace.
func usesUpperLabels(values []string) bool {
	for _, v := range values {
		if v == "" {
			continue
		}
		return v == strings.ToUpper(v)
	}
	return false
}

func hasFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}
