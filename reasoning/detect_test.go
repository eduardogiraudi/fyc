package reasoning

import "testing"

func TestFamilyFromModel(t *testing.T) {
	cases := map[string]Family{
		"claude-sonnet-4-5":  FamilyAnthropic,
		"gpt-5.5":            FamilyOpenAI,
		"o3-mini":            FamilyOpenAI,
		"gemini-2.5-flash":   FamilyGemini,
		"gemini-3-pro":       FamilyGemini,
		"deepseek-v4-pro":    FamilyDeepSeek,
		"glm-5.3":            FamilyGLM,
		"qwen3-32b-thinking": FamilyQwen,
		"qwq-32b":            FamilyQwen,
		"magistral-small":    FamilyMistral,
		"kimi-k2":            FamilyKimi,
		"minimax-m2":         FamilyMiniMax,
		// Codex-only names carry no gpt- prefix but are OpenAI models.
		"codex-auto-review": FamilyOpenAI,
		"codex-mini-latest": FamilyOpenAI,
		"mystery-model":     FamilyUnknown,
		// Routers prefix the vendor with "/" (OpenRouter) and some append a
		// route with "@"; the model segment is what must be recognised.
		"anthropic/claude-sonnet-4.5": FamilyAnthropic,
		"google/gemini-2.5-flash":     FamilyGemini,
		"meta-llama/Llama-3.3-70B":    FamilyUnknown,
		"claude-3@vertex":             FamilyAnthropic,
		// ":" is a legitimate tag on local servers, never a route separator.
		"qwen3:8b":              FamilyQwen,
		"qwen3:235b-cloud":      FamilyQwen,
		"gemma4:31b-cloud":      FamilyGemma,
		"gemma4:cloud":          FamilyGemma,
		"gemma4-cloud":          FamilyGemma,
		"google/gemma-4-31b-it": FamilyGemma,
		"gemma3:27b":            FamilyGemma,
		"unknown-cloud":         FamilyUnknown,
	}
	for model, want := range cases {
		if got := FamilyFromModel(model); got != want {
			t.Errorf("FamilyFromModel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestProviderFromHost(t *testing.T) {
	cases := map[string]Family{
		"https://api.deepseek.com/v1":           FamilyDeepSeek,
		"https://api.z.ai/api/paas/v4":          FamilyGLM,
		"https://api.moonshot.cn/v1":            FamilyKimi,
		"https://dashscope.aliyuncs.com/compat": FamilyQwen,
		"http://localhost:8000/v1":              FamilyLocal,
		"http://127.0.0.1:11434/v1":             FamilyLocal,
		"http://[::1]:11434/v1":                 FamilyLocal,
		"https://ollama.com/v1":                 FamilyOllama,
		"ollama.com":                            FamilyOllama,
		"ollama.com:443":                        FamilyOllama,
		" HTTPS://OLLAMA.COM/v1/ ":              FamilyOllama,
		"https://ollama.com.evil.test/v1":       FamilyUnknown,
		"https://proxy.test/ollama.com/v1":      FamilyUnknown,
		"https://api.openai.com/v1":             FamilyOpenAI,
		"https://api.anthropic.com":             FamilyAnthropic,
		"https://my-company-proxy.internal/v1":  FamilyUnknown,
	}
	for host, want := range cases {
		if got := ProviderFromHost(host); got != want {
			t.Errorf("ProviderFromHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestDetectConfidence(t *testing.T) {
	// Host and model agree: confident.
	if d := Detect("https://api.deepseek.com", "deepseek-v4-pro"); !d.Confident || d.Source != "host+model" {
		t.Fatalf("expected confident host+model, got %+v", d)
	}
	// Only the model is known.
	if d := Detect("", "gemini-2.5-pro"); !d.Confident || d.Source != "model" {
		t.Fatalf("expected confident model detection, got %+v", d)
	}
	// Neither signal: not confident, so the UI must warn.
	if d := Detect("https://my-proxy.internal", "unknown-llm"); d.Confident {
		t.Fatalf("expected unconfident detection, got %+v", d)
	}
	// Localhost alone is not enough to claim a provider, but it does select the
	// tolerant local profile: without a family the model would get the empty
	// profile and no control (not even the effort) would be sent.
	if d := Detect("http://localhost:11434/v1", "unknown-llm"); d.Confident {
		t.Fatalf("localhost + unknown model should not be confident, got %+v", d)
	} else if d.Family != FamilyLocal {
		t.Fatalf("expected the local fallback family, got %+v", d)
	}
}
