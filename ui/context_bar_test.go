package ui

import (
	"strings"
	"testing"
)

// A gauge without a maximum would be a lie, and an empty one says nothing: both
// cases must render no row at all.
func TestRenderContextBarNeedsAWindowAndSomeUsage(t *testing.T) {
	if got := RenderContextBar(5000, 0); got != "" {
		t.Fatalf("unknown window rendered %q", got)
	}
	if got := RenderContextBar(0, 128000); got != "" {
		t.Fatalf("empty context rendered %q", got)
	}
	if got := RenderContextBar(-1, 128000); got != "" {
		t.Fatalf("negative usage rendered %q", got)
	}
}

func TestRenderContextBarFill(t *testing.T) {
	line := stripANSI(RenderContextBar(64000, 128000))
	for _, want := range []string{"[ctx ", "]", "50%", "64k/128k tokens"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %q", want, line)
		}
	}
	if got := strings.Count(line, "█"); got != contextBarCells/2 {
		t.Fatalf("filled cells = %d, want %d (%q)", got, contextBarCells/2, line)
	}
	if got := strings.Count(line, "░"); got != contextBarCells/2 {
		t.Fatalf("empty cells = %d, want %d (%q)", got, contextBarCells/2, line)
	}
}

// Overshooting the window (a provider count that disagrees with the catalog, a
// model beyond its limit) saturates instead of overflowing the row.
func TestRenderContextBarSaturates(t *testing.T) {
	line := stripANSI(RenderContextBar(256000, 128000))
	if !strings.Contains(line, "100%") {
		t.Fatalf("missing 100%% in %q", line)
	}
	if got := strings.Count(line, "█"); got != contextBarCells {
		t.Fatalf("filled cells = %d, want %d (%q)", got, contextBarCells, line)
	}
	if strings.Contains(line, "░") {
		t.Fatalf("saturated bar still has empty cells: %q", line)
	}
}

func TestFormatTokenCount(t *testing.T) {
	cases := map[int]string{
		0: "0", 812: "812", 1200: "1.2k", 53700: "53.7k",
		128000: "128k", 200000: "200k", 1200000: "1.2M",
	}
	for n, want := range cases {
		if got := formatTokenCount(n); got != want {
			t.Fatalf("formatTokenCount(%d) = %q, want %q", n, got, want)
		}
	}
}
