package reasoning

import (
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/models"
)

func TestOllamaCloudProfiles(t *testing.T) {
	for _, host := range []string{"https://ollama.com/v1", "ollama.com", "https://ollama.com/v1/"} {
		for _, model := range []string{"gemma4:31b-cloud", "gemma4:31b", "gemma4:cloud", "qwen3:235b-cloud", "gpt-oss:120b-cloud", "deepseek-v4-pro-cloud", "unknown-model"} {
			for _, transport := range []Transport{ChatCompletions, OpenAIResponses} {
				t.Run(host+"/"+model+"/"+string(transport), func(t *testing.T) {
					p, det := Lookup(transport, host, model, nil)
					if !det.Confident || det.Provider != FamilyOllama {
						t.Fatalf("Ollama Cloud not detected: %+v", det)
					}
					if p.Model != model {
						t.Fatalf("request model changed: %q", p.Model)
					}
					body := apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p)
					off := apply(t, Intent{Mode: ModeOff}, p)
					if transport == ChatCompletions {
						if body["reasoning_effort"] != "high" || off["reasoning_effort"] != "none" {
							t.Fatalf("wrong chat control: on=%v off=%v", body, off)
						}
					} else if at(body, "reasoning", "effort") != "high" || off["think"] != false {
						t.Fatalf("wrong Responses control: on=%v off=%v", body, off)
					}
					if len(body) != 1 || len(off) != 1 {
						t.Fatalf("unexpected vendor fields: on=%v off=%v", body, off)
					}
					if body := apply(t, Intent{Mode: ModeDefault}, p); len(body) != 0 {
						t.Fatalf("default should remain defensive: %v", body)
					}
				})
			}
		}
	}
}

func TestGemmaLocalDetection(t *testing.T) {
	for _, model := range []string{"gemma4:31b-cloud", "gemma4:31b", "gemma4:cloud", "gemma4-cloud"} {
		p, det := Lookup(OpenAIResponses, "http://localhost:11434/v1", model, nil)
		if !det.Confident || det.Family != FamilyGemma || det.Provider != FamilyLocal || det.Source != "model" {
			t.Fatalf("Gemma not recognised: %+v", det)
		}
		if got := at(apply(t, Intent{Mode: ModeEffort, Effort: "high"}, p), "reasoning", "effort"); got != "high" {
			t.Fatalf("local effort = %v, want high", got)
		}
	}
}

func TestOllamaCatalogCloudAlias(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	models.Invalidate()
	t.Cleanup(models.Invalidate)
	const doc = `{
		"302ai":{"id":"302ai","models":{"gemma4:31b-cloud":{"id":"gemma4:31b-cloud","reasoning":false}}},
		"ollama-cloud":{"id":"ollama-cloud","api":"https://ollama.com/v1","models":{
			"gemma4:31b":{"id":"gemma4:31b","reasoning":true,"reasoning_options":[{"type":"toggle"}]},
			"gpt-oss:120b":{"id":"gpt-oss:120b","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}]}
		}}
	}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := capabilitySource
	capabilitySource = CatalogCapabilities
	t.Cleanup(func() { capabilitySource = prev })
	for _, host := range []string{"https://ollama.com/v1", "ollama.com", "http://localhost:11434/v1"} {
		caps, ok := CatalogCapabilities(host, "gemma4:31b-cloud")
		if !ok || caps.ProviderID != "ollama-cloud" || caps.ModelID != "gemma4:31b" || !caps.Reasoning || !caps.Toggle {
			t.Fatalf("catalog alias failed for %s: %+v ok=%v", host, caps, ok)
		}
		p, _ := Lookup(OpenAIResponses, host, "gpt-oss:120b-cloud", nil)
		if len(p.EffortValues) != 2 || p.EffortValues[0] != "low" || p.EffortValues[1] != "high" {
			t.Fatalf("cloud alias overlay not applied: %+v", p)
		}
	}
}
