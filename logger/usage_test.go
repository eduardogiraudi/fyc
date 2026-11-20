package logger

import (
	"context"
	"os"
	"testing"
	"time"

	"fuckyourcli/types"
)

func requestWith(t *testing.T, ctx context.Context, meta Metadata, result *types.StreamResult) {
	t.Helper()
	p := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return result, nil
	}), meta)
	if _, err := p.StreamChat(ctx, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUsageAccumulatesAcrossConversations(t *testing.T) {
	setupLogs(t)
	meta := Metadata{Provider: "openai_compatible", Model: "m"}
	requestWith(t, WithSession(context.Background(), "one"), meta,
		&types.StreamResult{PromptTokens: 100, CompletionTokens: 10, CachedTokens: 40, ContextTokens: 100})
	requestWith(t, WithSession(context.Background(), "two"), meta,
		&types.StreamResult{PromptTokens: 50, CompletionTokens: 5, ContextTokens: 50})

	u, err := LoadUsage(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := Totals{Requests: 2, Input: 150, Output: 15, CacheRead: 40}
	if u.All != want || u.Month != want {
		t.Fatalf("usage = %+v, want %+v", u, want)
	}
	if u.Since != dayKey(time.Now()) {
		t.Fatalf("since = %q", u.Since)
	}
}

func TestUsageInputIncludesCachedPartForAnthropic(t *testing.T) {
	setupLogs(t)
	// Anthropic reports the uncached tail as the prompt: 10 + 90 + 5 is the whole prompt.
	requestWith(t, WithSession(context.Background(), "a"), Metadata{Provider: "anthropic"},
		&types.StreamResult{PromptTokens: 10, CachedTokens: 90, CacheWriteTokens: 5, ContextTokens: 105, CompletionTokens: 3})
	// A record from before context_tokens existed is normalised from its parts.
	if got := eventInput(Event{Metadata: Metadata{Provider: "anthropic"}, PromptTokens: 10, CachedTokens: 90, CacheWriteTokens: 5}); got != 105 {
		t.Fatalf("legacy anthropic input = %d", got)
	}
	if got := eventInput(Event{Metadata: Metadata{Provider: "gemini"}, PromptTokens: 10, CachedTokens: 4}); got != 10 {
		t.Fatalf("legacy gemini input = %d", got)
	}
	u, err := LoadUsage(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if u.All.Input != 105 || u.All.CacheRead != 90 || u.All.CacheWrite != 5 || u.All.Output != 3 {
		t.Fatalf("usage = %+v", u.All)
	}
}

func TestUsageSurvivesLogRemoval(t *testing.T) {
	setupLogs(t)
	requestWith(t, WithSession(context.Background(), "gone"), Metadata{Provider: "test"},
		&types.StreamResult{PromptTokens: 7, CompletionTokens: 2})
	if err := RemoveSession("gone"); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUsage(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if u.All.Requests != 1 || u.All.Input != 7 || u.All.Output != 2 {
		t.Fatalf("ledger lost the removed conversation: %+v", u.All)
	}
	total, _, err := SessionUsage("gone")
	if err != nil || total.Requests != 0 {
		t.Fatalf("removed conversation still counted: %+v %v", total, err)
	}
}

func TestUsageMonthIsCalendarMonth(t *testing.T) {
	setupLogs(t)
	requestWith(t, WithSession(context.Background(), "m"), Metadata{Provider: "test"},
		&types.StreamResult{PromptTokens: 7})
	u, err := LoadUsage(time.Now().AddDate(0, -2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if u.All.Requests != 1 || u.Month.Requests != 0 {
		t.Fatalf("month window wrong: %+v", u)
	}
}

func TestUsageRebuildsFromLogsWhenLedgerIsMissing(t *testing.T) {
	setupLogs(t)
	meta := Metadata{Provider: "test"}
	requestWith(t, WithSession(context.Background(), "first"), meta, &types.StreamResult{PromptTokens: 5})
	if err := os.Remove(usagePath()); err != nil {
		t.Fatal(err)
	}
	// Reading without a ledger counts the logs still on disk.
	u, err := LoadUsage(time.Now())
	if err != nil || u.All.Requests != 1 {
		t.Fatalf("read without ledger: %+v %v", u, err)
	}
	// The next request rebuilds from the logs, which already hold it: it must
	// be counted once.
	requestWith(t, WithSession(context.Background(), "second"), meta, &types.StreamResult{PromptTokens: 6})
	u, err = LoadUsage(time.Now())
	if err != nil || u.All.Requests != 2 || u.All.Input != 11 {
		t.Fatalf("rebuild double counted or lost data: %+v %v", u, err)
	}
}

func TestUsageRecoversFromCorruptLedger(t *testing.T) {
	setupLogs(t)
	meta := Metadata{Provider: "test"}
	requestWith(t, WithSession(context.Background(), "c"), meta, &types.StreamResult{PromptTokens: 5})
	if err := os.WriteFile(usagePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if u, err := LoadUsage(time.Now()); err != nil || u.All.Requests != 1 {
		t.Fatalf("read with corrupt ledger: %+v %v", u, err)
	}
	requestWith(t, WithSession(context.Background(), "c"), meta, &types.StreamResult{PromptTokens: 6})
	u, err := LoadUsage(time.Now())
	if err != nil || u.All.Requests != 2 || u.All.Input != 11 {
		t.Fatalf("corrupt ledger not rebuilt: %+v %v", u, err)
	}
}

func TestSessionUsageSplitsByRoleAndCountsFailures(t *testing.T) {
	setupLogs(t)
	meta := Metadata{Provider: "test"}
	ctx := WithSession(context.Background(), "s")
	requestWith(t, ctx, meta, &types.StreamResult{PromptTokens: 10, CompletionTokens: 1})
	requestWith(t, ctx, meta, &types.StreamResult{PromptTokens: 20, CompletionTokens: 2})
	requestWith(t, WithAgent(ctx, "subagent", "a1"), meta, &types.StreamResult{PromptTokens: 5, CompletionTokens: 1})
	failing := Wrap(providerFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return nil, context.DeadlineExceeded
	}), meta)
	if _, err := failing.StreamChat(WithAgent(ctx, "compaction", ""), nil, nil, nil); err == nil {
		t.Fatal("expected the provider error")
	}
	// Another conversation must not leak into the session figures.
	requestWith(t, WithSession(context.Background(), "other"), meta, &types.StreamResult{PromptTokens: 999})

	total, roles, err := SessionUsage("s")
	if err != nil {
		t.Fatal(err)
	}
	if total.Requests != 3 || total.Failed != 1 || total.Input != 35 || total.Output != 4 {
		t.Fatalf("session total = %+v", total)
	}
	if roles["main"].Requests != 2 || roles["subagent"].Requests != 1 || roles["compaction"].Failed != 1 {
		t.Fatalf("roles = %+v", roles)
	}
	if _, _, err := SessionUsage("../escape"); err == nil {
		t.Fatal("invalid session id accepted")
	}
	if total, _, err := SessionUsage("never-used"); err != nil || total.Requests != 0 {
		t.Fatalf("unknown session: %+v %v", total, err)
	}
}
