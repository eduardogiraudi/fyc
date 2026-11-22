package main

import (
	"strings"
	"testing"

	"fuckyourcli/config"

	"github.com/charmbracelet/x/ansi"
)

// The gauge is a row of its own, right under the provider line it describes,
// and it disappears (leaving no blank row behind) when the catalog does not
// document the model's context window.
func TestContextGaugeUnderProviderLine(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	m.width, m.height = 80, 24
	m.enableAltScreen(nil)
	m.contextUsed, m.contextMax = 64000, 128000

	parts, statusIdx := m.bottomPartsIndexed()
	if statusIdx < 0 || statusIdx+1 >= len(parts) {
		t.Fatalf("no row under the provider line (status %d of %d)", statusIdx, len(parts))
	}
	if !strings.Contains(ansi.Strip(parts[statusIdx]), "[Provider:") {
		t.Fatalf("status row is not the provider line: %q", ansi.Strip(parts[statusIdx]))
	}
	bar := ansi.Strip(parts[statusIdx+1])
	for _, want := range []string{"[ctx ", "50%", "64k/128k"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("gauge row %q is missing %q", bar, want)
		}
	}

	m.contextMax = 0
	parts, _ = m.bottomPartsIndexed()
	if strings.Contains(ansi.Strip(strings.Join(parts, "\n")), "[ctx ") {
		t.Fatal("a model with no documented window must not draw a gauge")
	}
}

// The gauge measures the active session and the catalog, never a canned figure:
// with no session there is nothing to fill, and the window follows the model
// that would actually be sent.
func TestRefreshContextBarWithoutSession(t *testing.T) {
	m := newChatModel(nil, nil, nil, nil)
	if m.contextUsed != 0 {
		t.Fatalf("a session-less chat reports %d used tokens", m.contextUsed)
	}
	if want := contextWindowTokens(config.Get()); m.contextMax != want {
		t.Fatalf("window = %d, want %d for the configured model", m.contextMax, want)
	}
}
