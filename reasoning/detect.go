package reasoning

import (
	"net/url"
	"strings"
)

// Family groups models that share a thinking API shape.
type Family string

const (
	FamilyUnknown   Family = ""
	FamilyAnthropic Family = "anthropic"
	FamilyOpenAI    Family = "openai"
	FamilyGemini    Family = "gemini"
	FamilyGemma     Family = "gemma"
	FamilyOllama    Family = "ollama"
	FamilyDeepSeek  Family = "deepseek"
	FamilyGLM       Family = "glm"
	FamilyQwen      Family = "qwen"
	FamilyMistral   Family = "mistral"
	FamilyKimi      Family = "kimi"
	FamilyMiniMax   Family = "minimax"
	FamilyLocal     Family = "local"
)

// Detection records how a model was identified, so the /reasoning UI can warn
// when the guess is weak.
type Detection struct {
	Family   Family
	Provider Family // resolved from the host, when known
	// Source describes which signal produced the result: "model", "host",
	// "host+model", "override" or "none".
	Source    string
	Confident bool // false makes the UI show the "model not recognised" warning
}

// FamilyFromModel applies the name heuristics from Appendix A.10. It is a
// best-effort guess: callers must treat an empty result as "unknown" and fall
// back to the defensive default.
func FamilyFromModel(model string) Family {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return FamilyUnknown
	}
	// Model ids arrive in a few shapes: routers prefix a vendor
	// ("anthropic/claude-sonnet-4.5", "meta-llama/Llama-3.3-70B") and some
	// append a route ("model@provider"). Keep the model segment: drop
	// everything up to the last "/" and any "@…" suffix. A ":" is left alone
	// because it is a legitimate tag ("qwen3:8b") on local servers.
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, "@"); i > 0 {
		m = m[:i]
	}
	switch {
	case strings.Contains(m, "claude") || strings.Contains(m, "fable") || strings.Contains(m, "mythos"):
		return FamilyAnthropic
	case strings.HasPrefix(m, "gemini"):
		return FamilyGemini
	case strings.HasPrefix(m, "gemma"):
		return FamilyGemma
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"),
		strings.HasPrefix(m, "chatgpt"), strings.HasPrefix(m, "codex"):
		// "codex" covers the Codex-only names that carry no gpt- prefix
		// (codex-auto-review, codex-mini-latest…): they are OpenAI models and
		// take the same Responses reasoning fields.
		return FamilyOpenAI
	case strings.Contains(m, "deepseek"):
		return FamilyDeepSeek
	case strings.HasPrefix(m, "glm"), strings.Contains(m, "zai"):
		return FamilyGLM
	case strings.Contains(m, "qwen"), strings.HasPrefix(m, "qwq"):
		return FamilyQwen
	case strings.Contains(m, "mistral"), strings.Contains(m, "magistral"), strings.Contains(m, "devstral"):
		return FamilyMistral
	case strings.Contains(m, "kimi"), strings.Contains(m, "moonshot"):
		return FamilyKimi
	case strings.Contains(m, "minimax"):
		return FamilyMiniMax
	case strings.Contains(m, "thinking"), strings.Contains(m, "reasoning"):
		// Generic hint: a reasoning model of unknown family.
		return FamilyUnknown
	}
	return FamilyUnknown
}

// ProviderFromHost resolves a family from a base URL host, the strongest signal
// for known endpoints.
func ProviderFromHost(host string) Family {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return FamilyUnknown
	}
	switch {
	case endpointHostname(h) == "ollama.com":
		return FamilyOllama
	case strings.Contains(h, "deepseek.com"):
		return FamilyDeepSeek
	case strings.Contains(h, "z.ai"):
		return FamilyGLM
	case strings.Contains(h, "moonshot"):
		return FamilyKimi
	case strings.Contains(h, "dashscope.aliyuncs.com"):
		return FamilyQwen
	case strings.Contains(h, "mistral.ai"):
		return FamilyMistral
	case strings.Contains(h, "minimax"):
		return FamilyMiniMax
	case strings.Contains(h, "generativelanguage.googleapis.com"):
		return FamilyGemini
	case strings.Contains(h, "anthropic.com"):
		return FamilyAnthropic
	case strings.Contains(h, "openai.com"):
		return FamilyOpenAI
	case isLocalHost(h):
		return FamilyLocal
	}
	return FamilyUnknown
}

// isLocalHost reports whether the host points at a local inference server
// (vLLM, SGLang, Ollama, llama.cpp), which is generally tolerant of extra body
// fields and where the model name is the only reliable signal.
func isLocalHost(h string) bool {
	switch endpointHostname(h) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "host.docker.internal":
		return true
	}
	return false
}

func endpointHostname(raw string) string {
	if raw == "::1" {
		return raw
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Detect combines the host and model signals into a single Detection.
func Detect(host, model string) Detection {
	provider := ProviderFromHost(host)
	family := FamilyFromModel(model)

	switch {
	case family != FamilyUnknown && provider != FamilyUnknown && family == provider:
		return Detection{Family: family, Provider: provider, Source: "host+model", Confident: true}
	case family != FamilyUnknown:
		return Detection{Family: family, Provider: provider, Source: "model", Confident: true}
	case provider != FamilyUnknown && provider != FamilyLocal:
		return Detection{Family: provider, Provider: provider, Source: "host", Confident: true}
	default:
		// A local server is not evidence of a provider (any OpenAI-compatible
		// shim answers there), so Confident stays false — but the Local family
		// is the tolerant profile for exactly this case. Without it the model
		// would fall through to the empty profile and no control would ever be
		// sent, not even the effort the user picked.
		if provider == FamilyLocal {
			return Detection{Family: FamilyLocal, Provider: provider, Source: "none", Confident: false}
		}
		return Detection{Provider: provider, Source: "none", Confident: false}
	}
}
