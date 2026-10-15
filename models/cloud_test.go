package models

import "testing"

func TestCloudModelAliases(t *testing.T) {
	c := parseBytes([]byte(`{
		"302ai":{"id":"302ai","models":{"gemma4:31b-cloud":{"id":"gemma4:31b-cloud","reasoning":false}}},
		"ollama-cloud":{"id":"ollama-cloud","models":{
			"gemma4:31b":{"id":"gemma4:31b","reasoning":true},
			"gemma4":{"id":"gemma4","reasoning":true},
			"qwen3:235b":{"id":"qwen3:235b","reasoning":true},
			"qwen3:235b-cloud":{"id":"qwen3:235b-cloud","reasoning":false},
			"deepseek-v4-pro":{"id":"deepseek-v4-pro","reasoning":true}
		}}
	}`))
	for _, tc := range []struct{ input, want string }{
		{"gemma4:31b-cloud", "gemma4:31b"},
		{"gemma4:cloud", "gemma4"},
		{"gemma4-cloud", "gemma4"},
		{"  gemma4:31b-cloud  ", "gemma4:31b"},
		{"qwen3:235b-cloud", "qwen3:235b-cloud"},
		{"deepseek-v4-pro-cloud", "deepseek-v4-pro"},
	} {
		caps, ok := c.CapabilitiesForProvider("ollama-cloud", tc.input)
		if !ok || caps.ModelID != tc.want || caps.ProviderID != "ollama-cloud" {
			t.Errorf("provider alias %q = %+v ok=%v, want %s", tc.input, caps, ok, tc.want)
		}
	}
	// An exact catalog ID wins globally; aliases must not replace real entries.
	if pid, m, ok := c.LookupModel("gemma4:31b-cloud"); !ok || pid != "302ai" || m.Reasoning {
		t.Fatalf("exact match lost: %s %+v ok=%v", pid, m, ok)
	}
	if _, m, ok := c.LookupModel("gemma4:cloud"); !ok || m.ID != "gemma4" {
		t.Fatalf("global alias lookup failed: %+v ok=%v", m, ok)
	}
	for _, id := range []string{"unknown-cloud", "gemma4:31b-q4", "gemma4:31b-cloud-extra", "cloud-gemma4:31b", ""} {
		if _, _, ok := c.LookupModel(id); ok {
			t.Errorf("must not strip model/quantization tags: %q", id)
		}
	}
}
