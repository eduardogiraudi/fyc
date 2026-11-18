package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The badge is drawn by the chat, the rest of the status line by RenderProviderInfo:
// splitting must give back the same text and never double a space.
func TestSplitModeSegmentReturnsTheLineWithoutTheBadge(t *testing.T) {
	line := RenderProviderInfo("test (model)", "0.70", "manual")
	prefix, mode, badge, ok := SplitModeSegment(line)
	if !ok {
		t.Fatalf("no mode segment in %q", stripANSI(line))
	}
	if mode != "manual" {
		t.Fatalf("mode = %q, want manual", mode)
	}
	if badge != "[Mode: manual]" {
		t.Fatalf("badge = %q, want the plain badge text", badge)
	}
	if got, want := stripANSI(prefix), "[Provider: test (model)] [Temp: 0.70]"; got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
	if !strings.HasSuffix(stripANSI(line), badge) {
		t.Fatalf("the badge is not the tail of %q", stripANSI(line))
	}
}

func TestSplitModeSegmentRefusesOtherLines(t *testing.T) {
	for _, line := range []string{"", "[Temp: 0.70]", "[Mode: ]"} {
		if _, _, _, ok := SplitModeSegment(line); ok {
			t.Fatalf("%q has no usable mode segment", line)
		}
	}
}

// The hover highlight must not move a column: the click target would slide out
// from under the pointer.
func TestHoverButtonKeepsItsWidth(t *testing.T) {
	idle := ModeSegment("manual")
	want := lipgloss.Width(idle)
	if got := lipgloss.Width(HoverButton("[Mode: manual]", idle, true)); got != want {
		t.Fatalf("the hovered button renders %d columns, want %d", got, want)
	}
	if got := HoverButton("[Mode: manual]", idle, false); got != idle {
		t.Fatalf("an idle button must return the caller's rendering, got %q", got)
	}
}
