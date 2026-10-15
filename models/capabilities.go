package models

import (
	"net/url"
	"sort"
	"strings"
)

// Capabilities is the reasoning-relevant summary of one catalog entry: what the
// /reasoning UI may offer and what the transport tables may safely assume. It is
// derived data, never stored.
//
// The zero value means "nothing known" (the model is absent from the catalog),
// so every caller falls back to its built-in table and the defensive default.
type Capabilities struct {
	ProviderID string // catalog provider that owns the model
	ModelID    string // entry id, which may be "org/model"

	Reasoning bool
	// Toggle, Effort and HasBudget mirror reasoning_options: the controls the
	// model documents. They may coexist on the same model.
	Toggle    bool
	Effort    []string // nominal levels, normalized (lowercase, deduped, ordered)
	HasBudget bool
	BudgetMin int // 0 when unset; -1 means "dynamic thinking" (Gemini 2.5)
	BudgetMax int // already capped to MaxOutput

	// Adaptive is the budget_tokens.min == -1 heuristic: the model picks the
	// depth itself. models.dev has no explicit "adaptive" field.
	Adaptive bool
	// CanDisable is true when the model documents a way to turn thinking off:
	// a toggle, an effort value "none", or a zero budget.
	CanDisable bool
	// NoOptions marks a model that reasons but exposes no documented control at
	// all (reasoning == true without reasoning_options).
	NoOptions bool

	MaxOutput          int
	ForbidsTemperature bool // only when the catalog explicitly says temperature:false
	InterleavedField   string
	Status             string // "deprecated", "beta", ...
}

// effortRank orders the nominal levels from the shallowest to the deepest.
// Unknown labels keep their relative order after the known ones.
var effortRank = map[string]int{
	"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6,
}

// Capabilities derives the reasoning summary of a single catalog entry.
func (m Model) Capabilities() Capabilities {
	c := Capabilities{
		ModelID:   m.ID,
		Reasoning: m.Reasoning,
		MaxOutput: m.Limit.Output,
		Status:    m.Status,
	}
	if m.Temperature != nil && !*m.Temperature {
		c.ForbidsTemperature = true
	}
	if m.Interleaved != nil {
		c.InterleavedField = m.Interleaved.Field
	}
	for _, opt := range m.ReasoningOptions {
		switch opt.Type {
		case "toggle":
			c.Toggle = true
		case "effort":
			c.Effort = normalizeEffort(opt.Values)
		case "budget_tokens":
			c.HasBudget = true
			if opt.Min != nil {
				c.BudgetMin = *opt.Min
				c.Adaptive = c.Adaptive || *opt.Min < 0
			}
			if opt.Max != nil {
				c.BudgetMax = *opt.Max
			}
		}
	}
	c.CanDisable = c.Toggle || containsFold(c.Effort, "none") ||
		(c.HasBudget && c.BudgetMin == 0)
	c.NoOptions = c.Reasoning && len(m.ReasoningOptions) == 0
	// The documented budget can never exceed the model's own output limit.
	if c.HasBudget && c.MaxOutput > 0 && (c.BudgetMax == 0 || c.BudgetMax > c.MaxOutput) {
		c.BudgetMax = c.MaxOutput
	}
	return c
}

// normalizeEffort lowercases, trims, dedupes and orders the nominal levels so
// the menu is stable regardless of the catalog's casing ("None" → "none").
func normalizeEffort(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return effortRankOf(out[i]) < effortRankOf(out[j]) })
	return out
}

func effortRankOf(label string) int {
	if r, ok := effortRank[label]; ok {
		return r
	}
	return len(effortRank) + 1
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}

// CapabilitiesFor resolves the capabilities of modelID using the cached
// catalog. found is false for a model the catalog does not know.
func CapabilitiesFor(modelID string) (Capabilities, bool) {
	return Cached().CapabilitiesFor(modelID)
}

// CapabilitiesFor resolves the capabilities of modelID inside this catalog.
func (c Catalog) CapabilitiesFor(modelID string) (Capabilities, bool) {
	pid, m, ok := c.LookupModel(modelID)
	if !ok {
		return Capabilities{}, false
	}
	caps := m.Capabilities()
	caps.ProviderID = pid
	return caps, true
}

// CapabilitiesForProvider prefers an entry inside providerID, then falls back to
// a global lookup: the provider name in fyc is a user hint, never a requirement.
func (c Catalog) CapabilitiesForProvider(providerID, modelID string) (Capabilities, bool) {
	modelID = strings.TrimSpace(modelID)
	if providerID != "" && modelID != "" {
		if p, ok := c[providerID]; ok {
			ids := []string{modelID}
			if alias := cloudModelID(modelID); alias != modelID {
				ids = append(ids, alias)
			}
			for _, id := range ids {
				if m, ok := p.Models[id]; ok {
					caps := m.Capabilities()
					caps.ProviderID = providerID
					return caps, true
				}
				suffix := "/" + id
				for _, m := range p.Models {
					if m.ID == id || m.CanonicalID == id || strings.HasSuffix(m.ID, suffix) ||
						(m.CanonicalID != "" && strings.HasSuffix(m.CanonicalID, suffix)) {
						caps := m.Capabilities()
						caps.ProviderID = providerID
						return caps, true
					}
				}
			}
		}
	}
	return c.CapabilitiesFor(modelID)
}

// ProviderIDForBaseURL returns the provider whose api field matches the host of
// baseURL, or "" when none does. api is only documented for OpenAI-compatible
// providers, so native endpoints simply fall through.
func (c Catalog) ProviderIDForBaseURL(baseURL string) string {
	host := hostOf(baseURL)
	if host == "" {
		return ""
	}
	for _, pid := range c.providerIDs() {
		if h := hostOf(c[pid].API); h != "" && h == host {
			return pid
		}
	}
	return ""
}

// hostOf extracts the lowercase host of a URL, tolerating values without a
// scheme (e.g. "api.deepseek.com/v1").
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
