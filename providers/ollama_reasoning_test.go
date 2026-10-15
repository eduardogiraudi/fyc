package providers

import (
	"testing"

	"fuckyourcli/reasoning"
)

func TestAddResponsesReasoningOllamaCloud(t *testing.T) {
	for _, model := range []string{"gemma4:31b-cloud", "gemma4:31b", "qwen3:235b-cloud", "gpt-oss:120b-cloud"} {
		for _, override := range []*reasoning.Profile{nil, {EffortPath: "reasoning.effort", TogglePath: "think", ToggleDisabledValue: false}} {
			body := map[string]any{"model": model}
			addResponsesReasoning(body, reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "high"}, override, model, "https://ollama.com/v1")
			if nestedString(body, "reasoning", "effort") != "high" || body["model"] != model {
				t.Fatalf("wrong cloud request: %v", body)
			}
			if _, ok := body["include"]; ok {
				t.Fatalf("Ollama Cloud cannot supply encrypted reasoning: %v", body)
			}
			off := map[string]any{}
			addResponsesReasoning(off, reasoning.Intent{Mode: reasoning.ModeOff}, override, model, "https://ollama.com/v1")
			if off["think"] != false {
				t.Fatalf("wrong off control: %v", off)
			}
		}
	}
}
