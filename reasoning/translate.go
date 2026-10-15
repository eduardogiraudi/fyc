package reasoning

import (
	"fmt"
	"strings"
)

// OmitAll is the sentinel an EffortMap entry uses to drop the effort field
// entirely instead of mapping it to a wire value.
const OmitAll = "__omit__"

// Plan is the transport-agnostic output of Translate: the request fields to set
// (as dotted paths) plus the field paths the caller must not send. A nil
// value in Set deletes the key, so an override can clear a field a smaller
// profile set earlier.
type Plan struct {
	Set      map[string]any
	Omit     []string
	Warnings []string
}

// set stores value at a dotted path, creating intermediate objects as needed.
func (p *Plan) set(path string, value any) {
	if path == "" {
		return
	}
	if p.Set == nil {
		p.Set = map[string]any{}
	}
	segments := strings.Split(path, ".")
	m := p.Set
	for i, seg := range segments {
		if i == len(segments)-1 {
			m[seg] = value
			return
		}
		child, ok := m[seg].(map[string]any)
		if !ok {
			child = map[string]any{}
			m[seg] = child
		}
		m = child
	}
}

// Apply merges Set into a request body, preserving sibling fields in nested
// objects, then deletes every dotted path in Omit.
func (p Plan) Apply(body map[string]any) {
	for path, value := range p.Set {
		setInMap(body, strings.Split(path, "."), value)
	}
	for _, path := range p.Omit {
		deleteInMap(body, strings.Split(path, "."))
	}
}

func setInMap(m map[string]any, path []string, value any) {
	if len(path) == 1 {
		if fields, ok := value.(map[string]any); ok {
			child, ok := m[path[0]].(map[string]any)
			if !ok {
				child = map[string]any{}
				m[path[0]] = child
			}
			for key, value := range fields {
				setInMap(child, []string{key}, value)
			}
		} else {
			m[path[0]] = value
		}
		return
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[path[0]] = child
	}
	setInMap(child, path[1:], value)
}

func deleteInMap(m map[string]any, path []string) {
	if len(path) == 1 {
		delete(m, path[0])
		return
	}
	if child, ok := m[path[0]].(map[string]any); ok {
		deleteInMap(child, path[1:])
	}
}

// Translate turns a user Intent plus a resolved Profile into the concrete Plan
// for the request. It never errors on an unsupported control: it records a
// Warning and omits the field, so the defensive default (send nothing you are
// unsure about) holds even for unknown models.
func Translate(intent Intent, profile Profile) (Plan, error) {
	plan := Plan{Set: map[string]any{}}

	// Always-on fields first, so mode-specific ones can override them.
	for path, value := range profile.Always {
		plan.set(path, value)
	}

	var control bool
	switch intent.Mode {
	case ModeDefault:
		// Defensive: contribute nothing beyond Always.
	case ModeOff:
		control = translateOff(&plan, profile)
	case ModeAdaptive:
		control = translateAdaptive(&plan, profile)
	case ModeEffort:
		control = translateEffort(&plan, profile, intent.Effort)
	case ModeBudget:
		control = translateBudget(&plan, profile, intent.Budget)
	default:
		return plan, fmt.Errorf("reasoning: unknown mode %d", int(intent.Mode))
	}

	// Fields that are only meaningful once a control has actually been emitted
	// (e.g. reasoning.summary on the Responses API: it must not be sent when
	// thinking is off).
	if control {
		for path, value := range profile.AlongsideControl {
			plan.set(path, value)
		}
	}

	// Temperature: phase-3 Anthropic models reject it on every request, but the
	// phase-1/2 restriction ("temperature is incompatible with thinking") applies
	// only while thinking is on, so "off" and "default" keep the user's value.
	thinkingOn := profile.ForbidsTemperatureWithThinking &&
		intent.Mode != ModeDefault && intent.Mode != ModeOff
	if profile.ForbidsTemperature || thinkingOn {
		path := "temperature"
		if profile.Transport == GeminiGenerateContent {
			path = "generationConfig.temperature"
		}
		plan.Omit = append(plan.Omit, path)
	}

	// Escape hatch last: user-provided values always win.
	for path, value := range intent.ExtraBody {
		plan.set(path, value)
	}

	return plan, nil
}

// Each translate* returns whether it emitted a control field (effort or budget),
// which decides whether the profile's AlongsideControl fields are added.

func translateOff(plan *Plan, p Profile) bool {
	switch {
	case p.CannotDisable:
		plan.Warnings = append(plan.Warnings, "this model always thinks; the toggle is omitted")
		return false
	case p.TogglePath != "" && p.ToggleDisabledValue != nil:
		plan.set(p.TogglePath, p.ToggleDisabledValue)
		return false
	case p.EffortPath != "":
		if label, ok := p.EffortMap["none"]; ok {
			if label == OmitAll {
				return false
			}
			plan.set(p.EffortPath, label)
			return true
		}
		plan.Warnings = append(plan.Warnings, "thinking cannot be disabled on this model")
		return false
	case p.BudgetPath != "":
		// Some transports disable via a budget of zero (Gemini Flash).
		if budget, ok := p.BudgetFromEffort["none"]; ok {
			plan.set(p.BudgetPath, clamp(budget, p.BudgetMin, p.BudgetMax))
			return true
		}
		plan.Warnings = append(plan.Warnings, "thinking cannot be disabled on this model")
		return false
	default:
		plan.Warnings = append(plan.Warnings, "thinking cannot be disabled on this model")
		return false
	}
}

func translateAdaptive(plan *Plan, p Profile) bool {
	switch {
	case p.TogglePath != "" && p.ToggleAdaptiveValue != nil:
		plan.set(p.TogglePath, p.ToggleAdaptiveValue)
		return false
	// Budget-as-adaptive is only the encoding when the model has no adaptive
	// toggle (Gemini's -1 = dynamic). SupportsAdaptive keeps this in sync with
	// the /reasoning menu.
	case p.SupportsAdaptive():
		plan.set(p.BudgetPath, p.AdaptiveBudget) // e.g. -1 = dynamic
		return true
	default:
		plan.Warnings = append(plan.Warnings, "adaptive thinking is not supported; using the model default")
		return false
	}
}

func translateEffort(plan *Plan, p Profile, effort string) bool {
	if effort == "" {
		plan.Warnings = append(plan.Warnings, "no effort level selected")
		return false
	}
	wire := effort
	if mapped, ok := p.EffortMap[effort]; ok {
		if mapped == OmitAll {
			return false
		}
		wire = mapped
	}

	switch {
	case p.EffortPath != "":
		if len(p.EffortValues) > 0 && !contains(p.EffortValues, wire) {
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("effort %q is not in the known set %v for this model; sending it anyway", wire, p.EffortValues))
		}
		plan.set(p.EffortPath, wire)
		if p.ToggleFromControl && p.TogglePath != "" && p.ToggleEnabledValue != nil {
			plan.set(p.TogglePath, p.ToggleEnabledValue)
		}
		return true
	case p.BudgetPath != "":
		budget, ok := p.BudgetFromEffort[effort]
		if !ok {
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("effort %q has no budget mapping for this model", effort))
			return false
		}
		plan.set(p.BudgetPath, clamp(budget, p.BudgetMin, p.BudgetMax))
		if p.ToggleFromControl && p.TogglePath != "" && p.ToggleEnabledValue != nil {
			plan.set(p.TogglePath, p.ToggleEnabledValue)
		}
		return true
	default:
		plan.Warnings = append(plan.Warnings, "this model does not expose an effort control")
		return false
	}
}

func translateBudget(plan *Plan, p Profile, budget int) bool {
	if p.BudgetPath == "" {
		plan.Warnings = append(plan.Warnings, "this model does not expose a token budget")
		return false
	}
	if budget <= 0 {
		plan.Warnings = append(plan.Warnings, "budget must be positive; using the model default")
		return false
	}
	plan.set(p.BudgetPath, clamp(budget, p.BudgetMin, p.BudgetMax))
	if p.ToggleFromControl && p.TogglePath != "" && p.ToggleEnabledValue != nil {
		plan.set(p.TogglePath, p.ToggleEnabledValue)
	}
	return true
}

func clamp(v, min, max int) int {
	if min != 0 && v < min {
		v = min
	}
	if max != 0 && v > max {
		v = max
	}
	return v
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
