package config

import (
	"fmt"
	"strings"

	"fuckyourcli/reasoning"
)

// Intent converts the persisted preference into a reasoning.Intent. An unknown
// or empty Mode is treated as the defensive default.
func (r ReasoningConfig) Intent() reasoning.Intent {
	in := reasoning.Intent{
		Effort:    r.Effort,
		Budget:    r.Budget,
		ExtraBody: r.ExtraBody,
	}
	switch strings.ToLower(strings.TrimSpace(r.Mode)) {
	case "off":
		in.Mode = reasoning.ModeOff
	case "adaptive":
		in.Mode = reasoning.ModeAdaptive
	case "effort":
		in.Mode = reasoning.ModeEffort
	case "budget":
		in.Mode = reasoning.ModeBudget
	default:
		in.Mode = reasoning.ModeDefault
	}
	return in
}

// FromIntent is the inverse of Intent, used when persisting a choice made in
// the /reasoning UI.
func FromIntent(in reasoning.Intent) ReasoningConfig {
	rc := ReasoningConfig{Effort: in.Effort, Budget: in.Budget, ExtraBody: in.ExtraBody}
	switch in.Mode {
	case reasoning.ModeOff:
		rc.Mode = "off"
	case reasoning.ModeAdaptive:
		rc.Mode = "adaptive"
	case reasoning.ModeEffort:
		rc.Mode = "effort"
	case reasoning.ModeBudget:
		rc.Mode = "budget"
	default:
		rc.Mode = "default"
	}
	return rc
}

// Label renders the preference for the startup banner and the status line.
func (r ReasoningConfig) Label() string {
	switch strings.ToLower(strings.TrimSpace(r.Mode)) {
	case "off":
		return "off"
	case "adaptive":
		return "adaptive"
	case "effort":
		if r.Effort == "" {
			return "effort"
		}
		return "effort: " + r.Effort
	case "budget":
		return fmt.Sprintf("budget: %d tokens", r.Budget)
	default:
		return "default (server decides)"
	}
}

// IntentOr resolves the per-provider preference on top of the global one: every
// field the override sets wins, anything left empty is inherited. An empty Mode
// therefore means "use the global mode", which is what lets an entry override
// only the placement (Profile) or only one knob.
func (r ReasoningProviderConfig) IntentOr(global ReasoningConfig) ReasoningConfig {
	out := global
	if m := strings.TrimSpace(r.Intent.Mode); m != "" {
		out.Mode = r.Intent.Mode
	}
	if r.Intent.Effort != "" {
		out.Effort = r.Intent.Effort
	}
	if r.Intent.Budget != 0 {
		out.Budget = r.Intent.Budget
	}
	if r.Intent.ExtraBody != nil {
		out.ExtraBody = r.Intent.ExtraBody
	}
	return out
}

// ToProfile converts the typed placement override into the wire profile the
// reasoning package consumes. A nil receiver means "no override": the built-in
// table and the catalog stay in charge.
func (p *ReasoningProfileConfig) ToProfile() *reasoning.Profile {
	if p == nil {
		return nil
	}
	return &reasoning.Profile{
		TogglePath:          p.TogglePath,
		ToggleEnabledValue:  p.ToggleEnabledValue,
		ToggleDisabledValue: p.ToggleDisabledValue,
		ToggleAdaptiveValue: p.ToggleAdaptiveValue,
		CannotDisable:       p.CannotDisable,
		ToggleFromControl:   p.ToggleFromControl,

		EffortPath:   p.EffortPath,
		EffortValues: append([]string(nil), p.EffortValues...),
		EffortMap:    cloneStringMap(p.EffortMap),

		BudgetPath:       p.BudgetPath,
		BudgetMin:        p.BudgetMin,
		BudgetMax:        p.BudgetMax,
		BudgetFromEffort: cloneIntMap(p.BudgetFromEffort),
		AdaptiveBudget:   p.AdaptiveBudget,

		ForbidsTemperature: p.ForbidsTemperature,
		Always:             cloneAnyMap(p.Always),
		AlongsideControl:   cloneAnyMap(p.AlongsideControl),
	}
}

// IsZero reports whether the override carries nothing at all. Such an entry is
// dropped instead of being written to config.json.
func (r ReasoningProviderConfig) IsZero() bool {
	return strings.TrimSpace(r.Intent.Mode) == "" &&
		r.Intent.Effort == "" && r.Intent.Budget == 0 && len(r.Intent.ExtraBody) == 0 &&
		(r.Profile == nil || r.Profile.IsZero())
}

// IsZero reports whether the placement override sets no field.
func (p *ReasoningProfileConfig) IsZero() bool {
	if p == nil {
		return true
	}
	return p.TogglePath == "" && p.ToggleEnabledValue == nil &&
		p.ToggleDisabledValue == nil && p.ToggleAdaptiveValue == nil &&
		!p.CannotDisable && !p.ToggleFromControl &&
		p.EffortPath == "" && len(p.EffortValues) == 0 && len(p.EffortMap) == 0 &&
		p.BudgetPath == "" && p.BudgetMin == 0 && p.BudgetMax == 0 &&
		len(p.BudgetFromEffort) == 0 && p.AdaptiveBudget == 0 &&
		!p.ForbidsTemperature && len(p.Always) == 0 && len(p.AlongsideControl) == 0
}

// Summary renders the override for the /reasoning UI, one clause per configured
// part. It returns "" when the override is empty.
func (r ReasoningProviderConfig) Summary() string {
	var parts []string
	if m := strings.TrimSpace(r.Intent.Mode); m != "" {
		parts = append(parts, "mode="+r.Intent.Label())
	}
	if p := r.Profile; p != nil {
		if p.TogglePath != "" {
			parts = append(parts, "toggle="+p.TogglePath)
		}
		if p.EffortPath != "" {
			clause := "effort=" + p.EffortPath
			if len(p.EffortValues) > 0 {
				clause += "[" + strings.Join(p.EffortValues, "|") + "]"
			}
			parts = append(parts, clause)
		}
		if p.BudgetPath != "" {
			clause := "budget=" + p.BudgetPath
			if p.BudgetMin != 0 || p.BudgetMax != 0 {
				clause += fmt.Sprintf("[%d..%d]", p.BudgetMin, p.BudgetMax)
			}
			parts = append(parts, clause)
		}
		if p.ForbidsTemperature {
			parts = append(parts, "no temperature")
		}
		if p.CannotDisable {
			parts = append(parts, "cannot disable")
		}
		if len(p.Always) > 0 {
			parts = append(parts, fmt.Sprintf("always(%d)", len(p.Always)))
		}
	}
	return strings.Join(parts, " · ")
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
