package main

import (
	"strings"
	"testing"

	"fuckyourcli/logger"
)

func TestCompactTokens(t *testing.T) {
	for in, want := range map[int]string{
		0:          "0",
		999:        "999",
		1000:       "1k",
		1234:       "1.2k",
		99_960:     "100k",
		412_300:    "412k",
		999_600:    "1M",
		1_260_000:  "1.3M",
		18_400_000: "18.4M",
	} {
		if got := compactTokens(in); got != want {
			t.Errorf("compactTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCacheHitPercent(t *testing.T) {
	if got := cacheHitPercent(logger.Totals{}); got != "-" {
		t.Fatalf("empty = %q", got)
	}
	if got := cacheHitPercent(logger.Totals{Input: 200, CacheRead: 0}); got != "0%" {
		t.Fatalf("no cache = %q", got)
	}
	if got := cacheHitPercent(logger.Totals{Input: 200, CacheRead: 142}); got != "71%" {
		t.Fatalf("71%% = %q", got)
	}
	if got := cacheHitPercent(logger.Totals{Input: 10, CacheRead: 50}); got != "100%" {
		t.Fatalf("clamp = %q", got)
	}
}

func TestUsageLinesOrderAndRoles(t *testing.T) {
	u := logger.Usage{
		Since: "2026-11-02",
		All:   logger.Totals{Requests: 412, Input: 18_400_000, Output: 1_200_000, CacheRead: 13_064_000, CacheWrite: 2_000_000},
		Month: logger.Totals{Requests: 96, Input: 4_100_000, Output: 310_000},
	}
	session := logger.Totals{Requests: 31, Input: 412_000, Output: 38_000}
	roles := map[string]logger.Totals{
		"compaction": {Requests: 2},
		"subagent":   {Requests: 5},
		"main":       {Requests: 24},
		"zzz":        {Requests: 1},
	}
	lines := usageLines(u, session, roles)
	if len(lines) != 8 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	for i, prefix := range []string{"", "All time", "This month", "This session", "  main", "  subagent", "  compaction", "  zzz"} {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], prefix)
		}
	}
	for _, want := range []string{"412", "18.4M", "1.2M", "71%", "2M"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("all-time row %q lacks %q", lines[1], want)
		}
	}
	if !strings.Contains(lines[2], "4.1M") || !strings.Contains(lines[2], "310k") {
		t.Errorf("month row = %q", lines[2])
	}
	// Every row keeps the label column, so the styled split never cuts a figure.
	for _, l := range lines[1:] {
		if strings.TrimSpace(l[:usageLabelW]) == "" {
			t.Errorf("row %q has no label", l)
		}
	}
}

func TestUsageLinesHidesSingleRole(t *testing.T) {
	lines := usageLines(logger.Usage{}, logger.Totals{Requests: 3}, map[string]logger.Totals{"main": {Requests: 3}})
	if len(lines) != 4 {
		t.Fatalf("a lone main agent needs no breakdown: %q", lines)
	}
}

func TestUsageRowTruncatesLongLabel(t *testing.T) {
	long := usageRow("  a-very-long-agent-role", logger.Totals{Requests: 1})
	short := usageRow("x", logger.Totals{Requests: 1})
	if len(long) != len(short) {
		t.Fatalf("long label shifted the columns: %q vs %q", long, short)
	}
}

func TestUsageNotes(t *testing.T) {
	notes := usageNotes(logger.Usage{}, logger.Totals{})
	if len(notes) != 1 || !strings.Contains(notes[0], "no cost") {
		t.Fatalf("notes = %q", notes)
	}
	notes = usageNotes(logger.Usage{Since: "2026-11-02", All: logger.Totals{Failed: 3}, Month: logger.Totals{Failed: 1}}, logger.Totals{Failed: 1})
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"since 2026-11-02", "3 all time, 1 this month, 1 this session"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q: %q", want, joined)
		}
	}
}
