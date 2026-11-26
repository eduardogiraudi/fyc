package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

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
	// The recorded root is the resolved one (walkRoot), so a caller's spelling
	// would make every file look like it lives in an extra root.
	if root == "" || store.SamePath(root, primary) {
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
	// blobMu guards budget and the blob writes: a capture pass reads files in
	// parallel (see captureAll), so the per-turn byte budget is shared.
	blobMu sync.Mutex
	// budget tracks the bytes stored for the turn in progress.
	budget int
	// partial is set when the snapshot could not cover the workspace (limits,
	// unreadable files). It is written by the capture workers, off the journal
	// lock, so it is atomic.
	partial atomic.Bool
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
	// walkRoot records files under the resolved root; keeping the caller's
	// spelling here would make every record look like it belongs elsewhere.
	if resolved, err := store.ResolvePath(root); err == nil {
		root = resolved
	}
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
	j.blobMu.Lock()
	j.budget = 0
	j.blobMu.Unlock()
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

// walkEntry is one regular file the walk saw: the stat fingerprint is all a pass
// needs until something captures its bytes.
type walkEntry struct {
	root, rel, abs string
	size, mtime    int64
	mode           uint32
}

// fingerprint is the version a stat pass records for a file whose bytes are not
// stored.
func (e walkEntry) fingerprint() fileVersion {
	return fileVersion{Exists: true, Mode: e.mode, size: e.size, mtime: e.mtime}
}

// synced reports whether the manifest entry still describes this file: only the
// fingerprint is compared, because nothing about the content changed.
func (v fileVersion) synced(e walkEntry) bool {
	return v.Exists && v.size == e.size && v.mtime == e.mtime
}

// recoverable reports whether the bytes of the version are in the store. A
// version that existed but was never captured cannot be written back: a rewind
// has to say so instead of restoring an empty file.
func (v fileVersion) recoverable() bool { return !v.Exists || v.Hash != "" }

// walkEntries walks every covered root outside the journal lock: it is the slow,
// I/O-bound part of a snapshot, and no rewind has to wait behind it.
func (j *undoJournal) walkEntries(ig *tools.IgnoreManager) ([]walkEntry, map[string]bool) {
	j.mu.Lock()
	roots := append([]string(nil), j.walkRoots()...)
	im := j.ignoreManager(ig)
	j.mu.Unlock()
	var entries []walkEntry
	seen := map[string]bool{}
	for _, root := range roots {
		j.walkRoot(root, im, func(root, rel, abs string, info fs.FileInfo) {
			seen[changeKey(root, rel)] = true
			entries = append(entries, walkEntry{root: root, rel: rel, abs: abs,
				size: info.Size(), mtime: info.ModTime().UnixNano(), mode: uint32(info.Mode().Perm())})
		})
	}
	return entries, seen
}

// captureWorkers bounds how many files one capture pass reads at once: the pass
// is I/O bound, but a whole-workspace capture must not open the tree in one go.
func captureWorkers() int { return min(max(runtime.NumCPU(), 4), 16) }

// captureAll stores the bytes of the given files with a bounded worker pool and
// returns their versions keyed like the manifest. The caller keeps its own order
// for recording, so what is written stays deterministic.
func (j *undoJournal) captureAll(entries []walkEntry) map[string]fileVersion {
	out := make(map[string]fileVersion, len(entries))
	if len(entries) == 0 {
		return out
	}
	type captured struct {
		key string
		ver fileVersion
	}
	results := make(chan captured, len(entries))
	index := make(chan walkEntry)
	workers := min(captureWorkers(), len(entries))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range index {
				results <- captured{key: changeKey(e.root, e.rel), ver: j.capture(e)}
			}
		}()
	}
	go func() {
		for _, e := range entries {
			index <- e
		}
		close(index)
		wg.Wait()
		close(results)
	}()
	for c := range results {
		out[c.key] = c.ver
	}
	return out
}

// Sync records the fingerprint of every file of the workspace and stores not a
// single byte. A pre-image is captured when something is about to write the file
// (Prepare) or before a tool that cannot name its targets (PrepareTree), so
// starting a turn no longer copies the whole workspace.
func (j *undoJournal) Sync(_ string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	entries, seen := j.walkEntries(ig)
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range entries {
		key := changeKey(e.root, e.rel)
		if prev, ok := j.manifest[key]; ok && prev.synced(e) {
			continue
		}
		// The stored hash goes with the old fingerprint: it describes bytes this
		// file no longer has.
		j.manifest[key] = e.fingerprint()
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

// Prepare stores the pre-images of the files a call declares it is about to
// write, before it runs. It is the copy-on-write half of the journal: a turn
// copies what a tool actually names, not the workspace.
func (j *undoJournal) Prepare(paths []string, ig *tools.IgnoreManager) {
	if j == nil || len(paths) == 0 {
		return
	}
	j.mu.Lock()
	roots := append([]string(nil), j.walkRoots()...)
	im := j.ignoreManager(ig)
	j.mu.Unlock()
	var todo []walkEntry
	for _, path := range paths {
		e, ok := entryForPath(path, roots, im)
		if !ok {
			continue
		}
		j.mu.Lock()
		prev, known := j.manifest[changeKey(e.root, e.rel)]
		j.mu.Unlock()
		// The bytes of a file that did not change since it was captured are
		// already in the store.
		if known && prev.synced(e) && prev.recoverable() {
			continue
		}
		todo = append(todo, e)
	}
	versions := j.captureAll(todo)
	if len(versions) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for key, ver := range versions {
		j.manifest[key] = ver
	}
}

// PrepareTree stores the pre-image of the whole workspace, for a tool that cannot
// declare what it touches: sh, a git rewrite, an MCP server. It is the only pass
// that still reads every file, and it is paid once per file: an entry that
// already holds its bytes is skipped.
func (j *undoJournal) PrepareTree(_ string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	entries, _ := j.walkEntries(ig)
	j.mu.Lock()
	var todo []walkEntry
	for _, e := range entries {
		if prev, ok := j.manifest[changeKey(e.root, e.rel)]; ok && prev.synced(e) && prev.recoverable() {
			continue
		}
		todo = append(todo, e)
	}
	j.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	versions := j.captureAll(todo)
	j.mu.Lock()
	defer j.mu.Unlock()
	for key, ver := range versions {
		j.manifest[key] = ver
	}
}

// entryForPath maps one path to the walk entry of the regular file it names. It
// reports false for a file that does not exist yet (there is nothing to
// preserve), a directory, a path outside every root and an ignored artifact.
func entryForPath(path string, roots []string, ig *tools.IgnoreManager) (walkEntry, bool) {
	if strings.TrimSpace(path) == "" {
		return walkEntry{}, false
	}
	abs, err := store.ResolvePath(path)
	if err != nil {
		return walkEntry{}, false
	}
	for _, root := range roots {
		// The walk records files under the resolved root: the key has to match.
		resolved := root
		if r, err := filepath.EvalSymlinks(root); err == nil {
			resolved = r
		}
		if !insideRoot(resolved, abs) {
			continue
		}
		rel, err := filepath.Rel(resolved, abs)
		if err != nil || !relInside(rel) {
			return walkEntry{}, false
		}
		if ig != nil && ig.IsIgnored(abs, resolved) {
			return walkEntry{}, false
		}
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return walkEntry{}, false
		}
		return walkEntry{root: resolved, rel: filepath.ToSlash(rel), abs: abs,
			size: info.Size(), mtime: info.ModTime().UnixNano(), mode: uint32(info.Mode().Perm())}, true
	}
	return walkEntry{}, false
}

// Collect attributes every change since the last Sync/Collect to the turn in
// progress and updates the manifest to the new state. The bytes of the changed
// files are read off the journal lock, in parallel.
func (j *undoJournal) Collect(_ string, ig *tools.IgnoreManager) {
	if j == nil {
		return
	}
	j.mu.Lock()
	open := len(j.turns) > 0
	j.mu.Unlock()
	if !open {
		return
	}
	entries, seen := j.walkEntries(ig)
	j.mu.Lock()
	var changed []walkEntry
	for _, e := range entries {
		if prev, ok := j.manifest[changeKey(e.root, e.rel)]; ok && prev.synced(e) {
			continue
		}
		changed = append(changed, e)
	}
	j.mu.Unlock()
	versions := j.captureAll(changed)

	j.mu.Lock()
	defer j.mu.Unlock()
	turn := j.currentTurn()
	for _, e := range changed {
		key := changeKey(e.root, e.rel)
		after, captured := versions[key]
		if !captured {
			continue
		}
		before := fileVersion{}
		if prev, known := j.manifest[key]; known {
			before = prev
		}
		// A version whose bytes are not in the store cannot be restored: record
		// it as skipped rather than pretend the rewind will work.
		uncaptured := after.unrecoverable || !before.recoverable()
		j.record(turn, fileChange{Root: e.root, Path: e.rel, Before: before, After: after, Uncaptured: uncaptured})
		j.manifest[key] = after
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
			Uncaptured: prev.unrecoverable || !prev.recoverable(),
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
			j.partial.Store(true)
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

// capture reads one file and stores its bytes, returning the version to record.
// A file whose bytes cannot be kept (above the per-file limit, unreadable, over
// the turn budget) comes back marked unrecoverable, so a rewind reports it
// instead of writing an empty file. It takes no journal lock: capture workers
// run concurrently (see captureAll).
func (j *undoJournal) capture(e walkEntry) fileVersion {
	ver := e.fingerprint()
	ver.unrecoverable = true
	if e.size > undoMaxFileBytes {
		j.partial.Store(true)
		return ver
	}
	data, err := os.ReadFile(e.abs)
	if err != nil {
		j.partial.Store(true)
		return ver
	}
	sum := sha256.Sum256(data)
	ver.Hash = hex.EncodeToString(sum[:])
	if err := j.store(ver.Hash, data); err != nil {
		j.partial.Store(true)
		return ver
	}
	ver.unrecoverable = false
	return ver
}

// store writes content into the content-addressed blob directory.
func (j *undoJournal) store(hash string, data []byte) error {
	path := filepath.Join(j.dir, "blobs", hash)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	// The budget belongs to the turn, and the workers share it.
	j.blobMu.Lock()
	defer j.blobMu.Unlock()
	if j.budget+len(data) > undoMaxTurnBytes {
		j.partial.Store(true)
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
	return j.partial.Load()
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
