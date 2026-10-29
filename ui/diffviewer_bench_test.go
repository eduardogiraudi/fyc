package ui

import (
	"fmt"
	"strings"
	"testing"
)

func benchDiffLines(n, lineLen int) []DiffLine {
	body := strings.Repeat("x", lineLen)
	lines := make([]DiffLine, n)
	for i := range lines {
		kind := DiffContext
		switch i % 7 {
		case 0:
			kind = DiffAdded
		case 1:
			kind = DiffRemoved
		}
		lines[i] = DiffLine{Kind: kind, Content: "    " + body + fmt.Sprint(i), LineNum: i + 1}
	}
	return lines
}

// BenchmarkDiffViewerView guards the render path of the scrolled viewer: a
// frame must cost the same for a 500-line and a 20000-line diff.
func BenchmarkDiffViewerView(b *testing.B) {
	for _, n := range []int{500, 5000, 20000} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			m := DiffViewerModel{
				Title: "t", Filename: "main.go", Lines: benchDiffLines(n, 80),
				termWidth: 120, termHeight: 40, headerRows: 3,
				render: &diffRenderCache{},
			}
			_ = m.View() // warm the wrap and highlight caches
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

// BenchmarkDiffViewerOpen is the one-off cost of opening a viewer: the whole
// diff is sanitised and wrapped once.
func BenchmarkDiffViewerOpen(b *testing.B) {
	m := DiffViewerModel{
		Filename: "main.go", Lines: benchDiffLines(20000, 80),
		termWidth: 120, termHeight: 40, headerRows: 3,
		render: &diffRenderCache{},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.render = &diffRenderCache{}
		_ = len(m.visualLines())
	}
}
