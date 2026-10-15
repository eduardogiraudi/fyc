package reasoning

import (
	"fmt"
	"sort"
	"strings"
)

// This file holds the "auto wizard" knowledge: given a Profile that has already
// been resolved (Lookup), it answers the three questions the /reasoning UI used
// to make the user reason about:
//
//   - which modes can this model actually express? (Choices)
//   - which modes and levels can this model actually express? (Choices, Levels)
//   - which effort labels map to a real wire value? (EffortLevels)
//   - what will actually be sent if I pick this? (Preview)
//
// All of it is data derived from the Profile, so the menu can never offer a
// choice that Translate would turn into a warning plus nothing.

// Choice is one row of the wizard: a mode the resolved Profile can express, with
// the concrete wire effect spelled out in the label.
type Choice struct {
	Key   string // "default", "off", "adaptive", "effort:<level>"
	Label string
}

// canonicalLevels is the graded scale shared by every provider, shallow-to-deep.
var canonicalLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// Choices lists the wizard rows for a resolved Profile, in menu order: the
// defensive default, then Off / Adaptive when the model supports them, then one
// row per graded level. Only what the profile can actually encode is returned,
// so the wizard never shows a dead option. Level keys are "effort:<level>".
func Choices(p Profile) []Choice {
	choices := []Choice{{Key: "default", Label: "Default — send nothing (the server decides)"}}
	if canDisable(p) {
		choices = append(choices, Choice{Key: "off", Label: "Off — " + offTarget(p)})
	}
	if p.SupportsAdaptive() {
		choices = append(choices, Choice{Key: "adaptive", Label: "Adaptive — " + adaptiveTarget(p)})
	}
	for _, level := range Levels(p) {
		choices = append(choices, Choice{Key: "effort:" + level, Label: levelLabel(level, p)})
	}
	return choices
}

// Levels lists the graded levels the profile can express, in scale order
// (minimal → max). A profile that accepts any label (no documented set) gets the
// full canonical scale; one with no graded surface at all (a plain toggle) gets
// nothing. An empty result means "there is no depth to pick, only on/off".
func Levels(p Profile) []string {
	if !effortSurface(p) {
		return nil
	}
	accept := acceptableLabels(p)
	if len(accept) == 0 {
		return append([]string(nil), canonicalLevels...)
	}
	var out []string
	for _, level := range canonicalLevels {
		if accept[level] {
			out = append(out, level)
		}
	}
	if len(out) == 0 {
		return EffortLevels(p) // no canonical overlap: keep the model's own labels
	}
	return out
}

// acceptableLabels is the set of lowercase labels the profile can encode: the
// mapping keys (user labels) plus the documented wire values (sent verbatim).
func acceptableLabels(p Profile) map[string]bool {
	set := map[string]bool{}
	for k, v := range p.EffortMap {
		if k != "" && v != OmitAll {
			set[strings.ToLower(k)] = true
		}
	}
	for _, v := range p.EffortValues {
		if v != "" {
			set[strings.ToLower(v)] = true
		}
	}
	if len(set) == 0 {
		for k := range p.BudgetFromEffort {
			if k != "" {
				set[strings.ToLower(k)] = true
			}
		}
	}
	return set
}

// levelLabel renders one level row: its title plus the concrete wire effect.
func levelLabel(level string, p Profile) string {
	detail := level
	path := effortTarget(p)
	if plan, err := Translate(Intent{Mode: ModeEffort, Effort: level}, p); err == nil {
		for _, kv := range planPairs(plan) {
			if path != "" && strings.HasPrefix(kv, path+"=") {
				detail = kv
				break
			}
		}
	}
	return levelTitle(level) + " — " + detail
}

var levelTitles = map[string]string{
	"minimal": "Minimal", "low": "Low", "medium": "Medium",
	"high": "High", "xhigh": "Extra high", "max": "Max",
}

func levelTitle(level string) string {
	if t, ok := levelTitles[strings.ToLower(level)]; ok {
		return t
	}
	if level == "" {
		return "Level"
	}
	return strings.ToUpper(level[:1]) + level[1:]
}

// EffortLevels lists the effort labels the profile can encode, shallow-to-deep.
// When the profile carries a mapping, its keys are the user-facing labels (the
// wire spelling may differ); otherwise the documented wire values are used. An
// empty result means "ask the catalog or fall back to the fixed list". "none" is
// dropped: it is the Off row's business, not a depth.
func EffortLevels(p Profile) []string {
	var levels []string
	if len(p.EffortMap) > 0 {
		for k, v := range p.EffortMap {
			if k != "" && k != "none" && v != OmitAll {
				levels = append(levels, k)
			}
		}
	} else {
		for _, v := range p.EffortValues {
			if v != "" && !strings.EqualFold(v, "none") {
				levels = append(levels, v)
			}
		}
	}
	if len(levels) == 0 {
		for k := range p.BudgetFromEffort {
			if k != "" && k != "none" {
				levels = append(levels, k)
			}
		}
	}
	sortEffortLevels(levels)
	return levels
}

// Preview renders the concrete request fields a selection produces, as
// "path=value" pairs, with omitted fields as "omit <path>". An empty
// result means the selection adds nothing to the request.
func Preview(in Intent, p Profile) string {
	plan, err := Translate(in, p)
	if err != nil {
		return ""
	}
	return FormatPlan(plan)
}

// FormatPlan flattens a Plan into a stable, human-readable string.
func FormatPlan(plan Plan) string {
	return strings.Join(planPairs(plan), ", ")
}

// planPairs flattens a Plan's Set into sorted "path=value" pairs, then appends
// the omitted field paths as "omit <path>".
func planPairs(plan Plan) []string {
	var parts []string
	for path, value := range plan.Set {
		parts = append(parts, flattenPath(path, value)...)
	}
	sort.Strings(parts)
	omits := append([]string(nil), plan.Omit...)
	sort.Strings(omits)
	for _, omit := range omits {
		parts = append(parts, "omit "+omit)
	}
	return parts
}

func flattenPath(path string, value any) []string {
	if m, ok := value.(map[string]any); ok {
		var out []string
		for k, v := range m {
			out = append(out, flattenPath(path+"."+k, v)...)
		}
		return out
	}
	return []string{fmt.Sprintf("%s=%v", path, value)}
}

// Summary renders a resolved Profile for the /reasoning header, one clause per
// control. It returns "" for a profile that exposes nothing (unknown model).
func (p Profile) Summary() string {
	var parts []string
	if p.TogglePath != "" {
		var vals []string
		for _, v := range []any{p.ToggleEnabledValue, p.ToggleDisabledValue, p.ToggleAdaptiveValue} {
			if v != nil {
				vals = append(vals, fmt.Sprint(v))
			}
		}
		parts = append(parts, p.TogglePath+"("+strings.Join(vals, "|")+")")
	}
	if p.EffortPath != "" {
		clause := p.EffortPath
		if levels := EffortLevels(p); len(levels) > 0 {
			clause += "(" + strings.Join(levels, "|") + ")"
		}
		parts = append(parts, clause)
	}
	if p.BudgetPath != "" {
		parts = append(parts, budgetTarget(p))
	}
	if p.CannotDisable {
		parts = append(parts, "cannot disable")
	}
	if p.ForbidsTemperature {
		parts = append(parts, "no temperature")
	}
	if p.ForbidsTemperatureWithThinking {
		parts = append(parts, "no temperature while thinking")
	}
	return strings.Join(parts, " · ")
}

// canDisable mirrors translateOff: true only when the profile actually encodes
// "off" (a disabled toggle, an effort "none" mapping, or a zero budget).
func canDisable(p Profile) bool {
	if p.CannotDisable {
		return false
	}
	switch {
	case p.TogglePath != "" && p.ToggleDisabledValue != nil:
		return true
	case p.EffortPath != "":
		v, ok := p.EffortMap["none"]
		return ok && v != OmitAll
	case p.BudgetPath != "":
		_, ok := p.BudgetFromEffort["none"]
		return ok
	}
	return false
}

// effortSurface mirrors translateEffort: an effort is expressible either as a
// named field or as a token budget derived from the label.
func effortSurface(p Profile) bool {
	return p.EffortPath != "" || len(p.BudgetFromEffort) > 0
}

func offTarget(p Profile) string {
	switch {
	case p.TogglePath != "" && p.ToggleDisabledValue != nil:
		return fmt.Sprintf("%s=%v", p.TogglePath, p.ToggleDisabledValue)
	case p.EffortPath != "":
		if v, ok := p.EffortMap["none"]; ok {
			return fmt.Sprintf("%s=%s", p.EffortPath, v)
		}
	case p.BudgetPath != "":
		if v, ok := p.BudgetFromEffort["none"]; ok {
			return fmt.Sprintf("%s=%d", p.BudgetPath, v)
		}
	}
	return "thinking off"
}

func adaptiveTarget(p Profile) string {
	if p.TogglePath != "" && p.ToggleAdaptiveValue != nil {
		return fmt.Sprintf("%s=%v", p.TogglePath, p.ToggleAdaptiveValue)
	}
	return fmt.Sprintf("%s=%d", p.BudgetPath, p.AdaptiveBudget)
}

func effortTarget(p Profile) string {
	if p.EffortPath != "" {
		return p.EffortPath
	}
	return p.BudgetPath
}

func budgetTarget(p Profile) string {
	if p.BudgetMin != 0 || p.BudgetMax != 0 {
		return fmt.Sprintf("%s (%d..%d)", p.BudgetPath, p.BudgetMin, p.BudgetMax)
	}
	return p.BudgetPath
}

// effortOrder ranks the nominal levels shallow-to-deep; unknown labels sort
// after the known ones, alphabetically.
var effortOrder = map[string]int{
	"minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6,
}

func sortEffortLevels(levels []string) {
	sort.SliceStable(levels, func(i, j int) bool {
		ri, iok := effortOrder[strings.ToLower(levels[i])]
		rj, jok := effortOrder[strings.ToLower(levels[j])]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		default:
			return levels[i] < levels[j]
		}
	})
}
