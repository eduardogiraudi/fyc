package providers

import (
	"fuckyourcli/logger"
	"fuckyourcli/types"
)

// StreamResult represents the result of a streaming chat completion
type StreamResult = types.StreamResult

type Provider = types.Provider

type OutputSink = types.OutputSink

// WithLogging wraps every supported transport at the shared Provider boundary.
func WithLogging(p types.Provider) types.Provider {
	var meta logger.Metadata
	var secret string
	switch p := p.(type) {
	case *OpenAICompatibleProvider:
		meta = logger.Metadata{Provider: "openai_compatible", Name: p.Name, Model: p.Model}
		secret = p.APIKey
	case *OpenAIResponsesCompatibleProvider:
		meta = logger.Metadata{Provider: "openai_responses_compatible", Name: p.Name, Model: p.Model}
		secret = p.APIKey
	case *AnthropicProvider:
		meta = logger.Metadata{Provider: "anthropic", Name: p.Name, Model: p.Model}
		secret = p.APIKey
	case *GeminiProvider:
		meta = logger.Metadata{Provider: "gemini", Model: p.Model}
		secret = p.APIKey
	case *CodexProvider:
		meta = logger.Metadata{Provider: "codex", Model: p.Model}
		return logger.WrapWithSecrets(p, meta, func() []string { return []string{p.AccessToken} })
	default:
		return p
	}
	return logger.Wrap(p, meta, secret)
}
