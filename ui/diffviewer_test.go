package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDiffBackgroundPadsToFullWidth(t *testing.T) {
	v := diffVisualLine{
		line:    DiffLine{Kind: DiffAdded, LineNum: 1},
		content: "func main() {}",
		gutter:  diffGutter(DiffLine{LineNum: 1}),
	}
	out := renderDiffVisualLine(v, "main.go", 40)
	if got := DisplayWidth(stripControlSequences(out)); got != 40 {
		t.Fatalf("added row width = %d, want 40", got)
	}
}
func TestDiffViewerViewNeverExceedsTerminalWidth(t *testing.T) {
	lines := make([]DiffLine, 50)
	for i := range lines {
		lines[i] = DiffLine{Kind: DiffAdded, LineNum: i + 1, Content: strings.Repeat("x", 100)}
	}
	for _, w := range []int{10, 20, 30, 45, 80} {
		m := DiffViewerModel{
			Title: "titolo", Stats: "+50 -0", Filename: "a.go",
			Lines: lines, termWidth: w, termHeight: 12, headerRows: 3,
		}
		for i, row := range strings.Split(m.View(), "\n") {
			if got := DisplayWidth(stripControlSequences(row)); got > w {
				t.Fatalf("width %d, row %d is %d cells: %q", w, i, got, row)
			}
		}
	}
}
func TestDiffVisualLinesWrapWithoutRepeatingLineNumbers(t *testing.T) {
	m := DiffViewerModel{
		termWidth:  20,
		termHeight: 24,
		Filename:   "example.go",
		Lines: []DiffLine{{
			Kind:    DiffAdded,
			LineNum: 42,
			Content: "\t" + strings.Repeat("x", 24),
		}},
	}
	visual := m.visualLines()
	if len(visual) < 2 {
		t.Fatalf("long source line produced %d visual row(s), want at least 2", len(visual))
	}
	if got := visual[0].gutter; got != "  42 │ " {
		t.Fatalf("first gutter = %q, want source line number", got)
	}
	if got := diffGutter(DiffLine{}); got != "     │ " {
		t.Fatalf("blank gutter = %q, want separator aligned with numbered gutters", got)
	}
	if visual[0].continuation {
		t.Fatal("first visual row marked as continuation")
	}
	for i, line := range visual {
		if i > 0 && !line.continuation {
			t.Fatalf("row %d must be a continuation", i)
		}
		rendered := renderDiffVisualLine(line, m.Filename, m.termWidth)
		plain := stripControlSequences(rendered)
		if got := DisplayWidth(plain); got > m.termWidth {
			t.Fatalf("row %d is %d cells wide, terminal is %d: %q", i, got, m.termWidth, plain)
		}
		if i > 0 && strings.Contains(plain, "42 │") {
			t.Fatalf("continuation row %d repeated source line number: %q", i, plain)
		}
	}
}

func TestDiffViewerScrollsVisualRows(t *testing.T) {
	m := DiffViewerModel{
		termWidth:  20,
		termHeight: 8,
		headerRows: 3,
		Lines:      []DiffLine{{Kind: DiffContext, Content: strings.Repeat("x", 30)}},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	got := updated.(DiffViewerModel)
	if got.offset != 1 {
		t.Fatalf("down offset = %d, want 1 visual row", got.offset)
	}
}

// Wrapping every line on every frame is what made large diffs unresponsive:
// the rows must be computed once per terminal width, no matter how many times
// Update and View run.
func TestDiffViewerWrapsRowsOncePerWidth(t *testing.T) {
	lines := make([]DiffLine, 200)
	for i := range lines {
		lines[i] = DiffLine{Kind: DiffContext, LineNum: i + 1, Content: "line " + strings.Repeat("y", 60)}
	}
	m := DiffViewerModel{
		Filename:   "a.go",
		Lines:      lines,
		termWidth:  30,
		termHeight: 10,
		headerRows: 3,
		render:     &diffRenderCache{},
	}
	first := m.visualLines()
	second := m.visualLines()
	if len(first) == 0 || &first[0] != &second[0] {
		t.Fatal("visual rows were wrapped again for an unchanged width")
	}

	wider := m
	wider.termWidth = 60
	if got := wider.visualLines(); &got[0] == &first[0] {
		t.Fatal("width change did not re-wrap the rows")
	}
}

func TestWrapPlainByANSIWidthKeepsTextAndFitsWidth(t *testing.T) {
	for _, in := range []string{"", "short", strings.Repeat("x", 100), "日本語テキスト", "abc日本語def", "❤️", "a❤️b❤️c", "👨‍👩‍👧", "x👨‍👩‍👧y", strings.Repeat("日", 2000)} {
		for _, width := range []int{2, 3, 7, 10, 80} {
			chunks := wrapPlainByANSIWidth(in, width)
			if got := strings.Join(chunks, ""); got != in {
				t.Fatalf("width %d: %q -> %q", width, in, got)
			}
			for i, chunk := range chunks {
				if got := DisplayWidth(chunk); got > width {
					t.Fatalf("width %d: chunk %d is %d cells: %q", width, i, got, chunk)
				}
			}
		}
	}
	// Tabs advance to the next tab stop and must survive the split; callers
	// still expandTabs first so each chunk is exactly the width they measured.
	if got := strings.Join(wrapPlainByANSIWidth("a\tb", 4), ""); got != "a\tb" {
		t.Fatalf("tab input lost text: %q", got)
	}
}
