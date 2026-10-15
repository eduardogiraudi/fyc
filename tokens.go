// Package main — local token estimation.
//
// The compaction threshold is ALWAYS evaluated against a locally computed
// estimate: provider-reported token counts (StreamResult.PromptTokens,
// s.LastCtx) are only shown as informational stats and never drive the trigger,
// because some providers report only the last request's tokens rather than the
// whole context size.
package main

import (
	"encoding/json"
	"math"
	"unicode/utf8"

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
	runes := utf8.RuneCountInString(text)
	tokens := int(math.Ceil(float64(runes) / float64(charsPerToken)))
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
// It never uses provider token counts.
func currentContextTokens(s *ChatSession) int {
	if s == nil {
		return 0
	}
	history := pruneScreenshotImages(s.ChatHistory, maxKeptScreenshots)
	return estimateHistoryTokens(history) + estimateToolsTokens(s.getToolsForAI())
}
