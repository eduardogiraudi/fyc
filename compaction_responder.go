// Package main — the compaction request: which model writes the summary and
// what happens when its API call fails.
//
// The compactor is resolved from `config.Get()` on every attempt, so
// the default ("current") follows a runtime model switch, and the fallback can
// never write the configuration: pinning a compactor is a preference, not a
// second active provider.
package main

import (
	"context"
	"fmt"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/logger"
	"fuckyourcli/providers"
	"fuckyourcli/types"
)

// compactionAttempt records one summarization request that failed.
type compactionAttempt struct {
	Model string
	Err   error
}

// compactionResult carries what the caller needs to report: which model wrote
// the summary and whether a failed primary made room for the fallback.
type compactionResult struct {
	Summary  string
	Model    string
	FellBack bool
	Attempts []compactionAttempt
}

// compactionResponder performs the summarization of a prepared transcript.
type compactionResponder func(context.Context, []types.Message, *ChatSession) (compactionResult, error)

func validateCompactionResponse(ctx context.Context, result *types.StreamResult) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if result == nil || result.Interrupted {
		return "", fmt.Errorf("compaction response interrupted or missing")
	}
	if responseTruncated(result.FinishReason) {
		return "", fmt.Errorf("compaction response truncated: %s", result.FinishReason)
	}
	switch strings.ToLower(result.FinishReason) {
	case "":
		// Compatible backends may omit the finish reason on a successful stream.
		// Missing metadata is not an explicit failure or truncation signal.
	case "stop", "end_turn", "stop_sequence":
	default:
		return "", fmt.Errorf("compaction response not complete: %q", result.FinishReason)
	}
	if len(result.ToolCalls) != 0 || strings.TrimSpace(result.Content) == "" {
		return "", fmt.Errorf("compaction response has tools or an empty summary")
	}
	return result.Content, nil
}

// getCompactionResponse is the production compaction responder.
//
// The primary attempt uses the configured compactor (or the active model when
// none is configured). Any failure that is not the user's own interruption is
// handled by the configured policy: retry once with the model active at that
// moment, or leave the history untouched. The retry re-reads the configuration
// (the user may have switched model meanwhile) and is skipped when it would
// address the same connection and model that just failed.
func getCompactionResponse(ctx context.Context, messages []types.Message, s *ChatSession) (compactionResult, error) {
	if err := ctx.Err(); err != nil {
		return compactionResult{}, err
	}
	cfg := config.Get()
	primary, err := cfg.Compactor()
	var res compactionResult
	if err == nil {
		res, err = attemptCompaction(ctx, cfg, primary, messages, s)
		if err == nil {
			return res, nil
		}
	} else {
		// A compactor that no longer resolves (provider removed, unknown kind) is
		// an API-level failure for the user: apply the same policy.
		res = compactionResult{Model: cfg.CompactorLabel(), Attempts: []compactionAttempt{{Model: cfg.CompactorLabel(), Err: err}}}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, ctxErr
	}
	if !compactionRetryable(ctx, err) || cfg.CompactionFailurePolicy() != config.CompactionFallbackCurrent {
		return res, err
	}
	live := config.Get()
	fallback := live.ActiveChoice()
	if fallback.SameTarget(primary) {
		return res, err
	}
	retry, retryErr := attemptCompaction(ctx, live, fallback, messages, s)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, ctxErr
	}
	if retryErr != nil {
		return res, fmt.Errorf("%w (retry with %s also failed: %v)", err, fallback.Label(), retryErr)
	}
	retry.FellBack = true
	retry.Attempts = append(res.Attempts, retry.Attempts...)
	return retry, nil
}

// attemptCompaction performs exactly one summarization request. Both the provider
// construction and the request are reported through the returned error so the
// caller can apply the same policy to a missing model, a revoked key, a deleted
// provider entry and an API error alike.
func attemptCompaction(ctx context.Context, cfg config.Config, choice config.CompactorChoice, messages []types.Message, s *ChatSession) (compactionResult, error) {
	res := compactionResult{Model: choice.Label()}
	provider, err := newProvider(cfg, choice, nil)
	if err != nil {
		res.Attempts = []compactionAttempt{{Model: res.Model, Err: err}}
		return res, err
	}
	// A utility request must not inherit the active mode's instructions, and it
	// keeps the conversation-log scope so the summary stays correlated.
	reqCtx := logger.WithAgent(sessionLogContext(ctx, s), "compaction", "")
	result, err := providers.WithLogging(provider).StreamChat(reqCtx, pruneScreenshotImages(messages, 0), nil, providers.NewSilentOutputSink())
	if err != nil {
		res.Attempts = []compactionAttempt{{Model: res.Model, Err: err}}
		return res, err
	}
	summary, err := validateCompactionResponse(reqCtx, result)
	if err != nil {
		res.Attempts = []compactionAttempt{{Model: res.Model, Err: err}}
		return res, err
	}
	res.Summary = summary
	return res, nil
}

// compactionRetryable reports whether a compaction failure may be retried with
// another model. Only the parent context tells a user interruption apart: a
// provider idle timeout cancels the stream with a wrapped DeadlineExceeded
// while the turn context stays intact, so it must still fall back.
func compactionRetryable(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() == nil
}

// compactionFallbackNotice tells the user which compactor failed and that the
// configuration was not touched to recover.
func compactionFallbackNotice(res compactionResult) string {
	if len(res.Attempts) == 0 {
		return "⚠ Compactor failed; retried with " + res.Model + ". Configuration unchanged."
	}
	a := res.Attempts[0]
	return fmt.Sprintf("⚠ Compactor %s failed: %v. Retried with %s; configuration unchanged.", a.Model, a.Err, res.Model)
}

// compactionFailureHint is appended to a failed compaction: what the policy did
// with it. Both outcomes keep the history intact. auto marks the automatic
// paths, whose latch suspends further attempts for the current context.
func compactionFailureHint(cfg config.Config, auto bool) string {
	hint := " History untouched."
	if cfg.CompactionFailurePolicy() == config.CompactionAbort {
		hint = " Compaction interrupted, history untouched (policy: abort)."
	} else if auto {
		hint = ""
	}
	if auto {
		hint += " Suspended for this context."
	}
	return hint
}
