package providers

import (
	"encoding/json"

	"fuckyourcli/reasoning"
)

// legacyReasoningIntent bridges the pre-/reasoning configuration (a boolean plus
// an effort string) onto the new Intent, so existing callers keep working until
// the /reasoning UI feeds an Intent directly.
func legacyReasoningIntent(enabled bool, effort string) reasoning.Intent {
	if !enabled {
		// Defensive: send nothing, exactly like the old "thinking off".
		return reasoning.Intent{Mode: reasoning.ModeDefault}
	}
	if effort == "" {
		return reasoning.Intent{Mode: reasoning.ModeAdaptive}
	}
	return reasoning.Intent{Mode: reasoning.ModeEffort, Effort: effort}
}

// applyReasoningPlan merges a reasoning.Plan into an already-marshaled request
// body. It round-trips through a map because the plan addresses fields by dotted
// path, including ones absent from the typed request struct (extra_body,
// provider-specific knobs).
func applyReasoningPlan(body []byte, plan reasoning.Plan) ([]byte, error) {
	if len(plan.Set) == 0 && len(plan.Omit) == 0 {
		return body, nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, err
	}
	plan.Apply(m)
	return json.Marshal(m)
}
