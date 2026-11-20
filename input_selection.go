package main

import (
	"math"
	"strings"

	"fuckyourcli/ui"

	"github.com/charmbracelet/x/ansi"
)

// rowsSelection is a drag selection over the rows one part of the frame drew.
// The transcript keeps its own selection (its rows outlive a frame, so it can
// survive a resize); the input bar does not, because its rows are redrawn from
// the model every time. A key press or a resize therefore drops it.
type rowsSelection struct {
	active bool
	anchor cellPos
	cursor cellPos
}

// start begins a selection at a cell of the rendered rows (row 0 is the first
// row of that part of the frame).
func (s *rowsSelection) start(row, col int) {
	s.active = true
	s.anchor = cellPos{row: max(0, row), col: max(0, col)}
	s.cursor = s.anchor
}

// extend moves the loose end of the drag to another cell, clamped to row 0 so a
// drag that leaves the part upwards still selects its first row.
func (s *rowsSelection) extend(row, col int) {
	if !s.active {
		return
	}
	s.cursor = cellPos{row: max(0, row), col: max(0, col)}
}

func (s *rowsSelection) clear() { s.active = false }

// highlight paints the covered cells of rows and leaves the others untouched.
// gutters reports, per row, how many leading cells are prompt decoration: they
// are never painted, so what the user sees selected is exactly what is copied.
func (s rowsSelection) highlight(rows []string, gutters []int) []string {
	if !s.active || s.anchor == s.cursor {
		return rows
	}
	start, end := s.ends()
	out := make([]string, len(rows))
	for i, line := range rows {
		from, to, ok := selectionSpan(start, end, i)
		if !ok {
			out[i] = line
			continue
		}
		if g := gutterAt(gutters, i); from < g {
			from = g
		}
		out[i] = highlightSpan(line, from, to)
	}
	return out
}

// text is the plain text the selection covers, one line per row and without the
// trailing padding. The prompt gutter is dropped too: dragging from the left edge
// must not paste "❯ " into the middle of a sentence. A drag that ends where it
// started covers nothing.
func (s rowsSelection) text(rows []string, gutters []int) string {
	if !s.active || s.anchor == s.cursor {
		return ""
	}
	start, end := s.ends()
	var out []string
	for i := range rows {
		from, to, ok := selectionSpan(start, end, i)
		if !ok {
			continue
		}
		if g := gutterAt(gutters, i); from < g {
			from = g
		}
		out = append(out, selectionRowText(rows[i], from, to))
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// gutterAt is how many leading cells of row i are prompt decoration (see
// rowsSelection); a missing or malformed entry means none.
func gutterAt(gutters []int, i int) int {
	if i < 0 || i >= len(gutters) || gutters[i] < 0 {
		return 0
	}
	return gutters[i]
}

// ends returns the selection ordered top-to-bottom, so an upwards drag selects
// exactly what a downwards one does.
func (s rowsSelection) ends() (cellPos, cellPos) {
	start, end := s.anchor, s.cursor
	if end.row < start.row || (end.row == start.row && end.col < start.col) {
		start, end = end, start
	}
	return start, end
}

// selectionSpan is the [from,to) column window a cell selection covers on row i;
// ok is false when the row is not part of it. It is shared by the transcript and
// the input bar so both copy the same cells they highlight.
func selectionSpan(start, end cellPos, i int) (int, int, bool) {
	if i < start.row || i > end.row {
		return 0, 0, false
	}
	switch {
	case start.row == end.row:
		return start.col, end.col + 1, true
	case i == start.row:
		return start.col, math.MaxInt, true
	case i == end.row:
		return 0, end.col + 1, true
	}
	return 0, math.MaxInt, true
}

// highlightSpan reverses the [from,to) window of an already rendered row. The
// window is measured in display cells, never in bytes, because the row carries
// styles and wide runes.
func highlightSpan(line string, from, to int) string {
	width := ansi.StringWidth(line)
	if from >= width {
		return line
	}
	if to > width {
		to = width
	}
	return ansi.Cut(line, 0, from) +
		ui.SelectionHighlight(ansi.Cut(line, from, to)) +
		ansi.Cut(line, to, width)
}

// selectionRowText is the plain text of the [from,to) window of one rendered
// row, without styles and without the trailing padding.
func selectionRowText(line string, from, to int) string {
	plain := ansi.Strip(line)
	width := ansi.StringWidth(plain)
	if from >= width {
		return ""
	}
	if to > width {
		to = width
	}
	return strings.TrimRight(ansi.Cut(plain, from, to), " ")
}
