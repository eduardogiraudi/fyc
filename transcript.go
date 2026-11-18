package main

import (
	"math"
	"strings"

	"fuckyourcli/ui"

	"github.com/charmbracelet/x/ansi"
)

// transcriptMaxLines bounds the transcript kept in memory. The alternate screen
// has no terminal scrollback to fall back on, so the app keeps its own — and a
// long session must not grow it without limit.
const transcriptMaxLines = 20000

// mouseScrollRows is how many rows one wheel notch moves. Three keeps the
// scrollback from crawling on high-resolution wheels while still feeling
// proportional to a terminal row.
const mouseScrollRows = 3

type transcriptBlock struct {
	rows   []string
	render func(int) string
}

// transcriptState holds the completed output of the alt-screen chat: rendered
// blocks are appended here instead of being printed into the terminal
// scrollback (see chatModel.printCmd), and view returns the window of rows the
// user is looking at.
//
// The state hangs off a pointer because chatModel is copied by value on every
// Bubble Tea message: the accumulated rows and the scroll position are shared,
// the frame is not.
type transcriptState struct {
	lines  []string
	blocks []transcriptBlock
	width  int
	// offset is how many rows the window is scrolled up from the bottom; zero
	// follows the live output.
	offset int
	// height is the row budget of the last render, used to clamp the scroll.
	height int
	// viewStart is the transcript index drawn on the first screen row of the
	// last render: mouse coordinates are relative to it.
	viewStart int
	// selAnchor/selCursor are the two ends of a drag selection, as row (index
	// into lines) and display column. selActive reports whether one exists.
	selActive bool
	selAnchor cellPos
	selCursor cellPos
}

// cellPos is a position in the transcript: which row, and which column of it.
type cellPos struct {
	row int
	col int
}

// Append adds an already-rendered block whose rows do not need reflow.
func (t *transcriptState) Append(block string) {
	t.appendBlock(block, nil)
}

// AppendRendered retains the source so resizing never reflows truncated rows.
func (t *transcriptState) AppendRendered(render func(int) string, width int) {
	t.width = width
	t.appendBlock(render(width), render)
}

func (t *transcriptState) appendBlock(block string, render func(int) string) {
	block = strings.TrimRight(block, "\n")
	if strings.TrimSpace(block) == "" {
		return
	}
	added := strings.Split(block, "\n")
	if t.offset > 0 {
		t.offset += len(added)
	}
	t.blocks = append(t.blocks, transcriptBlock{rows: added, render: render})
	t.lines = append(t.lines, added...)
	t.trim()
	t.clamp()
}

func (t *transcriptState) trim() {
	drop := len(t.lines) - transcriptMaxLines
	if drop <= 0 {
		return
	}
	t.lines = append([]string(nil), t.lines[drop:]...)
	t.selAnchor.row -= drop
	t.selCursor.row -= drop
	t.viewStart -= drop
	if t.selAnchor.row < 0 || t.selCursor.row < 0 {
		t.selActive = false
	}
	i := 0
	for i < len(t.blocks) && drop >= len(t.blocks[i].rows) {
		drop -= len(t.blocks[i].rows)
		i++
	}
	t.blocks = append([]transcriptBlock(nil), t.blocks[i:]...)
	if drop > 0 {
		// A partially evicted block becomes static: never resurrect its head.
		t.blocks[0] = transcriptBlock{rows: append([]string(nil), t.blocks[0].rows[drop:]...)}
	}
}

func (t *transcriptState) Resize(width int) {
	if width <= 0 || width == t.width {
		return
	}
	t.width = width
	start := max(0, len(t.lines)-t.height-t.offset)
	oldRow, newStart := 0, 0
	lines := make([]string, 0, len(t.lines))
	blocks := append([]transcriptBlock(nil), t.blocks...)
	for i, block := range blocks {
		rows := block.rows
		if block.render != nil {
			rows = strings.Split(strings.TrimRight(block.render(width), "\n"), "\n")
		}
		if start >= oldRow && start < oldRow+len(block.rows) {
			newStart = len(lines) + min(start-oldRow, len(rows)-1)
		}
		oldRow += len(block.rows)
		blocks[i].rows = rows
		lines = append(lines, rows...)
	}
	t.blocks, t.lines = blocks, lines
	if t.offset > 0 {
		t.offset = max(0, len(lines)-t.height-newStart)
	}
	t.SelectionClear()
	t.viewStart = newStart
	t.trim()
	t.clamp()
}

func (t *transcriptState) Clone() *transcriptState {
	copy := *t
	copy.lines = append([]string(nil), t.lines...)
	copy.blocks = append([]transcriptBlock(nil), t.blocks...)
	return &copy
}

// ScrollUp moves the window n rows towards the start of the transcript.
func (t *transcriptState) ScrollUp(n int) {
	t.offset += n
	t.clamp()
}

// ScrollDown moves the window n rows towards the live output.
func (t *transcriptState) ScrollDown(n int) {
	t.offset -= n
	t.clamp()
}

// ScrollTop shows the first row the window can hold.
func (t *transcriptState) ScrollTop() {
	t.offset = t.maxOffset()
}

// Follow jumps back to the live output.
func (t *transcriptState) Follow() {
	t.offset = 0
}

// ScrolledUp reports whether the reader is away from the live output.
func (t *transcriptState) ScrolledUp() bool {
	return t.offset > 0
}

// PageSize is how many rows pgup/pgdn move: one screen minus two rows of
// overlap, so the reader never loses the line they were on.
func (t *transcriptState) PageSize() int {
	if t.height <= 2 {
		return 1
	}
	return t.height - 2
}

// View returns the rows to draw in a region h rows tall. It also records h, so
// scrolling clamps against the region the user can actually see.
func (t *transcriptState) View(h int) string {
	t.height = h
	t.clamp()
	if h <= 0 || len(t.lines) == 0 {
		return ""
	}
	start := len(t.lines) - h - t.offset
	if start < 0 {
		start = 0
	}
	t.viewStart = start
	end := start + h
	if end > len(t.lines) {
		end = len(t.lines)
	}
	if !t.selActive {
		return strings.Join(t.lines[start:end], "\n")
	}
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, t.highlighted(i))
	}
	return strings.Join(rows, "\n")
}

func (t *transcriptState) maxOffset() int {
	if t.height <= 0 {
		return 0
	}
	if m := len(t.lines) - t.height; m > 0 {
		return m
	}
	return 0
}

func (t *transcriptState) clamp() {
	if max := t.maxOffset(); t.offset > max {
		t.offset = max
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

// SelectionStart begins a drag selection at a screen cell: y is the row of the
// frame the transcript occupies (0 = the first row drawn), x the column.
func (t *transcriptState) SelectionStart(y, x int) {
	pos := t.cellAt(y, x)
	t.selAnchor = pos
	t.selCursor = pos
	t.selActive = true
}

// SelectionExtend moves the loose end of the drag selection to a screen cell.
func (t *transcriptState) SelectionExtend(y, x int) {
	if !t.selActive {
		return
	}
	t.selCursor = t.cellAt(y, x)
}

// SelectionClear drops the selection: a drag that covered no text leaves
// nothing to copy, so the highlight goes away instead of staying behind.
func (t *transcriptState) SelectionClear() {
	t.selActive = false
}

// SelectionText is the plain text covered by the drag selection, one line per
// transcript row, without the styles and without trailing padding. It is empty
// when the drag covered no text at all.
func (t *transcriptState) SelectionText() string {
	if !t.selActive {
		return ""
	}
	start, end := t.selEnds()
	if start == end || start.row >= len(t.lines) {
		return ""
	}
	if end.row >= len(t.lines) {
		end.row = len(t.lines) - 1
	}

	var out []string
	for row := start.row; row <= end.row; row++ {
		from, to, ok := t.selectionSpan(row)
		if !ok {
			continue
		}
		plain := ansi.Strip(t.lines[row])
		width := ansi.StringWidth(plain)
		if from >= width {
			out = append(out, "")
			continue
		}
		if to > width {
			to = width
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, to), " "))
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// cellAt maps a screen cell of the transcript region to a transcript position,
// clamped to the rows and columns that exist.
func (t *transcriptState) cellAt(y, x int) cellPos {
	row := t.viewStart + max(0, y)
	if row >= len(t.lines) {
		row = len(t.lines) - 1
	}
	if row < 0 {
		row = 0
	}
	if x < 0 {
		x = 0
	}
	return cellPos{row: row, col: x}
}

// highlighted draws row i, painting the part covered by the selection.
func (t *transcriptState) highlighted(i int) string {
	line := t.lines[i]
	if !t.selActive {
		return line
	}
	start, end := t.selEnds()
	if start == end {
		return line
	}
	from, to, ok := t.selectionSpan(i)
	if !ok {
		return line
	}
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

// selEnds returns the selection ordered top-to-bottom.
func (t *transcriptState) selEnds() (cellPos, cellPos) {
	start, end := t.selAnchor, t.selCursor
	if end.row < start.row || (end.row == start.row && end.col < start.col) {
		start, end = end, start
	}
	return start, end
}

// selectionSpan is the [from,to) column window of row i the selection covers;
// ok is false when the row is not part of it.
func (t *transcriptState) selectionSpan(i int) (int, int, bool) {
	if !t.selActive {
		return 0, 0, false
	}
	start, end := t.selEnds()
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
