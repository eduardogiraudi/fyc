package tools

import (
	"fmt"
	"hash/crc32"
	"math/bits"
	"strings"
	"sync"
)

// anchorDelimiter separates the anchor id from the line content in get_anchors
// output ("k7fm4§package main"). It can legitimately appear in source code, so
// nothing parses it blindly: a prefix is only treated as an anchor when the text
// before the delimiter is a token this file actually issued (see stripKnownAnchors).
const anchorDelimiter = "§"

// Anchor ids are short opaque tokens ("k7f"). The alphabet drops the look-alikes
// i, l, o, 0 and 1 so a model copying an id cannot confuse them.
const (
	anchorAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	anchorRadix    = uint64(len(anchorAlphabet))
	anchorMinLen   = 3
	anchorMaxLen   = 12
)

// anchorID turns the n-th issued index into an anchor id.
//
// Ids of length L are drawn from a pool of 31^L values and issued through a
// bijective scramble (multiplication by a constant coprime to the radix), so:
//   - an id is never issued twice, exactly like a plain counter;
//   - consecutive ids look unrelated, so a model cannot guess "the next one";
//   - three characters cover the first 29 791 lines of a session, then ids grow
//     to four characters, and so on.
func anchorID(n uint64) string {
	length, size := anchorMinLen, uint64(1)
	for i := 0; i < length; i++ {
		size *= anchorRadix
	}
	for n >= size && length < anchorMaxLen {
		n -= size
		length++
		size *= anchorRadix
	}
	mult := uint64(float64(size) * 0.6180339887498949)
	if mult%anchorRadix == 0 {
		mult++ // coprime to size (a power of the radix) iff not divisible by it
	}
	hi, lo := bits.Mul64(n%size, mult)
	_, idx := bits.Div64(hi, lo, size) // hi < size because both factors are < size
	idx = (idx + size/7) % size        // shift so the very first id is not "aaa"
	buf := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		buf[i] = anchorAlphabet[idx%anchorRadix]
		idx /= anchorRadix
	}
	return string(buf)
}

// isAnchorShape reports whether s could be a bare anchor id (right length, alphabet).
func isAnchorShape(s string) bool {
	if len(s) < anchorMinLen || len(s) > anchorMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(anchorAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// What the model sees and must send back is not the bare id but the anchor
// token: the id followed by anchorCheckLen check characters derived from it
// ("k7f" + "m4" = "k7fm4"). Internally every map stays keyed by the bare id; the
// check exists only at the boundary. A token whose check does not match is
// refused before anything else is looked at, so a mistyped or half-remembered
// anchor that happens to spell another line's id cannot reach that line.
// With the 31-character alphabet, 2 check characters let a wrong token through
// about once in 961 tries.
const anchorCheckLen = 2

// anchorCheck returns the check characters for a bare id.
func anchorCheck(id string) string {
	v := crc32.ChecksumIEEE([]byte(id)) % uint32(anchorRadix*anchorRadix)
	return string([]byte{anchorAlphabet[v/uint32(anchorRadix)], anchorAlphabet[v%uint32(anchorRadix)]})
}

// anchorToken returns the text shown to the model for a bare id.
func anchorToken(id string) string { return id + anchorCheck(id) }

// splitAnchorToken returns the bare id of a token and whether the token is
// well formed (id shape) with a matching check.
func splitAnchorToken(tok string) (id string, ok bool) {
	if len(tok) < anchorMinLen+anchorCheckLen {
		return "", false
	}
	id, check := tok[:len(tok)-anchorCheckLen], tok[len(tok)-anchorCheckLen:]
	if !isAnchorShape(id) || check != anchorCheck(id) {
		return "", false
	}
	return id, true
}

// ── Diff engine ──────────────────────────────────────────────────────────────

type diffOpType int

const (
	diffEqual  diffOpType = iota
	diffInsert            // present in new, absent in old
	diffDelete            // present in old, absent in new
)

type diffOp struct {
	opType diffOpType
	text   string
}

// myersDiff computes a minimal edit script between a and b using Myers' algorithm.
//
// Complexity: O((n+m)·d) time, O((n+m)·d) space, where d is the edit distance.
// For typical source-file edits (d << n+m) this is substantially faster than the
// O(n·m) LCS approach, and produces the same shortest edit script.
//
// The algorithm works in two phases:
//  1. Forward pass: for each edit depth d (0, 1, 2, …) sweep all reachable
//     diagonals k = x−y and record the furthest-reaching x coordinate. A
//     "snake" of consecutive matching elements is greedily consumed at each
//     step. We store a snapshot of the frontier array after every depth so
//     that the path can be reconstructed.
//  2. Backtracking: walk the saved snapshots in reverse to recover the exact
//     sequence of equal/delete/insert operations.
func myersDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)

	// Fast paths
	if n == 0 {
		ops := make([]diffOp, m)
		for i, s := range b {
			ops[i] = diffOp{diffInsert, s}
		}
		return ops
	}
	if m == 0 {
		ops := make([]diffOp, n)
		for i, s := range a {
			ops[i] = diffOp{diffDelete, s}
		}
		return ops
	}

	max := n + m
	offset := max // shift so that negative diagonal indices are valid
	size := 2*max + 1

	// v[k+offset] = furthest x reached on diagonal k.
	v := make([]int, size)

	// trace[d] is a snapshot of v taken at the *start* of round d (i.e. after
	// round d−1 has fully settled). Used during backtracking.
	trace := make([][]int, 0, max+1)

	lastD := -1

outer:
	for d := 0; d <= max; d++ {
		// Snapshot before this round.
		snap := make([]int, size)
		copy(snap, v)
		trace = append(trace, snap)

		for k := -d; k <= d; k += 2 {
			// Decide whether to move right (delete from a) or down (insert from b).
			var x int
			if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
				x = v[k+1+offset] // move down → insert b[y]
			} else {
				x = v[k-1+offset] + 1 // move right → delete a[x-1]
			}
			y := x - k

			// Greedily extend along matching elements (the "snake").
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+offset] = x

			if x == n && y == m {
				lastD = d
				break outer
			}
		}
	}

	// ── Backtrack ────────────────────────────────────────────────────────────
	// Reconstruct ops in reverse order, then flip at the end.
	ops := make([]diffOp, 0, n+m)
	x, y := n, m

	for d := lastD; d > 0; d-- {
		vd := trace[d]
		k := x - y

		// Determine whether diagonal k was reached via an insert (from k+1)
		// or a delete (from k−1).
		fromInsert := k == -d || (k != d && vd[k-1+offset] < vd[k+1+offset])

		var prevK int
		if fromInsert {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vd[prevK+offset]
		prevY := prevX - prevK

		// The snake starts right after the single edit.
		var snakeX, snakeY int
		if fromInsert {
			snakeX, snakeY = prevX, prevY+1
		} else {
			snakeX, snakeY = prevX+1, prevY
		}

		// Walk back along the snake (equal elements, emitted in reverse).
		for x > snakeX && y > snakeY {
			x--
			y--
			ops = append(ops, diffOp{diffEqual, a[x]})
		}

		// Emit the single edit that preceded the snake.
		if fromInsert {
			y--
			ops = append(ops, diffOp{diffInsert, b[y]})
		} else {
			x--
			ops = append(ops, diffOp{diffDelete, a[x]})
		}
		// Invariant: (x, y) == (prevX, prevY) after this point.
	}

	// Consume the initial snake at d=0 (common prefix before any edits).
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, diffOp{diffEqual, a[x]})
	}

	// Reverse in place to restore forward order.
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

// ── State types ───────────────────────────────────────────────────────────────

// staleAnchor explains why an anchor cannot be used as-is.
type staleAnchor struct {
	msg     string
	oneShot bool     // reported once, then cleared (a neighbour of an external change)
	reveals []string // new anchors named in msg: they count as shown once reported
}

// FileState holds the current anchor assignment for one file.
type FileState struct {
	lines        []string
	content      string // Exact disk snapshot, including the final newline.
	lineToAnchor map[int]string
	anchorToLine map[string]int

	// stale maps retired anchors (permanent) and anchors next to an external
	// change (one-shot) to the reason they were refused. Retired anchors keep the
	// text they had, which is what turns a refusal into "expected X but got Y".
	stale map[string]staleAnchor
	// unseen holds anchors created by an external change that the model has not
	// been shown yet; a patch range must not silently swallow them.
	unseen map[string]bool
}

func newFileState(lines []string) *FileState {
	return &FileState{
		lines:        append([]string(nil), lines...),
		content:      JoinLines(lines),
		lineToAnchor: make(map[int]string, len(lines)),
		anchorToLine: make(map[string]int, len(lines)),
		stale:        make(map[string]staleAnchor),
		unseen:       make(map[string]bool),
	}
}

// Lines returns a copy of the current lines (safe to read outside the lock).
func (fs *FileState) Lines() []string {
	out := make([]string, len(fs.lines))
	copy(out, fs.lines)
	return out
}

// ── State manager ─────────────────────────────────────────────────────────────

// AnchorStateManager tracks anchor assignments for all files in the current session.
// It is safe for concurrent use.
type AnchorStateManager struct {
	opMu   sync.Mutex // Serializes anchored disk operations, never held during approval.
	mu     sync.Mutex
	files  map[string]*FileState
	issued uint64 // number of ids handed out so far; never rewound
}

// globalAnchorState is the session-scoped singleton.
var globalAnchorState = &AnchorStateManager{
	files: make(map[string]*FileState),
}

// ResetAnchorState invalidates all cached anchors when the chat context is cleared.
func ResetAnchorState() { globalAnchorState.Reset() }

func (m *AnchorStateManager) Reset() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files = make(map[string]*FileState)
	// Do not rewind the id counter: an in-flight patch must not regain validity after a re-read.
}

// EnsureContent records the exact bytes read by get_anchors, not just split lines.
func (m *AnchorStateManager) EnsureContent(absPath, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAnchors(absPath, SplitLines(content))
	m.files[absPath].content = content
}

// syncWithDisk brings the cached file up to date with the exact bytes now on
// disk. Unchanged lines keep their anchor; changed lines are retired together
// with the text they had, so a patch that still targets them is refused with an
// "expected … but got …" message instead of landing on the wrong content.
//
// A change that only touches line endings or the final newline leaves every
// line identical, so there is nothing to reconcile: that case drops the cache
// and requires an explicit get_anchors.
func (m *AnchorStateManager) syncWithDisk(absPath, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs := m.files[absPath]
	if fs == nil {
		return fmt.Errorf("file not loaded — call get_anchors first")
	}
	if fs.content == content {
		return nil
	}
	lines := SplitLines(content)
	if slicesEqual(fs.lines, lines) {
		delete(m.files, absPath)
		return fmt.Errorf("file changed on disk since the last anchored read/edit (final newline) — no changes written; call get_anchors again and reconsider the patch using the current content")
	}
	m.reconcile(fs, lines)
	fs.content = content
	return nil
}

// nextAnchor returns a fresh anchor id. Must be called with m.mu held.
func (m *AnchorStateManager) nextAnchor() string {
	id := anchorID(m.issued)
	m.issued++
	return id
}

// EnsureAnchors makes sure absPath has anchors for exactly these lines.
//   - Not yet loaded → assign fresh anchors.
//   - Already loaded, same content → return existing anchors unchanged.
//     A second get_anchors call on an unmodified file must not consume new ids.
//   - Already loaded, content differs (external edit) → reconcile with Myers
//     so unchanged lines keep their anchor and only new lines get fresh ones.
func (m *AnchorStateManager) EnsureAnchors(absPath string, lines []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAnchors(absPath, lines)
}

// Must be called with m.mu held.
func (m *AnchorStateManager) ensureAnchors(absPath string, lines []string) {
	fs := m.files[absPath]
	if fs == nil {
		fs = newFileState(lines)
		for i := range lines {
			a := m.nextAnchor()
			fs.lineToAnchor[i] = a
			fs.anchorToLine[a] = i
		}
		m.files[absPath] = fs
		return
	}
	if slicesEqual(fs.lines, lines) {
		return
	}
	m.reconcile(fs, lines)
}

// reconcile moves fs from its current lines to the given ones. Must be called
// with m.mu held.
//
// Besides carrying anchors over, it records what the model needs to know about
// the change: retired anchors with their old text (paired with the line that
// replaced them when there is one), the new anchors it has not been shown, and
// a one-shot warning on the unchanged lines bordering each changed hunk — a
// repeated line such as "}" may have been matched to the wrong twin there.
func (m *AnchorStateManager) reconcile(fs *FileState, lines []string) {
	ops := myersDiff(fs.lines, lines)
	newL2A := make(map[int]string, len(lines))
	newA2L := make(map[string]int, len(lines))
	oldI, newI := 0, 0

	var dels, ins []int // the current run of changed lines (old / new indices)
	prevEqual := ""     // anchor of the unchanged line just before the run

	flush := func(nextEqual string) {
		if len(dels) == 0 && len(ins) == 0 {
			return
		}
		var shown []string
		for _, ni := range ins {
			a := newL2A[ni]
			fs.unseen[a] = true
			if len(shown) < 3 {
				shown = append(shown, anchorToken(a)+anchorDelimiter+clipLine(lines[ni]))
			}
		}
		for i, d := range dels {
			old := fs.lineToAnchor[d]
			if i < len(ins) {
				now := anchorToken(newL2A[ins[i]])
				fs.stale[old] = staleAnchor{
					msg:     fmt.Sprintf("line changed on disk: expected %q but got %q (now %s)", clipLine(fs.lines[d]), clipLine(lines[ins[i]]), now),
					reveals: []string{newL2A[ins[i]]},
				}
			} else {
				fs.stale[old] = staleAnchor{msg: fmt.Sprintf("line removed on disk: expected %q", clipLine(fs.lines[d]))}
			}
		}
		now := fmt.Sprintf("%d line(s) removed", len(dels))
		var reveals []string
		if len(ins) > 0 {
			now = strings.Join(shown, "; ")
			if len(ins) > len(shown) {
				now += "; …"
			}
			for _, ni := range ins {
				reveals = append(reveals, newL2A[ni])
			}
		}
		for _, nb := range []string{prevEqual, nextEqual} {
			if nb != "" {
				fs.stale[nb] = staleAnchor{
					msg:     "neighbouring lines changed on disk (now: " + now + "); retry to confirm it still targets the right line",
					oneShot: true,
					reveals: reveals,
				}
			}
		}
		dels, ins = nil, nil
	}

	for _, op := range ops {
		switch op.opType {
		case diffEqual:
			a := fs.lineToAnchor[oldI]
			flush(a)
			newL2A[newI] = a
			newA2L[a] = newI
			prevEqual = a
			oldI++
			newI++
		case diffDelete:
			dels = append(dels, oldI)
			oldI++
		case diffInsert:
			a := m.nextAnchor()
			newL2A[newI] = a
			newA2L[a] = newI
			ins = append(ins, newI)
			newI++
		}
	}
	flush("")

	fs.lines = append([]string(nil), lines...)
	fs.content = JoinLines(lines)
	fs.lineToAnchor = newL2A
	fs.anchorToLine = newA2L
}

// MarkSeen records that the anchors printed in output reached the model: they
// stop counting as unseen and their one-shot warnings are dropped.
func (m *AnchorStateManager) MarkSeen(absPath, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs := m.files[absPath]
	if fs == nil {
		return
	}
	for _, line := range strings.Split(output, "\n") {
		tok, _, ok := strings.Cut(line, anchorDelimiter)
		if !ok {
			continue
		}
		id, valid := splitAnchorToken(tok)
		if !valid {
			continue
		}
		delete(fs.unseen, id)
		if st, found := fs.stale[id]; found && st.oneShot {
			delete(fs.stale, id)
		}
	}
}

// slicesEqual reports whether a and b contain identical strings in the same order.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GetAnnotated returns the full file content with anchor prefixes, ready to
// send to the LLM. Returns an error if the file has not been loaded yet.
func (m *AnchorStateManager) GetAnnotated(absPath string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs := m.files[absPath]
	if fs == nil {
		return "", fmt.Errorf("file not loaded — call get_anchors first")
	}
	return buildAnnotated(fs), nil
}

// PlanEdit validates the anchors and computes what the file would look like
// after the edit. It never touches the file or the anchor assignment; the only
// state it changes is the bookkeeping of warnings it reports (a warning is shown
// once, so the retry goes through). Returns (oldLines, newLines, range, err); the
// range is what CommitEdit needs to keep every untouched line's anchor.
//
// start_anchor and end_anchor are anchor tokens exactly as printed by get_anchors
// ("k7fm4": id plus its check characters). A full annotated line ("k7fm4§code")
// is tolerated; a token whose check does not match is refused.
func (m *AnchorStateManager) PlanEdit(absPath, startAnchor, endAnchor, replacement string) ([]string, []string, editRange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fs := m.files[absPath]
	if fs == nil {
		return nil, nil, editRange{}, fmt.Errorf("file not loaded — call get_anchors first")
	}

	startLine, startID, startProblem := m.lookupAnchor(fs, startAnchor)
	endLine, endID, endProblem := m.lookupAnchor(fs, endAnchor)
	sameAnchor := normalizeAnchor(startAnchor) == normalizeAnchor(endAnchor)
	if startProblem != "" || endProblem != "" {
		var parts []string
		if startProblem != "" {
			parts = append(parts, "start_anchor: "+startProblem)
			m.acknowledge(fs, startID)
		}
		if endProblem != "" && !(startProblem != "" && sameAnchor) {
			parts = append(parts, "end_anchor: "+endProblem)
			m.acknowledge(fs, endID)
		}
		return nil, nil, editRange{}, fmt.Errorf("%s; no changes written", strings.Join(parts, "; "))
	}
	if startLine > endLine {
		return nil, nil, editRange{}, fmt.Errorf("start_anchor (line %d) must come before end_anchor (line %d) in the file", startLine+1, endLine+1)
	}

	// Lines that appeared on disk after the last read and were never shown must
	// not be replaced or deleted without the model having seen them.
	var hidden []string
	for i := startLine; i <= endLine; i++ {
		if a := fs.lineToAnchor[i]; fs.unseen[a] {
			hidden = append(hidden, anchorToken(a)+anchorDelimiter+clipLine(fs.lines[i]))
		}
	}
	if len(hidden) > 0 {
		for i := startLine; i <= endLine; i++ {
			delete(fs.unseen, fs.lineToAnchor[i])
		}
		return nil, nil, editRange{}, fmt.Errorf("the range contains lines added on disk that you have not seen: %s; no changes written — retry if replacing them is intended", strings.Join(hidden, "; "))
	}

	replacement = fs.stripKnownAnchors(replacement)
	var replacementLines []string
	if replacement != "" {
		// "\n" alone is one blank line; one trailing newline never adds a phantom line.
		replacementLines = strings.Split(strings.TrimSuffix(replacement, "\n"), "\n")
	}

	newLines := make([]string, 0, len(fs.lines)-(endLine-startLine+1)+len(replacementLines))
	newLines = append(newLines, fs.lines[:startLine]...)
	newLines = append(newLines, replacementLines...)
	newLines = append(newLines, fs.lines[endLine+1:]...)

	return fs.Lines(), newLines, editRange{start: startLine, end: endLine, count: len(replacementLines)}, nil
}

// PreviewEdit is PlanEdit without the edited range.
func (m *AnchorStateManager) PreviewEdit(absPath, startAnchor, endAnchor, replacement string) ([]string, []string, error) {
	oldLines, newLines, _, err := m.PlanEdit(absPath, startAnchor, endAnchor, replacement)
	return oldLines, newLines, err
}

// editRange locates a patch inside the file: the old lines [start, end]
// (0-based, inclusive) were replaced by count new lines.
type editRange struct{ start, end, count int }

// normalizeAnchor extracts the anchor token from what the model passed: it drops
// a copied "§content" tail and surrounding space and lowercases the rest.
func normalizeAnchor(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, anchorDelimiter); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// lookupAnchor resolves an anchor token to a line index without changing any
// state. id is the bare id when the token is valid ("" otherwise); problem is
// empty on success. Must be called with m.mu held.
func (m *AnchorStateManager) lookupAnchor(fs *FileState, raw string) (line int, id, problem string) {
	tok := normalizeAnchor(raw)
	id, ok := splitAnchorToken(tok)
	if !ok {
		return 0, "", m.badToken(fs, raw, tok)
	}
	if st, ok := fs.stale[id]; ok {
		return 0, id, fmt.Sprintf("anchor %s is stale — %s", tok, st.msg)
	}
	line, ok = fs.anchorToLine[id]
	if !ok {
		return 0, id, fmt.Sprintf("anchor %q not found — call get_anchors again", raw)
	}
	return line, id, ""
}

// badToken explains why tok is not a valid anchor token. It never reveals the
// correct check characters of a mistyped anchor (that would defeat the check);
// the one exception is a known bare id sent without any check characters, where
// the model demonstrably copied the id right and only needs the full token.
func (m *AnchorStateManager) badToken(fs *FileState, raw, tok string) string {
	known := func(id string) bool {
		_, live := fs.anchorToLine[id]
		_, retired := fs.stale[id]
		return live || retired
	}
	if known(tok) {
		return fmt.Sprintf("anchor %q is missing its %d check characters — use the full anchor %s exactly as printed by get_anchors", raw, anchorCheckLen, anchorToken(tok))
	}
	if len(tok) > anchorCheckLen && known(tok[:len(tok)-anchorCheckLen]) {
		return fmt.Sprintf("anchor %q has wrong check characters — copy the full anchor (id plus its %d check characters) exactly as printed by get_anchors, or call get_anchors again", raw, anchorCheckLen)
	}
	return fmt.Sprintf("anchor %q not found — call get_anchors again", raw)
}

// acknowledge marks a refusal as reported: its one-shot warning is dropped and
// the new anchors it named count as shown. Must be called with m.mu held.
func (m *AnchorStateManager) acknowledge(fs *FileState, id string) {
	st, ok := fs.stale[id]
	if !ok {
		return
	}
	for _, r := range st.reveals {
		delete(fs.unseen, r)
	}
	if st.oneShot {
		delete(fs.stale, id)
	}
}

// stripKnownAnchors removes "token§" prefixes the model copied from get_anchors
// output into a replacement. A prefix only counts when it is a valid token (check
// characters included) for an id this file has issued, so a line that merely
// contains or starts with "§" is left alone.
func (fs *FileState) stripKnownAnchors(s string) string {
	if !strings.Contains(s, anchorDelimiter) {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		tok, rest, ok := strings.Cut(l, anchorDelimiter)
		if !ok {
			continue
		}
		id, valid := splitAnchorToken(strings.ToLower(tok))
		if !valid {
			continue
		}
		_, live := fs.anchorToLine[id]
		_, retired := fs.stale[id]
		if live || retired {
			lines[i] = rest
		}
	}
	return strings.Join(lines, "\n")
}

// editOps maps the old lines onto the new ones after a patch. Lines outside the
// patched range keep their anchor by position: two identical lines (a closing
// brace, a blank line) must never swap anchors because a diff could not tell
// them apart. Myers only runs inside the range the model addressed, where it
// lets an unchanged line keep its anchor (the "insert after line" pattern).
// A range that does not describe the change falls back to a whole-file diff.
func editOps(r editRange, oldLines, newLines []string) []diffOp {
	ok := r.start >= 0 && r.start <= r.end && r.end < len(oldLines) &&
		r.count >= 0 && r.start+r.count <= len(newLines) &&
		len(newLines)-r.count == len(oldLines)-(r.end-r.start+1) &&
		slicesEqual(oldLines[:r.start], newLines[:r.start]) &&
		slicesEqual(oldLines[r.end+1:], newLines[r.start+r.count:])
	if !ok {
		return myersDiff(oldLines, newLines)
	}
	ops := make([]diffOp, 0, len(oldLines)+r.count)
	for _, l := range oldLines[:r.start] {
		ops = append(ops, diffOp{diffEqual, l})
	}
	ops = append(ops, myersDiff(oldLines[r.start:r.end+1], newLines[r.start:r.start+r.count])...)
	for _, l := range oldLines[r.end+1:] {
		ops = append(ops, diffOp{diffEqual, l})
	}
	return ops
}

// CommitEdit applies newLines to the file state and returns:
//   - fullAnnotated: the complete updated file (for internal use / re-read)
//   - insertedOnly:  only the lines that are new (diffInsert), annotated with
//     their fresh anchors — this is what replace_anchors returns to the LLM so
//     it can make further edits without re-reading the whole file.
//
// Call this only after the edit has been written to disk.
func (m *AnchorStateManager) CommitEdit(absPath string, r editRange, oldLines, newLines []string) (fullAnnotated, insertedOnly string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fs := m.files[absPath]
	if fs == nil {
		// Shouldn't happen if PreviewEdit succeeded, but be safe.
		fs = newFileState(newLines)
		for i := range newLines {
			a := m.nextAnchor()
			fs.lineToAnchor[i] = a
			fs.anchorToLine[a] = i
		}
		m.files[absPath] = fs
		full := buildAnnotated(fs)
		return full, full
	}

	ops := editOps(r, oldLines, newLines)

	newL2A := make(map[int]string, len(newLines))
	newA2L := make(map[string]int, len(newLines))
	oldI, newI := 0, 0
	var insertedBuf strings.Builder

	for _, op := range ops {
		switch op.opType {
		case diffEqual:
			a := fs.lineToAnchor[oldI]
			newL2A[newI] = a
			newA2L[a] = newI
			oldI++
			newI++
		case diffDelete:
			fs.stale[fs.lineToAnchor[oldI]] = staleAnchor{
				msg: "the line was replaced or deleted by an earlier edit — use the anchors that edit returned, or call get_anchors",
			}
			oldI++
		case diffInsert:
			a := m.nextAnchor()
			newL2A[newI] = a
			newA2L[a] = newI
			if insertedBuf.Len() > 0 {
				insertedBuf.WriteByte('\n')
			}
			insertedBuf.WriteString(anchorToken(a))
			insertedBuf.WriteString(anchorDelimiter)
			insertedBuf.WriteString(op.text)
			newI++
		}
	}

	fs.lines = newLines
	fs.content = JoinLines(newLines)
	fs.lineToAnchor = newL2A
	fs.anchorToLine = newA2L

	return buildAnnotated(fs), insertedBuf.String()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// SplitLines splits file content into lines for use with EnsureAnchors /
// AssignAnchors. It always strips a single trailing newline before splitting,
// so that "foo\nbar\n" and "foo\nbar" both produce ["foo","bar"].
// This keeps the in-memory line slice consistent with what JoinLines writes
// back to disk, preventing spurious slicesEqual mismatches that would
// otherwise cause the anchor map to be reconciled and drop the last anchor.
func SplitLines(content string) []string {
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// JoinLines is the inverse of SplitLines: it joins lines with "\n" and
// appends a trailing newline, matching the POSIX convention for text files.
// Always use this (not strings.Join) when writing content back to disk.
func JoinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

// clipLine shortens a line for use in an error message.
func clipLine(s string) string {
	const max = 120
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// buildAnnotated serialises a FileState into the annotated string the LLM reads.
// Each line is formatted as "token§content", e.g. "k7fm4§package main" (token =
// id + check characters, see anchorToken).
// Must be called with m.mu held (or on a privately owned fs).
func buildAnnotated(fs *FileState) string {
	var sb strings.Builder
	for i, line := range fs.lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(anchorToken(fs.lineToAnchor[i]))
		sb.WriteString(anchorDelimiter)
		sb.WriteString(line)
	}
	return sb.String()
}
