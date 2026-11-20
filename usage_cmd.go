// Package main — the /usage slash command.
//
// It reports token usage, never cost: prices depend on the provider, the
// endpoint and the plan, and cannot be known reliably across every transport
// fyc supports. The figures come from the logger package: a persistent
// per-day ledger for the all-time and monthly rows, and the conversation's own
// event stream for the session row.
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fuckyourcli/logger"
	"fuckyourcli/ui"
)

// usageLabelW is the width of the first column. Rows are built as plain text
// and split here so the label and the figures can be styled separately.
const usageLabelW = 14

// usageRoleOrder lists the agent roles in display order; unknown roles follow
// alphabetically.
var usageRoleOrder = []string{"main", "subagent", "compaction"}

func usageHeader() string {
	return fmt.Sprintf("%-*s%10s%9s%9s%11s%13s", usageLabelW, "", "Requests", "Input", "Output", "Cache hit", "Cache write")
}

func usageRow(label string, t logger.Totals) string {
	if len(label) > usageLabelW-1 {
		label = label[:usageLabelW-1]
	}
	return fmt.Sprintf("%-*s%10d%9s%9s%11s%13s", usageLabelW, label,
		t.Requests, compactTokens(t.Input), compactTokens(t.Output), cacheHitPercent(t), cacheWriteCell(t))
}

// compactTokens renders a token count as 950, 1.2k, 412k or 18.4M.
func compactTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 999_500:
		return scaledTokens(float64(n)/1e3, "k")
	default:
		return scaledTokens(float64(n)/1e6, "M")
	}
}

func scaledTokens(v float64, unit string) string {
	s := fmt.Sprintf("%.1f", v)
	if v >= 100 {
		s = fmt.Sprintf("%.0f", v)
	}
	return strings.TrimSuffix(s, ".0") + unit
}

// cacheHitPercent is the share of the prompt served from the provider cache.
func cacheHitPercent(t logger.Totals) string {
	if t.Input <= 0 {
		return "-"
	}
	pct := int(float64(t.CacheRead)*100/float64(t.Input) + 0.5)
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%d%%", pct)
}

func cacheWriteCell(t logger.Totals) string {
	if t.CacheWrite <= 0 {
		return "-"
	}
	return compactTokens(t.CacheWrite)
}

func sortedUsageRoles(roles map[string]logger.Totals) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	rank := func(name string) int {
		for i, r := range usageRoleOrder {
			if r == name {
				return i
			}
		}
		return len(usageRoleOrder)
	}
	sort.Slice(names, func(i, j int) bool {
		if ri, rj := rank(names[i]), rank(names[j]); ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	return names
}

// usageLines returns the table as plain text: the header, then all time, this
// month and this session. When more than one agent took part in the session,
// one indented row per agent role follows it.
func usageLines(u logger.Usage, session logger.Totals, roles map[string]logger.Totals) []string {
	lines := []string{
		usageHeader(),
		usageRow("All time", u.All),
		usageRow("This month", u.Month),
		usageRow("This session", session),
	}
	if len(roles) > 1 {
		for _, name := range sortedUsageRoles(roles) {
			lines = append(lines, usageRow("  "+name, roles[name]))
		}
	}
	return lines
}

func usageNotes(u logger.Usage, session logger.Totals) []string {
	var notes []string
	if u.Since != "" {
		notes = append(notes, "All time counts requests since "+u.Since+".")
	}
	if u.All.Failed > 0 || session.Failed > 0 {
		notes = append(notes, fmt.Sprintf("Failed requests: %d all time, %d this month, %d this session.",
			u.All.Failed, u.Month.Failed, session.Failed))
	}
	notes = append(notes, "Input is the whole prompt, cached tokens included. Tokens only: no cost is estimated.")
	return notes
}

// showUsage prints the /usage report to the chat.
func showUsage(s *ChatSession) {
	sink := s.commandSink()
	u, err := logger.LoadUsage(time.Now())
	if err != nil {
		sinkLine(sink, ui.ErrorStyle.Render("✗ Cannot read usage: "+err.Error()))
		return
	}
	var session logger.Totals
	var roles map[string]logger.Totals
	if s.SessionID != "" {
		session, roles, err = logger.SessionUsage(s.SessionID)
		if err != nil {
			sinkLine(sink, ui.ErrorStyle.Render("✗ Cannot read this session's usage: "+err.Error()))
		}
	}

	lines := usageLines(u, session, roles)
	sinkLine(sink, ui.TitleStyle.Render("Usage"))
	sinkLine(sink, ui.InfoStyle.Render(lines[0]))
	for _, line := range lines[1:] {
		sinkLine(sink, ui.InfoStyle.Render(line[:usageLabelW])+ui.OkStyle.Render(line[usageLabelW:]))
	}
	for _, note := range usageNotes(u, session) {
		sinkLine(sink, ui.TipStyle.Render(note))
	}
	sinkBlankLine(sink)
}
