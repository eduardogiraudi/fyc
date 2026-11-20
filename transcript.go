package main

import (
	"strings"

	"fuckyourcli/ui"
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
	// expand is set on a block the alt-screen chat expands in place (a tool
	// result): controls are the rows of rows a click acts on.
	expand   expandable
	controls []ui.BlockControl
}

// expandable is a transcript block whose rows change on a click or on hover.
// Toggle and SetHover report whether anything changed, so the transcript knows
// to re-render the block.
type expandable interface {
	Rows(width int) ([]string, []ui.BlockControl)
	Toggle(id int) bool
	SetHover(id int) bool
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
	// hoverBlock/hoverControl are the control under the pointer: the block
	// repaints that row white, so only it knows what is highlighted.
	hoverActive  bool
	hoverBlock   int
	hoverControl int
	// tail is the output still in progress (reasoning, tool, live text): it is
	// drawn after the last completed row and scrolls with it, but is not part
	// of lines until it completes.
	tail []string
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

// AppendTool adds a tool result the alt-screen chat expands in place: its
// marker rows are clickable controls, not decoration.
func (t *transcriptState) AppendTool(tool *ui.ToolOutput) {
	if tool == nil {
		return
	}
	rows, controls := tool.Rows(t.width)
	t.appendExpandable(rows, controls, tool)
}

// AppendExpandable adds any other block the alt-screen chat expands in place (a
// finished reasoning trace): its control rows are clickable exactly like a tool
// result's.
func (t *transcriptState) AppendExpandable(e expandable) {
	if e == nil {
		return
	}
	width := t.width
	if width <= 0 {
		width = 80
	}
	rows, controls := e.Rows(width)
	t.appendExpandable(rows, controls, e)
}

func (t *transcriptState) appendExpandable(rows []string, controls []ui.BlockControl, e expandable) {
	if len(rows) == 0 {
		return
	}
	if t.offset > 0 {
		t.offset += len(rows)
	}
	t.blocks = append(t.blocks, transcriptBlock{rows: rows, expand: e, controls: controls})
	t.lines = append(t.lines, rows...)
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
	t.resetHover()
}

func (t *transcriptState) Resize(width int) {
	if width <= 0 || width == t.width {
		return
	}
	t.width = width
	t.resetHover()
	start := max(0, len(t.lines)-t.height-t.offset)
	oldRow, newStart := 0, 0
	lines := make([]string, 0, len(t.lines))
	blocks := append([]transcriptBlock(nil), t.blocks...)
	for i, block := range blocks {
		rows := block.rows
		switch {
		case block.expand != nil:
			rows, block.controls = block.expand.Rows(width)
		case block.render != nil:
			rows = strings.Split(strings.TrimRight(block.render(width), "\n"), "\n")
		}
		if start >= oldRow && start < oldRow+len(block.rows) {
			newStart = len(lines) + min(start-oldRow, len(rows)-1)
		}
		oldRow += len(block.rows)
		block.rows = rows
		blocks[i] = block
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
	return t.ViewWithTail(h, nil)
}

// ViewWithTail is View with the in-progress output appended after the last
// completed row. The tail scrolls like the rest of the transcript; while the
// reader is scrolled up its growth is compensated so the rows on screen stay
// still.
func (t *transcriptState) ViewWithTail(h int, tail []string) string {
	if t.offset > 0 {
		t.offset = max(0, t.offset+len(tail)-len(t.tail))
	}
	t.tail = tail
	t.height = h
	t.clamp()
	total := len(t.lines) + len(tail)
	if h <= 0 || total == 0 {
		return ""
	}
	start := total - h - t.offset
	if start < 0 {
		start = 0
	}
	t.viewStart = start
	end := start + h
	if end > total {
		end = total
	}
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		switch {
		case i >= len(t.lines):
			rows = append(rows, tail[i-len(t.lines)])
		case t.selActive:
			rows = append(rows, t.highlighted(i))
		default:
			rows = append(rows, t.lines[i])
		}
	}
	return strings.Join(rows, "\n")
}

func (t *transcriptState) maxOffset() int {
	if t.height <= 0 {
		return 0
	}
	if m := len(t.lines) + len(t.tail) - t.height; m > 0 {
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

// rowAt maps a screen row of the transcript region (0 = the first row drawn) to
// a transcript row, or -1 when that row is outside what the transcript drew.
func (t *transcriptState) rowAt(screenRow int) int {
	if screenRow < 0 || screenRow >= t.height {
		return -1
	}
	row := t.viewStart + screenRow
	if row < 0 || row >= len(t.lines) {
		return -1
	}
	return row
}

// ToggleAt flips the expandable control drawn on screen row y. It reports
// whether that row was a control: a click there is a button press, never the
// start of a text selection.
func (t *transcriptState) ToggleAt(x, y int) bool {
	row := t.rowAt(y)
	if row < 0 {
		return false
	}
	idx, offset, ok := t.blockAt(row)
	if !ok || t.blocks[idx].expand == nil {
		return false
	}
	id := -1
	for _, c := range t.blocks[idx].controls {
		if c.Row == offset && x >= 0 && x < c.Width {
			id = c.ID
			break
		}
	}
	if id < 0 {
		return false
	}
	if t.blocks[idx].expand.Toggle(id) {
		t.rerenderBlock(idx)
	}
	return true
}

// HoverAt highlights the control under the pointer and drops the highlight when
// the pointer is anywhere else on the transcript, including on the empty cells
// to the right of a control row.
func (t *transcriptState) HoverAt(x, y int) {
	idx, id, ok := t.controlUnder(x, y)
	if !ok {
		t.ClearHover()
		return
	}
	if t.hoverActive && t.hoverBlock == idx && t.hoverControl == id {
		return
	}
	t.ClearHover()
	t.hoverActive, t.hoverBlock, t.hoverControl = true, idx, id
	if t.blocks[idx].expand.SetHover(id) {
		t.redrawBlock(idx)
	}
}

// ClearHover drops the control highlight.
func (t *transcriptState) ClearHover() {
	if !t.hoverActive {
		return
	}
	idx := t.hoverBlock
	t.hoverActive = false
	if idx >= 0 && idx < len(t.blocks) && t.blocks[idx].expand != nil && t.blocks[idx].expand.SetHover(-1) {
		t.redrawBlock(idx)
	}
}

// resetHover forgets the highlight without redrawing: the block is about to be
// re-rendered from scratch anyway.
func (t *transcriptState) resetHover() {
	if t.hoverActive && t.hoverBlock >= 0 && t.hoverBlock < len(t.blocks) && t.blocks[t.hoverBlock].expand != nil {
		t.blocks[t.hoverBlock].expand.SetHover(-1)
	}
	t.hoverActive = false
	t.hoverBlock, t.hoverControl = 0, 0
}

// controlUnder maps a screen row of the transcript region to the block and the
// control drawn there.
func (t *transcriptState) controlUnder(x, y int) (int, int, bool) {
	row := t.rowAt(y)
	if row < 0 {
		return 0, 0, false
	}
	idx, offset, ok := t.blockAt(row)
	if !ok || t.blocks[idx].expand == nil {
		return 0, 0, false
	}
	for _, c := range t.blocks[idx].controls {
		if c.Row == offset && x >= 0 && x < c.Width {
			return idx, c.ID, true
		}
	}
	return 0, 0, false
}

// blockAt locates the block that owns transcript row and the row's offset in it.
func (t *transcriptState) blockAt(row int) (int, int, bool) {
	start := 0
	for i := range t.blocks {
		n := len(t.blocks[i].rows)
		if row >= start && row < start+n {
			return i, row - start, true
		}
		start += n
	}
	return 0, 0, false
}

// redrawBlock re-renders block idx in place and returns how many rows it gained
// (negative when it lost some).
func (t *transcriptState) redrawBlock(idx int) int {
	block := &t.blocks[idx]
	start := 0
	for i := 0; i < idx; i++ {
		start += len(t.blocks[i].rows)
	}
	old := len(block.rows)
	rows, controls := block.expand.Rows(t.width)
	block.rows, block.controls = rows, controls

	lines := make([]string, 0, len(t.lines)+len(rows)-old)
	lines = append(lines, t.lines[:start]...)
	lines = append(lines, rows...)
	lines = append(lines, t.lines[start+old:]...)
	t.lines = lines
	return len(rows) - old
}

// rerenderBlock redraws block idx after its expandable part changed, keeping
// the reader's window anchored on what it was showing.
func (t *transcriptState) rerenderBlock(idx int) {
	delta := t.redrawBlock(idx)
	t.SelectionClear()
	t.resetHover()
	// A reader who was scrolled up keeps the row they were looking at: the
	// revealed rows land where the clicked control was. A reader following the
	// live output keeps following it, so the expansion shows at the bottom.
	if delta != 0 && t.offset > 0 {
		t.offset = max(0, len(t.lines)-t.height-t.viewStart)
	}
	t.trim()
	t.clamp()
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

	// One empty string per row the selection touches (a row whose selection
	// starts past its end contributes nothing), so the rows keep their place in
	// the joined text.
	var out []string
	for row := start.row; row <= end.row; row++ {
		from, to, ok := t.selectionSpan(row)
		if !ok {
			continue
		}
		out = append(out, selectionRowText(t.lines[row], from, to))
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
	return highlightSpan(line, from, to)
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
// ok is false when the row is not part of it. The span itself is shared with the
// input bar's selection (see selectionSpan in input_selection.go), so both copy
// exactly the cells they highlight.
func (t *transcriptState) selectionSpan(i int) (int, int, bool) {
	if !t.selActive {
		return 0, 0, false
	}
	start, end := t.selEnds()
	return selectionSpan(start, end, i)
}
