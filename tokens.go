// Package main — context size and local token estimation.
//
// The compaction trigger measures the context the way the provider does: the
// prompt tokens reported by the last request (types.StreamResult.ContextTokens),
// extended with the locally estimated size of the messages appended since. The
// local estimate (a chars/4 BPE approximation) is only a fallback when there is
// no measurement and a sanity check when the measurement is implausible next to
// it (see contextTokens).
//
// Providers report prompt usage differently, so each one normalises the figure
// at parse time: Anthropic splits the prompt into input_tokens (the uncached
// tail), cache writes and cache reads, while OpenAI-compatible,
// Responses/Codex and Gemini already count the cached part in their prompt
// count.
package main

import (
	"encoding/json"
	"math"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// charsPerToken is the rough character-to-token ratio used by the estimator.
// It is an approximation, not an exact BPE count: the threshold is meant as a
// safety margin, not a precise limit.
const charsPerToken = 4

// defaultImageTokens is a conservative estimate used when a screenshot has no
// known dimensions.
const defaultImageTokens = 1024 * 768 / 750

// estimateTokens returns a rough token estimate for plain text.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	// CJK, kana, Hangul and emoji cost about one token per character, far above
	// the Latin ratio; ASCII and accented Latin keep the plain estimate.
	runes, wide := 0, 0
	for _, r := range text {
		runes++
		if r >= 0x2E80 {
			wide++
		}
	}
	tokens := int(math.Ceil(float64(runes-wide)/float64(charsPerToken))) + wide
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// estimateMessageTokens estimates the cost of a single history message,
// including reasoning, tool-call arguments, images (by area, never the base64
// payload), plus a small per-message structural overhead.
func estimateMessageTokens(m types.Message) int {
	tokens := estimateTokens(m.Content)
	tokens += estimateTokens(m.ReasoningContent)

	for _, tc := range m.ToolCalls {
		tokens += estimateTokens(tc.Function.Name)
		args := tc.Function.ArgumentsRaw
		if args == "" && len(tc.Function.Arguments) > 0 {
			if b, err := json.Marshal(tc.Function.Arguments); err == nil {
				args = string(b)
			}
		}
		tokens += estimateTokens(args)
	}

	for _, img := range m.Images {
		w, h := img.Width, img.Height
		if w <= 0 || h <= 0 {
			tokens += defaultImageTokens
			continue
		}
		tokens += int(math.Ceil(float64(w*h) / 750.0))
	}

	// role, delimiters and other per-message framing overhead.
	tokens += 4
	return tokens
}

// estimateHistoryTokens sums the per-message estimates over a history slice.
func estimateHistoryTokens(history []types.Message) int {
	total := 0
	for _, m := range history {
		total += estimateMessageTokens(m)
	}
	return total
}

// estimateToolsTokens estimates the cost of the tool schemas sent with every
// request (descriptions included).
func estimateToolsTokens(tools []types.Tool) int {
	total := 0
	for _, t := range tools {
		b, err := json.Marshal(t)
		if err != nil {
			continue
		}
		total += estimateTokens(string(b))
	}
	return total
}

// currentContextTokens returns the locally estimated size of the context that
// would be sent with the next request: the pruned history plus the tool schemas.
// It never uses provider token counts and is never the trigger on its own: see
// contextTokens.
func currentContextTokens(s *ChatSession) int {
	if s == nil {
		return 0
	}
	history := pruneScreenshotImages(s.ChatHistory, config.EffectiveMaxMedia())
	return estimateHistoryTokens(history) + estimateToolsTokens(s.getToolsForAI())
}

// tokenEstimateTolerance bounds how far the provider's count and the local
// estimate may diverge before the measurement is considered implausible. The
// estimator is a rough approximation and tokenizers vary, so a factor of a few
// is normal; an order-of-magnitude gap means the provider reported something
// other than the whole context (a per-chunk count, the uncached tail of a
// cached prompt, a gateway echoing the last message).
const tokenEstimateTolerance = 4

// providerContextTokens returns the provider's measurement of the context the
// last request carried, extended with the locally estimated size of the
// messages appended since that request (the response and its tool results). A
// zero result means no usable measurement exists.
func providerContextTokens(s *ChatSession) int {
	if s == nil || s.lastProviderTokens <= 0 {
		return 0
	}
	if s.lastProviderMessages < 0 || s.lastProviderMessages > len(s.ChatHistory) {
		// The history was rewritten (compaction, resume, an edited prefix): the
		// measurement no longer describes its prefix.
		return 0
	}
	appended := pruneScreenshotImages(s.ChatHistory[s.lastProviderMessages:], config.EffectiveMaxMedia())
	return s.lastProviderTokens + estimateHistoryTokens(appended)
}

// contextTokens returns the size of the context the next request would send, in
// provider tokens: the provider's own measurement when it exists and is
// plausible, the local estimate otherwise. This drives the compaction trigger.
func contextTokens(s *ChatSession) int {
	local := currentContextTokens(s)
	provider := providerContextTokens(s)
	if provider <= 0 || !plausibleContextTokens(provider, local) {
		return local
	}
	return provider
}

// plausibleContextTokens reports whether a provider measurement is close enough
// to the local estimate to be believed (see tokenEstimateTolerance).
func plausibleContextTokens(provider, local int) bool {
	if provider <= 0 || local <= 0 {
		return false
	}
	return provider <= local*tokenEstimateTolerance && local <= provider*tokenEstimateTolerance
}

// rescaleContextTokens converts a local estimate into the unit a trigger used:
// cur is the trigger value, local the local estimate taken at the same moment.
// Compaction measures its result locally, but the threshold may have been
// reached through the provider count, so both numbers must share a unit before
// they are compared.
func rescaleContextTokens(v, cur, local int) int {
	if v <= 0 || cur <= 0 || local <= 0 || cur == local {
		return v
	}
	return int(int64(v) * int64(cur) / int64(local))
}
