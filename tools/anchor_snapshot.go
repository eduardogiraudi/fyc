package tools

// The anchor cache is part of a turn's state: after /undo the model must find the
// anchors it had before the discarded turns, including the ones an earlier edit or
// a `sh` run had already invalidated — otherwise it would re-read (or worse, patch
// with anchors the file no longer has).
//
// Snapshotting is cheap because entries are copy-on-write: a snapshot only shares
// the map, and the next mutation of a shared entry replaces it with a private copy
// (see cowLocked). Restoring the whole map is then the exact state of the turn
// boundary.

// AnchorSnapshot is the anchor cache frozen at a turn boundary.
type AnchorSnapshot struct {
	files map[string]*FileState
}

// SnapshotAnchors freezes the current cache. Existing entries become shared, so
// the next mutation of any of them copies it first.
func SnapshotAnchors() *AnchorSnapshot {
	globalAnchorState.mu.Lock()
	defer globalAnchorState.mu.Unlock()
	snap := &AnchorSnapshot{files: make(map[string]*FileState, len(globalAnchorState.files))}
	for path, fs := range globalAnchorState.files {
		snap.files[path] = fs
	}
	globalAnchorState.epoch++
	return snap
}

// RestoreAnchors puts the cache back to a snapshot. drop names the files whose
// disk content could not be reverted (the user kept a newer version): their
// anchors are discarded so the model reads them again. A nil snapshot means "the
// boundary is unknown" and clears the cache, which is what a conversation that
// started in another process needs anyway.
func RestoreAnchors(snap *AnchorSnapshot, drop ...string) {
	m := globalAnchorState
	m.mu.Lock()
	defer m.mu.Unlock()
	files := make(map[string]*FileState, len(drop))
	if snap != nil {
		for path, fs := range snap.files {
			files[path] = fs
		}
	}
	for _, path := range drop {
		delete(files, path)
	}
	m.files = files
	m.epoch++
}

// cowLocked returns the entry of path ready to be mutated: an entry still shared
// with a snapshot is replaced by a private copy first. Callers hold m.mu.
func (m *AnchorStateManager) cowLocked(absPath string) *FileState {
	fs := m.files[absPath]
	if fs == nil || fs.epoch == m.epoch {
		return fs
	}
	private := fs.clone()
	private.epoch = m.epoch
	m.files[absPath] = private
	return private
}

// clone deep-copies an entry, so later mutations cannot reach a snapshot.
func (fs *FileState) clone() *FileState {
	out := &FileState{
		lines:        append([]string(nil), fs.lines...),
		content:      fs.content,
		lineToAnchor: make(map[int]string, len(fs.lineToAnchor)),
		anchorToLine: make(map[string]int, len(fs.anchorToLine)),
		stale:        make(map[string]staleAnchor, len(fs.stale)),
		unseen:       make(map[string]bool, len(fs.unseen)),
		epoch:        fs.epoch,
	}
	for k, v := range fs.lineToAnchor {
		out.lineToAnchor[k] = v
	}
	for k, v := range fs.anchorToLine {
		out.anchorToLine[k] = v
	}
	for k, v := range fs.stale {
		out.stale[k] = v
	}
	for k, v := range fs.unseen {
		out.unseen[k] = v
	}
	return out
}
