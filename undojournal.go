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
const (
	undoJournalSchema = 1

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
	Path   string      `json:"path"`
	Before fileVersion `json:"before"`
	After  fileVersion `json:"after"`
	// Uncaptured is set when the limits prevented storing the content: the turn
	// changed the file but it cannot be restored.
	Uncaptured bool `json:"uncaptured,omitempty"`
}

// undoJournal is the per-session record. Turns are indexed like the history's
// turns; a missing record means the turn changed nothing outside the workspace.
type undoJournal struct {
	mu      sync.Mutex
	root    string
	session string
	dir     string
	// base is the history index of turns[0]: the cap drops the oldest records,
	// and compaction renumbers the surviving turns from zero.
	base     int
	manifest map[string]fileVersion
	turns    [][]fileChange
	allow    map[string]bool // paths a turn may restore, empty = everything under root
	// budget tracks the bytes stored for the turn in progress.
	budget  int
	partial bool
}

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
	j := &undoJournal{root: root, session: session, dir: dir, manifest: map[string]fileVersion{}}
	if err := j.load(); err != nil {
		store.Notice("undo journal: %v", err)
	}
	return j
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
// root is the caller's workspace (a sub-agent's subdirectory): the journal keeps
// covering the whole session root, which is a superset of it.
func (j *undoJournal) Sync(root string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.walk(ig, func(rel string, abs string, info fs.FileInfo) {
		if v, ok := j.manifest[rel]; ok && v.size == info.Size() && v.mtime == info.ModTime().UnixNano() {
			return
		}
		ver, err := j.capture(rel, abs, info)
		ver.unrecoverable = err != nil
		j.manifest[rel] = ver
	})
	// Files that disappeared are dropped from the manifest: the next Collect does
	// not see them either, so nothing is claimed about them.
	for rel := range j.manifest {
		if _, err := os.Lstat(filepath.Join(j.root, filepath.FromSlash(rel))); err != nil {
			delete(j.manifest, rel)
		}
	}
}

// Collect attributes every change since the last Sync/Collect to the turn in
// progress and updates the manifest to the new state.
func (j *undoJournal) Collect(root string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.turns) == 0 {
		return
	}
	turn := j.currentTurn()
	seen := map[string]bool{}
	j.walk(ig, func(rel string, abs string, info fs.FileInfo) {
		seen[rel] = true
		prev, known := j.manifest[rel]
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
		j.record(turn, fileChange{Path: rel, Before: before, After: after, Uncaptured: uncaptured})
		j.manifest[rel] = after
	})
	for rel, prev := range j.manifest {
		if seen[rel] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(j.root, filepath.FromSlash(rel))); err == nil {
			continue
		}
		delete(j.manifest, rel)
		j.record(turn, fileChange{Path: rel, Before: prev, After: fileVersion{}, Uncaptured: prev.unrecoverable})
	}
}

// record stores one change of the turn, skipping a path already recorded (the
// pre-image of the first change is the one a rewind needs).
func (j *undoJournal) record(turn int, change fileChange) {
	i := turn - j.base
	if i < 0 || i >= len(j.turns) {
		return
	}
	for k := range j.turns[i] {
		if j.turns[i][k].Path == change.Path {
			// Later changes of the same path only move the expected end state.
			j.turns[i][k].After = change.After
			return
		}
	}
	j.turns[i] = append(j.turns[i], change)
}

// walk visits every regular file of the workspace the journal covers: symlinks,
// ignored paths, the .git directory and files above the per-file limit are left
// out (an ignored artifact is not a model edit worth reverting).
func (j *undoJournal) walk(ig *tools.IgnoreManager, visit func(rel, abs string, info fs.FileInfo)) {
	visited := 0
	err := filepath.WalkDir(j.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			// Only the repository metadata itself is out of scope: .github, .gitignore
			// and the rest of the dot files are ordinary workspace content.
			if path != j.root && name == ".git" {
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
		if ig != nil && ig.IsIgnored(path, j.root) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(j.root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		visit(filepath.ToSlash(rel), path, info)
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
	j.mu.Unlock()

	for _, change := range changes {
		if change.Uncaptured {
			report.Skipped = append(report.Skipped, change.Path)
			continue
		}
		if sameContentVersions(change.Before, change.After) {
			continue
		}
		abs := filepath.Join(j.root, filepath.FromSlash(change.Path))
		if decide != nil && !matchesVersion(abs, change.After) {
			if decide(change.Path) == rewindKeep {
				report.Kept = append(report.Kept, change.Path)
				continue
			}
		}
		if err := j.restore(change); err != nil {
			report.Failed = append(report.Failed, change.Path)
			continue
		}
		report.Restored = append(report.Restored, change.Path)
	}
	return report
}

// restore writes the before version of a change back to the disk.
func (j *undoJournal) restore(change fileChange) error {
	abs := filepath.Join(j.root, filepath.FromSlash(change.Path))
	if !insideRoot(j.root, abs) {
		return fmt.Errorf("%s is outside the workspace", change.Path)
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

// insideRoot reports whether abs stays inside root, symlinks included.
func insideRoot(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return false
	}
	relParent, err := filepath.Rel(root, parent)
	return err == nil && !strings.HasPrefix(relParent, "..")
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
			if change.Uncaptured || seen[change.Path] || sameContentVersions(change.Before, change.After) {
				continue
			}
			seen[change.Path] = true
			abs := filepath.Join(j.root, filepath.FromSlash(change.Path))
			if !matchesVersion(abs, change.After) {
				out = append(out, change.Path)
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
