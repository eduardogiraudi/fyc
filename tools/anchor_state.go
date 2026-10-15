package tools

import (
	"fmt"
	"hash/crc32"
	"strings"
	"sync"
)

// anchorDelimiter separates the anchor word from the line content.
// § is chosen because it never appears in source code.
const anchorDelimiter = "§"

// anchorWords is the pool of single-token English words used as line anchors.
// All words are capitalized and unlikely to appear in source code.
// Organized in groups for readability; duplicates deliberately avoided.
var anchorWords = []string{
	// Gemstones & minerals
	"Agate", "Amethyst", "Beryl", "Citrine", "Diamond",
	"Emerald", "Feldspar", "Garnet", "Halite", "Iolite",
	"Jasper", "Kyanite", "Malachite", "Nephrite", "Obsidian",
	"Olivine", "Onyx", "Opal", "Pearl", "Pyrite",
	"Quartz", "Rhodonite", "Sapphire", "Selenite", "Spinel",
	"Topaz", "Tourmaline", "Turquoise", "Zircon", "Chalcedony",

	// Colors & pigments
	"Auburn", "Azure", "Bisque", "Carmine", "Cerise",
	"Celadon", "Cerulean", "Chartreuse", "Cobalt", "Crimson",
	"Ebony", "Fuchsia", "Gamboge", "Heliotrope", "Indigo",
	"Ivory", "Khaki", "Lavender", "Magenta", "Maroon",
	"Mauve", "Ochre", "Periwinkle", "Russet", "Saffron",
	"Scarlet", "Sepia", "Sienna", "Taupe", "Teal",
	"Umber", "Vermeil", "Viridian", "Violet", "Vermilion",

	// Birds
	"Albatross", "Bittern", "Bunting", "Capercaillie", "Cormorant",
	"Crake", "Crane", "Curlew", "Dipper", "Dotterel",
	"Dunlin", "Eagle", "Egret", "Falcon", "Fieldfare",
	"Finch", "Gannet", "Godwit", "Goldfinch", "Grebe",
	"Grosbeak", "Guillemot", "Harrier", "Hawfinch", "Heron",
	"Hoopoe", "Ibis", "Jackdaw", "Kestrel", "Kingfisher",
	"Kittiwake", "Lapwing", "Linnet", "Mallard", "Martin",
	"Merlin", "Moorhen", "Nightjar", "Nuthatch", "Osprey",
	"Partridge", "Pintail", "Pipit", "Plover", "Pochard",
	"Puffin", "Raven", "Redshank", "Redstart", "Robin",
	"Rook", "Sanderling", "Sandpiper", "Siskin", "Snipe",
	"Sparrow", "Starling", "Stonechat", "Stork", "Swallow",
	"Swift", "Tanager", "Thrush", "Wagtail", "Warbler",
	"Wheatear", "Whimbrel", "Whitethroat", "Wigeon", "Woodcock",
	"Woodlark", "Wren", "Wryneck", "Treecreeper", "Turnstone",

	// Trees & plants
	"Acacia", "Alder", "Almond", "Apricot", "Arbutus",
	"Aspen", "Bamboo", "Birch", "Blackthorn", "Buckthorn",
	"Cedar", "Cherry", "Chestnut", "Cypress", "Elder",
	"Elm", "Eucalyptus", "Fir", "Gorse", "Hawthorn",
	"Hazel", "Holly", "Hornbeam", "Juniper", "Larch",
	"Laurel", "Linden", "Magnolia", "Maple", "Mulberry",
	"Myrtle", "Osier", "Rowan", "Sequoia", "Sloe",
	"Sorrel", "Spruce", "Sycamore", "Tamarisk", "Walnut",
	"Willow", "Wisteria", "Yew", "Hemlock", "Balsam",

	// Celestial & weather
	"Aurora", "Borealis", "Comet", "Corona", "Cosmos",
	"Eclipse", "Equinox", "Flare", "Frost", "Galaxy",
	"Glimmer", "Haze", "Mist", "Nebula", "Nova",
	"Penumbra", "Prism", "Pulsar", "Quasar", "Radiance",
	"Ripple", "Solstice", "Tempest", "Twilight", "Umbra",
	"Vortex", "Zenith", "Zephyr", "Nimbus", "Perihelion",

	// Architecture & geography
	"Alcazar", "Arcade", "Basin", "Beacon", "Bluff",
	"Buttress", "Canyon", "Capstone", "Causeway", "Citadel",
	"Cloister", "Copse", "Croft", "Delta", "Dune",
	"Estuary", "Fjord", "Flint", "Fosse", "Gable",
	"Glen", "Gorge", "Granite", "Grotto", "Gully",
	"Harbor", "Hollow", "Inlet", "Isle", "Lagoon",
	"Lichen", "Lintel", "Loch", "Loess", "Marble",
	"Marsh", "Mesa", "Moraine", "Mound", "Niche",
	"Obelisk", "Parapet", "Pediment", "Pinnacle", "Portico",
	"Quay", "Ravine", "Reef", "Ridge", "Rubble",
	"Scarp", "Scree", "Slate", "Sluice", "Spire",
	"Steppe", "Terrace", "Tundra", "Turret", "Vale",
	"Viaduct", "Weald", "Wold", "Moat", "Promontory",
	"Tarn", "Torrent", "Tributary", "Spit", "Strand",
	"Summit", "Thicket", "Watershed", "Wetland", "Tableland",

	// Natural phenomena
	"Avalanche", "Bedrock", "Brackish", "Brine", "Canopy",
	"Cavern", "Crevasse", "Eddy", "Fissure", "Floodplain",
	"Fumarole", "Geode", "Geyser", "Glacier", "Gravel",
	"Headland", "Karst", "Loam", "Magma", "Mangrove",
	"Marl", "Massif", "Mudflat", "Outcrop", "Sediment",
	"Shingle", "Shoal", "Silt", "Littoral", "Fluvial",

	// Abstract & conceptual
	"Axiom", "Cadence", "Cipher", "Clarity", "Codex",
	"Crest", "Dossier", "Emblem", "Enigma", "Epoch",
	"Essence", "Ethos", "Facet", "Feather", "Ferment",
	"Filigree", "Fragment", "Fulcrum", "Glyph", "Harbinger",
	"Inertia", "Kernel", "Lattice", "Lexicon", "Liminal",
	"Locus", "Mantle", "Matrix", "Maxim", "Mentor",
	"Meridian", "Motif", "Mosaic", "Nadir", "Nexus",
	"Notation", "Nuance", "Omen", "Oracle", "Orbit",
	"Paradigm", "Paragon", "Patina", "Pivot", "Premise",
	"Praxis", "Precept", "Relic", "Remnant", "Residue",
	"Resonance", "Rubric", "Schism", "Shard", "Sigma",
	"Solace", "Specter", "Sphere", "Stigma", "Stratum",
	"Subtext", "Tableau", "Tenet", "Tenor", "Theorem",
	"Totem", "Traverse", "Trope", "Vantage", "Vector",
	"Vestige", "Vignette", "Virtue", "Warrant", "Wisdom",
	"Acumen", "Ardor", "Brevity", "Candor", "Decorum",
	"Dissent", "Duality", "Empathy", "Fervor", "Finesse",
	"Gallant", "Gravitas", "Hubris", "Humility", "Impetus",
	"Ingenuity", "Acuity", "Aplomb", "Candid", "Cognizant",
	"Concord", "Constancy", "Decency", "Dexterity", "Diligence",
	"Discern", "Elegance", "Equanimity", "Fortitude", "Frugal",
	"Generosity", "Gentleness", "Gratitude", "Integrity", "Kindness",
	"Lucidity", "Magnanimity", "Nobility", "Patience", "Prudence",
	"Rectitude", "Sagacity", "Serenity", "Sincerity", "Tenacity",
	"Tolerance", "Valiance", "Veracity", "Vigilance", "Zeal",

	// Mythology & legend
	"Alchemy", "Ambrosia", "Amphora", "Amulet", "Atheneum",
	"Augury", "Basilisk", "Caduceus", "Centaur", "Chimera",
	"Colossus", "Cyclops", "Daedalus", "Elysium", "Faun",
	"Gorgon", "Gryphon", "Harpy", "Hydra", "Labyrinth",
	"Leviathan", "Lyric", "Manticore", "Minotaur", "Nymph",
	"Pegasus", "Phoenix", "Satyr", "Selene", "Siren",
	"Sphinx", "Stygian", "Tartarus", "Triton", "Typhon",
	"Unicorn", "Valkyrie", "Wyvern", "Zephyrus", "Kraken",
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

// FileState holds the current anchor assignment for one file.
type FileState struct {
	lines        []string
	content      string // Exact disk snapshot, including the final newline.
	lineToAnchor map[int]string
	anchorToLine map[string]int
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
	opMu      sync.Mutex // Serializes anchored disk operations, never held during approval.
	mu        sync.Mutex
	files     map[string]*FileState
	poolIndex int // next index into anchorWords (or overflow counter)
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
	// Do not recycle words: an in-flight patch must not regain validity after a re-read.
}

// EnsureContent records the exact bytes read by get_anchors, not just split lines.
func (m *AnchorStateManager) EnsureContent(absPath, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAnchors(absPath, SplitLines(content))
	m.files[absPath].content = content
}

func (m *AnchorStateManager) validateContent(absPath, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs := m.files[absPath]
	if fs == nil {
		return fmt.Errorf("file not loaded — call get_anchors first")
	}
	if fs.content != content {
		delete(m.files, absPath)
		return fmt.Errorf("file changed on disk since the last anchored read/edit — no changes written; call get_anchors again and reconsider the patch using the current content")
	}
	return nil
}

// nextAnchor returns the next available anchor word.
// Must be called with m.mu held.
func (m *AnchorStateManager) nextAnchor() string {
	if m.poolIndex < len(anchorWords) {
		a := anchorWords[m.poolIndex]
		m.poolIndex++
		return a
	}
	// Pool exhausted: generate 2-word anchors deterministically.
	idx := m.poolIndex - len(anchorWords)
	a1 := anchorWords[idx%len(anchorWords)]
	a2 := anchorWords[(idx/len(anchorWords))%len(anchorWords)]
	m.poolIndex++
	return a1 + a2
}

//   - Not yet loaded → assign fresh anchors (same as AssignAnchors).
//   - Already loaded, same content → return existing anchors unchanged.
//     This is the key case: a second get_anchors call on an unmodified file
//     must not consume new anchor words from the pool.
//   - Already loaded, content differs (external edit) → reconcile with LCS
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
		// First time seeing this file — do a full fresh assignment.
		fs = &FileState{
			lines:        append([]string(nil), lines...),
			content:      JoinLines(lines),
			lineToAnchor: make(map[int]string, len(lines)),
			anchorToLine: make(map[string]int, len(lines)),
		}
		for i := range lines {
			a := m.nextAnchor()
			fs.lineToAnchor[i] = a
			fs.anchorToLine[a] = i
		}
		m.files[absPath] = fs
		return
	}

	// File already loaded. Check whether content is identical.
	if slicesEqual(fs.lines, lines) {
		// Nothing changed — reuse existing anchor map as-is.
		return
	}

	// Content differs (external edit while agent was running).
	// Reconcile with Myers diff so stable lines keep their anchor.
	ops := myersDiff(fs.lines, lines)
	newL2A := make(map[int]string, len(lines))
	newA2L := make(map[string]int, len(lines))
	oldI, newI := 0, 0
	for _, op := range ops {
		switch op.opType {
		case diffEqual:
			a := fs.lineToAnchor[oldI]
			newL2A[newI] = a
			newA2L[a] = newI
			oldI++
			newI++
		case diffDelete:
			oldI++
		case diffInsert:
			a := m.nextAnchor()
			newL2A[newI] = a
			newA2L[a] = newI
			newI++
		}
	}
	fs.lines = append([]string(nil), lines...)
	fs.content = JoinLines(lines)
	fs.lineToAnchor = newL2A
	fs.anchorToLine = newA2L
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

// PreviewEdit validates the anchors and computes what the file would look like
// after the edit, WITHOUT modifying any state. Returns (oldLines, newLines, err).
//
// startAnchor and endAnchor must be in the "Word:hash" format produced by
// get_anchors (e.g. "Agate:a3"). The hash is verified against the current
// content of the referenced line so stale references are caught early.
func (m *AnchorStateManager) PreviewEdit(absPath, startAnchor, endAnchor, replacement string) ([]string, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fs := m.files[absPath]
	if fs == nil {
		return nil, nil, fmt.Errorf("file not loaded — call get_anchors first")
	}

	startLine, err := m.resolveAnchor(fs, startAnchor)
	if err != nil {
		return nil, nil, fmt.Errorf("start_anchor: %w", err)
	}
	endLine, err := m.resolveAnchor(fs, endAnchor)
	if err != nil {
		return nil, nil, fmt.Errorf("end_anchor: %w", err)
	}
	if startLine > endLine {
		return nil, nil, fmt.Errorf("start_anchor (line %d) must come before end_anchor (line %d) in the file", startLine+1, endLine+1)
	}

	var replacementLines []string
	if replacement != "" {
		replacementLines = strings.Split(replacement, "\n")
	}

	newLines := make([]string, 0, len(fs.lines)-(endLine-startLine+1)+len(replacementLines))
	newLines = append(newLines, fs.lines[:startLine]...)
	newLines = append(newLines, replacementLines...)
	newLines = append(newLines, fs.lines[endLine+1:]...)

	return fs.Lines(), newLines, nil
}

// resolveAnchor parses a "Word:hash" token, looks up the word in the file's
// anchor map, and verifies the hash matches the current line content.
// Must be called with m.mu held.
func (m *AnchorStateManager) resolveAnchor(fs *FileState, anchor string) (int, error) {
	word, hash, ok := strings.Cut(anchor, ":")
	if !ok {
		// Accept bare word for convenience (no hash → skip content check).
		word = anchor
		lineIdx, exists := fs.anchorToLine[word]
		if !exists {
			return 0, fmt.Errorf("anchor %q not found — call get_anchors again", anchor)
		}
		return lineIdx, nil
	}

	lineIdx, exists := fs.anchorToLine[word]
	if !exists {
		return 0, fmt.Errorf("anchor %q not found — call get_anchors again", anchor)
	}

	// Tolerate the model passing the full annotated line (e.g. "Agate:a3§package main")
	// instead of just the Word:hash token ("Agate:a3"). Strip everything from § onward.
	hash, _, _ = strings.Cut(hash, anchorDelimiter)

	actual := lineHash(fs.lines[lineIdx])
	if actual != hash {
		return 0, fmt.Errorf("anchor %q content mismatch (expected hash %s, got %s) — call get_anchors again", anchor, hash, actual)
	}

	return lineIdx, nil
}

// CommitEdit applies newLines to the file state, reconciles anchors using LCS,
// and returns:
//   - fullAnnotated: the complete updated file (for internal use / re-read)
//   - insertedOnly:  only the lines that are new (diffInsert), annotated with
//     their fresh Word:hash anchors — this is what replace_anchors returns to the
//     LLM so it can make further edits without re-reading the whole file.
//
// Call this only after the edit has been written to disk.
func (m *AnchorStateManager) CommitEdit(absPath string, oldLines, newLines []string) (fullAnnotated, insertedOnly string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fs := m.files[absPath]
	if fs == nil {
		// Shouldn't happen if PreviewEdit succeeded, but be safe.
		fs = &FileState{
			lines:        append([]string(nil), newLines...),
			content:      JoinLines(newLines),
			lineToAnchor: make(map[int]string, len(newLines)),
			anchorToLine: make(map[string]int, len(newLines)),
		}
		for i := range newLines {
			a := m.nextAnchor()
			fs.lineToAnchor[i] = a
			fs.anchorToLine[a] = i
		}
		m.files[absPath] = fs
		full := buildAnnotated(fs)
		return full, full
	}

	// Reconcile: preserve anchors for equal lines, assign new ones for inserts.
	ops := myersDiff(oldLines, newLines)

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
			// anchor retired — not reused for this file in this session
			oldI++
		case diffInsert:
			a := m.nextAnchor()
			newL2A[newI] = a
			newA2L[a] = newI
			// Record this line for the insertedOnly response.
			if insertedBuf.Len() > 0 {
				insertedBuf.WriteByte('\n')
			}
			insertedBuf.WriteString(a)
			insertedBuf.WriteByte(':')
			insertedBuf.WriteString(lineHash(op.text))
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

// lineHash returns a 2-char hex content fingerprint for one line.
// Uses CRC32/IEEE of the whitespace-trimmed content, truncated to 1 byte.
// The hash is embedded in the anchor token (e.g. "Agate:a3") so the LLM
// can reference a specific line unambiguously without re-reading the file.
func lineHash(line string) string {
	h := crc32.ChecksumIEEE([]byte(strings.TrimSpace(line))) % 256
	return fmt.Sprintf("%02x", h)
}

// buildAnnotated serialises a FileState into the annotated string the LLM reads.
// Each line is formatted as "Word:hash§content", e.g. "Agate:a3§package main".
// Must be called with m.mu held (or on a privately owned fs).
func buildAnnotated(fs *FileState) string {
	var sb strings.Builder
	for i, line := range fs.lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(fs.lineToAnchor[i])
		sb.WriteByte(':')
		sb.WriteString(lineHash(line))
		sb.WriteString(anchorDelimiter)
		sb.WriteString(line)
	}
	return sb.String()
}

// stripAnchors removes the anchor prefix from each line of an annotated string
// and returns the plain content.
func stripAnchors(annotated string) string {
	lines := strings.Split(annotated, "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		if idx := strings.Index(l, anchorDelimiter); idx >= 0 {
			out[i] = l[idx+len(anchorDelimiter):]
		} else {
			out[i] = l
		}
	}
	return strings.Join(out, "\n")
}
