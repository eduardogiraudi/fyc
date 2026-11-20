package ui

import (
	"strings"
	"testing"
)

func TestTipsModeBadgeHintOnlyInAltScreen(t *testing.T) {
	for _, alt := range []bool{false, true} {
		got := stripControlSequences(TipsBlock(alt))
		if strings.Contains(got, "or a click on the mode badge") != alt {
			t.Fatalf("unexpected badge hint for alt_screen=%v: %s", alt, got)
		}
		if !strings.Contains(got, "ctrl+o") || !strings.Contains(got, "/auto") || !strings.Contains(got, "to enable/disable auto mode") {
			t.Fatalf("keyboard and command hints must remain visible: %s", got)
		}
		if !strings.Contains(got, "ctrl+r to search the history") {
			t.Fatalf("reverse history search hint must be visible: %s", got)
		}
	}
}
