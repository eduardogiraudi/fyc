package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"fuckyourcli/store"
	"fuckyourcli/tools"
)

// The undo journal records the workspace before and after every turn, so /undo
// can put the files back exactly as they were before the turn it rewinds to.
//
// It is a workspace snapshot with an incremental manifest, not a hook per tool:
// every tool that can touch the disk (write, replace_anchors, delete, rename,
// git, and sh — a shell command is as much a model change as a patch) is covered
// by diffing the workspace against the last known state. The bytes needed to
// restore a file are stored content-addressed the first time they are seen, so
// identical content across turns costs nothing.
//
// The journal covers every root the session allowed (/cd): changes are keyed by
// root and relative path, and a record whose root is no longer allowed is never
// restored (it could rewrite a folder the user has just revoked).
const (
	undoJournalSchema = 2

	// Limits keep a snapshot from silently copying a huge tree. A turn that
	// exceeds them is reported as partial rather than pretended reverted.
	undoMaxFileBytes  = 8 << 20
	undoMaxTurnBytes  = 64 << 20
	undoMaxTurnFiles  = 2000
	undoMaxWalkFiles  = 20000
	undoMaxTurnsKept  = 200
	undoJournalFolder = "undo"
)

// fileVersion is the recoverable identity of one file: whether it existed, its
// content hash and the mode it had.
type fileVersion struct {
	Exists bool   `json:"exists,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`

	// size/mtime are the stat fingerprint that lets Sync and Collect skip the
	// files that did not change. They are memory only.
	size  int64
	mtime int64
	// unrecoverable marks a file whose bytes are not in the store (above the size
	// limit, unreadable): a turn that changes it can only report it.
	unrecoverable bool
}

// fileChange is one path a turn touched.
type fileChange struct {
	// Root is the allowed root the path belongs to. It is empty for the records
	// written before /cd existed (schema 1), which all belong to the journal's
	// primary root.
	Root string `json:"root,omitempty"`
	// Path is relative to Root, slash-separated.
	Path   string      `json:"path"`
	Before fileVersion `json:"before"`
	After  fileVersion `json:"after"`
	// Uncaptured is set when the limits prevented storing the content: the turn
	// changed the file but it cannot be restored.
	Uncaptured bool `json:"uncaptured,omitempty"`
}

// rootOr is the root the change belongs to.
func (c fileChange) rootOr(primary string) string {
	if c.Root == "" {
		return primary
	}
	return c.Root
}

// displayPath is what the user (and the conflict prompt) sees: the relative
// name inside the primary root, the root-qualified one everywhere else.
func (c fileChange) displayPath(primary string) string {
	root := c.rootOr(primary)
	if root == "" || root == primary {
		return c.Path
	}
	return filepath.Join(root, filepath.FromSlash(c.Path))
}

// undoJournal is the per-session record. Turns are indexed like the history's
// turns; a missing record means the turn changed nothing outside the workspace.
type undoJournal struct {
	mu sync.Mutex
	// root is the primary workspace root. It identifies the journal together
	// with the session id.
	root string
	// roots are all the roots the journal covers, primary first. They are kept
	// in sync with the session's allowed roots (see SetRoots).
	roots []string
	// ig is the ignore manager of every covered root. When it is nil the manager
	// handed to Collect/Sync is used.
	ig      *tools.IgnoreManager
	session string
	dir     string
	// base is the history index of turns[0]: the cap drops the oldest records,
	// and compaction renumbers the surviving turns from zero.
	base     int
	manifest map[string]fileVersion
	turns    [][]fileChange
	// budget tracks the bytes stored for the turn in progress.
	budget  int
	partial bool
}

// changeKey identifies one path across every allowed root.
func changeKey(root, rel string) string { return root + "\x00" + rel }

// undoJournalFile is the on-disk envelope.
type undoJournalFile struct {
	Schema int            `json:"schema"`
	Base   int            `json:"base,omitempty"`
	Turns  [][]fileChange `json:"turns"`
}

func undoJournalDir(workspaceDir, sessionID string) string {
	return filepath.Join(workspaceDir, undoJournalFolder, sessionID)
}

// newUndoJournal prepares (or loads) the journal of a session.
func newUndoJournal(root, session, dir string) *undoJournal {
	j := &undoJournal{root: root, roots: []string{root}, session: session, dir: dir, manifest: map[string]fileVersion{}}
	if err := j.load(); err != nil {
		store.Notice("undo journal: %v", err)
	}
	return j
}

// SetRoots installs the workspace the journal must cover: every additional root
// allowed with /cd, and the ignore rules that police them.
func (j *undoJournal) SetRoots(roots []string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.roots = append([]string(nil), roots...)
	if len(j.roots) == 0 && j.root != "" {
		j.roots = []string{j.root}
	}
	j.ig = ig
}

// walkRoots is every root the journal covers.
func (j *undoJournal) walkRoots() []string {
	if len(j.roots) > 0 {
		return j.roots
	}
	if j.root != "" {
		return []string{j.root}
	}
	return nil
}

// ignoreManager prefers the manager installed with the root set (it knows every
// root) and falls back to the one the caller passed.
func (j *undoJournal) ignoreManager(ig *tools.IgnoreManager) *tools.IgnoreManager {
	if j.ig != nil {
		return j.ig
	}
	return ig
}

// coversRoot reports whether root is still an allowed root of this journal.
func (j *undoJournal) coversRoot(root string) bool {
	want, err := store.ResolvePath(root)
	if err != nil {
		return false
	}
	for _, r := range j.walkRoots() {
		if got, rerr := store.ResolvePath(r); rerr == nil && got == want {
			return true
		}
	}
	return false
}

// load restores the turns of an earlier process, so /undo still reverts files
// after a restart.
func (j *undoJournal) load() error {
	data, err := os.ReadFile(filepath.Join(j.dir, "journal.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc undoJournalFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %v", j.dir, err)
	}
	j.turns = doc.Turns
	j.base = doc.Base
	if doc.Schema < 2 {
		// Schema 1 knew a single root: every record belongs to the primary one.
		for _, turn := range j.turns {
			for i := range turn {
				if turn[i].Root == "" {
					turn[i].Root = j.root
				}
			}
		}
	}
	return nil
}

// Save writes the journal next to the session file.
func (j *undoJournal) Save() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := store.EnsureDir(j.dir, store.DirPerm); err != nil {
		return err
	}
	return store.WriteJSON(filepath.Join(j.dir, "journal.json"),
		undoJournalFile{Schema: undoJournalSchema, Base: j.base, Turns: j.turns}, store.FilePerm, true)
}

// BeginTurn opens the record of turn n.
func (j *undoJournal) BeginTurn(n int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.grow(n)
	j.budget = 0
}

func (j *undoJournal) grow(n int) {
	for len(j.turns)+j.base <= n {
		j.turns = append(j.turns, nil)
	}
	// The oldest records are dropped beyond the cap: a long session must not keep
	// every turn's blobs on disk forever.
	for len(j.turns) > undoMaxTurnsKept {
		j.turns = append([][]fileChange(nil), j.turns[1:]...)
		j.base++
	}
	j.gcLocked()
}

// turnAt is the record of turn n, or nil when the journal does not cover it.
func (j *undoJournal) turnAt(n int) []fileChange {
	i := n - j.base
	if i < 0 || i >= len(j.turns) {
		return nil
	}
	return j.turns[i]
}

// currentTurn is the index of the turn being recorded.
func (j *undoJournal) currentTurn() int { return j.base + len(j.turns) - 1 }

// Sync brings the manifest up to date with the disk and stores the bytes of the
// files that changed since the last sync. It runs at the start of a turn, so the
// pre-image of everything a turn is about to change is already in the store.
// root is the caller's workspace (a sub-agent's subdirectory): the journal always
// covers every allowed root, which is a superset of it.
func (j *undoJournal) Sync(_ string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	im := j.ignoreManager(ig)
	seen := map[string]bool{}
	for _, root := range j.walkRoots() {
		j.walkRoot(root, im, func(root, rel, abs string, info fs.FileInfo) {
			key := changeKey(root, rel)
			seen[key] = true
			if v, ok := j.manifest[key]; ok && v.size == info.Size() && v.mtime == info.ModTime().UnixNano() {
				return
			}
			ver, err := j.capture(rel, abs, info)
			ver.unrecoverable = err != nil
			j.manifest[key] = ver
		})
	}
	// An entry leaves the manifest only when its file is really gone. A file the
	// walk did not visit keeps its pre-image: it can be missing from the walk
	// without being deleted (a new ignore rule written during the same turn, the
	// walk cap, an unreadable root), and dropping it would turn the next change
	// into a "creation" — which /undo then removes instead of restoring.
	for key := range j.manifest {
		if seen[key] || manifestFileExists(key) {
			continue
		}
		delete(j.manifest, key)
	}
}

// Collect attributes every change since the last Sync/Collect to the turn in
// progress and updates the manifest to the new state.
func (j *undoJournal) Collect(_ string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.turns) == 0 {
		return
	}
	turn := j.currentTurn()
	im := j.ignoreManager(ig)
	seen := map[string]bool{}
	for _, root := range j.walkRoots() {
		j.walkRoot(root, im, func(root, rel, abs string, info fs.FileInfo) {
			key := changeKey(root, rel)
			seen[key] = true
			prev, known := j.manifest[key]
			if known && prev.size == info.Size() && prev.mtime == info.ModTime().UnixNano() {
				return
			}
			after, err := j.capture(rel, abs, info)
			after.unrecoverable = err != nil
			before := prev
			if !known {
				before = fileVersion{}
			}
			uncaptured := err != nil || prev.unrecoverable
			j.record(turn, fileChange{Root: root, Path: rel, Before: before, After: after, Uncaptured: uncaptured})
			j.manifest[key] = after
		})
	}
	for key, prev := range j.manifest {
		if seen[key] {
			continue
		}
		// Only a path that is really gone describes a deletion. A file the walk
		// did not visit may still be there (a new ignore rule, the walk cap, an
		// unreadable root): recording it as deleted would make /undo write its
		// pre-turn bytes back over it, so its entry stays in the manifest.
		if manifestFileExists(key) {
			continue
		}
		delete(j.manifest, key)
		root, rel := splitChangeKey(key)
		change := fileChange{
			Root:       root,
			Path:       rel,
			Before:     prev,
			After:      fileVersion{},
			Uncaptured: prev.unrecoverable,
		}
		j.record(turn, change)
	}
}

// manifestFileExists reports whether the file a manifest key names is still on
// disk. It is what separates "the turn deleted it" from "the walk did not visit
// it": only the first one describes a deletion.
func manifestFileExists(key string) bool {
	root, rel := splitChangeKey(key)
	if root == "" || rel == "" {
		return false
	}
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

// splitChangeKey separates a manifest key back into root and relative path.
func splitChangeKey(key string) (root, rel string) {
	if i := strings.IndexByte(key, 0); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// record stores one change of the turn, skipping a path already recorded (the
// pre-image of the first change is the one a rewind needs).
func (j *undoJournal) record(turn int, change fileChange) {
	i := turn - j.base
	if i < 0 || i >= len(j.turns) {
		return
	}
	for k := range j.turns[i] {
		existing := j.turns[i][k]
		if existing.Path == change.Path && existing.rootOr(j.root) == change.rootOr(j.root) {
			// Later changes of the same path only move the expected end state.
			j.turns[i][k].After = change.After
			return
		}
	}
	j.turns[i] = append(j.turns[i], change)
}

// walk visits every regular file of one root the journal covers: symlinks,
// ignored paths, the .git directory and files above the per-file limit are left
// out (an ignored artifact is not a model edit worth reverting).
func (j *undoJournal) walkRoot(root string, ig *tools.IgnoreManager, visit func(root, rel, abs string, info fs.FileInfo)) {
	// WalkDir does not follow the root itself, so a workspace opened through a
	// symlink would hand back nothing but the link: every turn would look like it
	// changed no files at all.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	visited := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			// Only the repository metadata itself is out of scope: .github, .gitignore
			// and the rest of the dot files are ordinary workspace content.
			if path != root && name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || name == ".git" {
			return nil
		}
		visited++
		if visited > undoMaxWalkFiles {
			j.partial = true
			return filepath.SkipAll
		}
		if ig != nil && ig.IsIgnored(path, root) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !relInside(rel) {
			return nil
		}
		visit(root, filepath.ToSlash(rel), path, info)
		return nil
	})
	if err != nil {
		store.Notice("undo journal: %v", err)
	}
}

// capture records the version of one file and stores its bytes. It returns an
// error when the file is above the per-file limit (nothing is stored).
func (j *undoJournal) capture(rel, abs string, info fs.FileInfo) (fileVersion, error) {
	ver := fileVersion{Exists: true, Mode: uint32(info.Mode().Perm()), size: info.Size(), mtime: info.ModTime().UnixNano()}
	if info.Size() > undoMaxFileBytes {
		j.partial = true
		return ver, fmt.Errorf("%s: %d bytes is above the snapshot limit", rel, info.Size())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		j.partial = true
		return ver, err
	}
	sum := sha256.Sum256(data)
	ver.Hash = hex.EncodeToString(sum[:])
	if err := j.store(ver.Hash, data); err != nil {
		j.partial = true
		return ver, err
	}
	return ver, nil
}

// store writes content into the content-addressed blob directory.
func (j *undoJournal) store(hash string, data []byte) error {
	path := filepath.Join(j.dir, "blobs", hash)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if j.budget+len(data) > undoMaxTurnBytes {
		j.partial = true
		return fmt.Errorf("snapshot budget of %d MiB reached", undoMaxTurnBytes>>20)
	}
	if err := store.EnsureDir(filepath.Dir(path), store.DirPerm); err != nil {
		return err
	}
	if err := store.WriteFile(path, data, store.FilePerm); err != nil {
		return err
	}
	j.budget += len(data)
	return nil
}

// rewindDecision is what the user wants for a file that changed after the turn.
type rewindDecision int

const (
	rewindRestore rewindDecision = iota
	rewindKeep
)

// undoFileReport is the outcome of reverting the files of a turn.
type undoFileReport struct {
	Restored []string
	Kept     []string
	Failed   []string
	Skipped  []string // changed by the turn but not captured (limits)
}

func (r *undoFileReport) merge(other undoFileReport) {
	r.Restored = append(r.Restored, other.Restored...)
	r.Kept = append(r.Kept, other.Kept...)
	r.Failed = append(r.Failed, other.Failed...)
	r.Skipped = append(r.Skipped, other.Skipped...)
}

// revertTurn puts back the files turn n changed. decide is asked for every file
// that changed after the turn (an external edit): restoring it would discard the
// user's own work, so nothing is decided silently.
func (j *undoJournal) revertTurn(n int, decide func(string) rewindDecision) undoFileReport {
	var report undoFileReport
	if j == nil {
		return report
	}
	// The record is copied out first: deciding what to do with a file the user
	// edited may open a prompt, and no lock may be held across one.
	j.mu.Lock()
	changes := append([]fileChange(nil), j.turnAt(n)...)
	primary := j.root
	j.mu.Unlock()

	for _, change := range changes {
		if change.Uncaptured {
			report.Skipped = append(report.Skipped, change.displayPath(primary))
			continue
		}
		if sameContentVersions(change.Before, change.After) {
			continue
		}
		root := change.rootOr(primary)
		abs := filepath.Join(root, filepath.FromSlash(change.Path))
		if decide != nil && !matchesVersion(abs, change.After) {
			if decide(change.displayPath(primary)) == rewindKeep {
				report.Kept = append(report.Kept, change.displayPath(primary))
				continue
			}
		}
		if err := j.restore(change); err != nil {
			report.Failed = append(report.Failed, change.displayPath(primary))
			continue
		}
		report.Restored = append(report.Restored, change.displayPath(primary))
	}
	return report
}

// restore writes the before version of a change back to the disk.
func (j *undoJournal) restore(change fileChange) error {
	root := change.rootOr(j.root)
	// A record whose root is no longer allowed is never written: the user just
	// revoked it, and restoring into it would reach a folder the session cannot
	// even read.
	if !j.coversRoot(root) {
		return fmt.Errorf("%s is not an allowed root any more", root)
	}
	abs := filepath.Join(root, filepath.FromSlash(change.Path))
	if !insideRoot(root, abs) {
		return fmt.Errorf("%s is outside its workspace root", change.Path)
	}
	if !change.Before.Exists {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := os.ReadFile(filepath.Join(j.dir, "blobs", change.Before.Hash))
	if err != nil {
		return err
	}
	if err := store.EnsureDir(filepath.Dir(abs), store.DirPerm); err != nil {
		return err
	}
	mode := os.FileMode(change.Before.Mode)
	if mode == 0 {
		mode = store.FilePerm
	}
	return os.WriteFile(abs, data, mode)
}

// insideRoot reports whether abs stays inside root, symlinks included. Both
// sides are resolved before they are compared: a workspace reached through a
// symlink (macOS $TMPDIR lives under /var → /private/var, and /tmp the same way)
// would otherwise look like another tree and every restore would be refused.
// A path that does not exist yet is matched by its deepest existing ancestor, so
// a restore can recreate it.
func insideRoot(root, abs string) bool {
	if !withinTree(root, abs) {
		return false
	}
	resolvedRoot, err := store.ResolvePath(root)
	if err != nil {
		return false
	}
	resolvedAbs, err := store.ResolvePath(abs)
	if err != nil {
		return false
	}
	return withinTree(resolvedRoot, resolvedAbs)
}

// withinTree reports whether p is lexically inside root.
func withinTree(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && relInside(rel)
}

// relInside reports whether a relative path stays below its base. A name that
// merely starts with ".." (..notes.txt) is a normal file, not an escape.
func relInside(rel string) bool {
	return !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// matchesVersion reports whether the file on disk still is what the turn left.
func matchesVersion(abs string, want fileVersion) bool {
	info, err := os.Lstat(abs)
	if !want.Exists {
		return os.IsNotExist(err)
	}
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == want.Hash
}

func sameContentVersions(a, b fileVersion) bool {
	if a.Exists != b.Exists {
		return false
	}
	if !a.Exists {
		return true
	}
	return a.Hash == b.Hash && a.Mode == b.Mode
}

// truncate drops the records of the turns that no longer exist.
func (j *undoJournal) truncate(turns int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if turns < 0 {
		turns = 0
	}
	keep := turns - j.base
	if keep < 0 {
		keep = 0
	}
	if keep < len(j.turns) {
		j.turns = append([][]fileChange(nil), j.turns[:keep]...)
	}
	if len(j.turns) == 0 {
		j.base = turns
	}
	j.gcLocked()
}

// keepLastTurns re-aligns the records after compaction summarized the older
// turns away: the retained turns are the newest ones.
func (j *undoJournal) keepLastTurns(turns int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if turns <= 0 {
		j.turns, j.base = nil, 0
		j.gcLocked()
		return
	}
	if turns >= len(j.turns) {
		// The surviving turns are renumbered from zero, so the records start there
		// too; only the tail of the journal can still describe them.
		j.base = 0
		return
	}
	j.turns = append([][]fileChange(nil), j.turns[len(j.turns)-turns:]...)
	j.base = 0
	j.gcLocked()
}

// Discard forgets everything: the conversation it described is gone.
func (j *undoJournal) Discard() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.turns = nil
	dir := j.dir
	j.mu.Unlock()
	_ = os.RemoveAll(dir)
}

// conflicts lists the paths touched by the turns from `from` onwards that no
// longer hold what the turn left: the user edited them afterwards, and restoring
// the pre-turn bytes would throw that work away.
func (j *undoJournal) conflicts(from int) []string {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for turn := from; turn < j.base+len(j.turns); turn++ {
		for _, change := range j.turnAt(turn) {
			display := change.displayPath(j.root)
			if change.Uncaptured || seen[display] || sameContentVersions(change.Before, change.After) {
				continue
			}
			seen[display] = true
			root := change.rootOr(j.root)
			abs := filepath.Join(root, filepath.FromSlash(change.Path))
			if !matchesVersion(abs, change.After) {
				out = append(out, display)
			}
		}
	}
	sort.Strings(out)
	return out
}

// gcLocked deletes the pre-images no record refers to any more. The manifest is
// part of the reference set: its hashes are the before-images of the next turn.
func (j *undoJournal) gcLocked() {
	refs := map[string]bool{}
	for _, turn := range j.turns {
		for _, change := range turn {
			if change.Before.Hash != "" {
				refs[change.Before.Hash] = true
			}
			if change.After.Hash != "" {
				refs[change.After.Hash] = true
			}
		}
	}
	for _, ver := range j.manifest {
		if ver.Hash != "" {
			refs[ver.Hash] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(j.dir, "blobs"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || refs[entry.Name()] {
			continue
		}
		_ = os.Remove(filepath.Join(j.dir, "blobs", entry.Name()))
	}
}

// incomplete reports whether the snapshot could not cover the whole turn (too
// many files, too large content): the recap has to say so.
func (j *undoJournal) incomplete() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.partial
}

// turnChangedFiles is how many paths the turn touched, for the undo menu.
func (j *undoJournal) turnChangedFiles(n int) int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.turnAt(n))
}

// pruneUndoJournal removes the journal and the blobs of a session that is gone.
func pruneUndoJournal(workspaceDir, sessionID string) {
	if sessionID == "" {
		return
	}
	_ = os.RemoveAll(undoJournalDir(workspaceDir, sessionID))
}

// sortedPaths keeps the recap deterministic.
func sortedPaths(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
