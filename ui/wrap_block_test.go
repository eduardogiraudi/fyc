package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrapBlockPreservesContentAndCellWidths(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("word ", 20),
		"\x1b[31m" + strings.Repeat("界e\u0301", 20) + "\x1b[0m",
		"abc\tdef\n\tghi",
		"first\n\nlast\n",
	} {
		for _, width := range []int{4, 13, 40} {
			got := WrapBlock(input, width)
			want := strings.ReplaceAll(ansi.Strip(expandTabs(input, tabStopWidth)), "\n", "")
			if plain := strings.ReplaceAll(ansi.Strip(got), "\n", ""); plain != want {
				t.Fatalf("width %d lost content: %q != %q", width, plain, want)
			}
			for _, row := range strings.Split(got, "\n") {
				if DisplayWidth(row) > width {
					t.Fatalf("row exceeds width %d: %q", width, row)
				}
			}
		}
	}
}

func TestWrapBlockRestoresStyleOnEveryRow(t *testing.T) {
	got := WrapBlock("\x1b[31mabcdefghijkl\x1b[0m", 4)
	for _, row := range strings.Split(got, "\n") {
		if !strings.HasPrefix(row, "\x1b[31m") || !strings.HasSuffix(row, "\x1b[0m") {
			t.Fatalf("wrapped row lost or leaked its style: %q", row)
		}
	}
}
