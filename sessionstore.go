package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/logger"
	"fuckyourcli/store"
	"fuckyourcli/types"
)

// The per-workspace chat store keeps multiple sessions under a directory:
//
//	<workspace>/index.json          -- one entry per session, newest first
//	<workspace>/sessions/<id>.json  -- the versioned session envelope
//	<workspace>/blobs/<sha256>      -- image payloads referenced by messages
//
// A legacy single-file "<workspace>/chat.json" is migrated into the new layout
// on first read and never written again.
const (
	sessionStoreSchema = 1
	sessionIndexSchema = 1

	// MaxSessionsPerWorkspace caps how many chat sessions are kept per
	// workspace. The oldest ones (and their orphan blobs) are pruned.
	MaxSessionsPerWorkspace = 50

	// sessionTitleMax is the number of runes kept for an automatic title.
	sessionTitleMax = 60

	// blobRefPrefix marks an ImageAttachment whose payload lives in a blob
	// file: the message carries "blob:<sha256>" instead of the base64 data.
	blobRefPrefix = "blob:"
)

// SessionMeta is one entry of the workspace session index.
type SessionMeta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	MsgCount  int       `json:"msg_count"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
}

// sessionIndex is the on-disk index.json envelope.
type sessionIndex struct {
	Schema   int           `json:"schema"`
	Sessions []SessionMeta `json:"sessions"`
}

func (sessionIndex) SchemaVersion() int { return sessionIndexSchema }

// sessionFile is the versioned envelope of a single chat session.
type sessionFile struct {
	Schema    int             `json:"schema"`
	ID        string          `json:"id"`
	Workspace string          `json:"workspace"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Commit    string          `json:"commit,omitempty"`
	Messages  []types.Message `json:"messages"`
}

func (sessionFile) SchemaVersion() int { return sessionStoreSchema }

// setSchemaField returns a migration that stamps the "schema" field, used to
// upgrade a document written before the field existed.
func setSchemaField(version int) func([]byte) ([]byte, error) {
	return func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		doc["schema"] = json.RawMessage(strconv.Itoa(version))
		return json.Marshal(doc)
	}
}

var (
	sessionIndexMigrations = store.Migrations{0: setSchemaField(sessionIndexSchema)}
	sessionMigrations      = store.Migrations{0: setSchemaField(sessionStoreSchema)}
)

func sessionsDir(workspaceDir string) string { return filepath.Join(workspaceDir, "sessions") }
func blobsDir(workspaceDir string) string    { return filepath.Join(workspaceDir, "blobs") }
func indexPath(workspaceDir string) string   { return filepath.Join(workspaceDir, "index.json") }

func sessionPath(workspaceDir, id string) string {
	return filepath.Join(sessionsDir(workspaceDir), id+".json")
}

func blobPath(workspaceDir, key string) string {
	return filepath.Join(blobsDir(workspaceDir), key)
}

// newSessionID combines a sortable timestamp with a random conversation id.
func newSessionID(t time.Time) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session store: generate id: %w", err)
	}
	return t.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

// validSessionID guards against path traversal from a user-supplied id.
func validSessionID(id string) bool {
	if len(id) < 17 || len(id) > 64 || !strings.Contains(id, "-") {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && r != '-' {
			return false
		}
	}
	return true
}

// validBlobKey accepts only a 64-character lowercase hex digest.
func validBlobKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, r := range key {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sessionTitle derives a one-line title from the first user message.
func sessionTitle(history []types.Message) string {
	for _, m := range history {
		if m.Role != "user" {
			continue
		}
		text := strings.Join(strings.Fields(m.Content), " ")
		if text == "" {
			continue
		}
		if runes := []rune(text); len(runes) > sessionTitleMax {
			text = strings.TrimSpace(string(runes[:sessionTitleMax])) + "…"
		}
		return text
	}
	return ""
}

// hasConversation reports whether the history holds anything worth persisting:
// a session carrying only the system prompt is never written.
func hasConversation(history []types.Message) bool {
	for _, m := range history {
		if m.Role != "system" {
			return true
		}
	}
	return false
}

// encodeImages copies the history replacing every inline base64 image with a
// "blob:<sha256>" reference, writing the decoded payload to blobs/. The input
// slice and its attachments are never modified.
func encodeImages(workspaceDir string, msgs []types.Message) ([]types.Message, error) {
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		imgs := make([]types.ImageAttachment, len(out[i].Images))
		copy(imgs, out[i].Images)
		for j := range imgs {
			img := imgs[j]
			if img.Data == "" || strings.HasPrefix(img.Data, blobRefPrefix) {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(img.Data)
			if err != nil {
				// Not decodable: keep it inline rather than lose it.
				continue
			}
			key := sha256Hex(raw)
			if err := store.EnsureDir(blobsDir(workspaceDir), store.DirPerm); err != nil {
				return nil, err
			}
			if err := store.WriteFile(blobPath(workspaceDir, key), raw, store.FilePerm); err != nil {
				return nil, err
			}
			img.Data = blobRefPrefix + key
			imgs[j] = img
		}
		out[i].Images = imgs
	}
	return out, nil
}

// decodeImages restores the base64 payload of every blob reference in place.
// A missing or invalid blob is left as a reference rather than guessed at.
func decodeImages(workspaceDir string, msgs []types.Message) {
	for i := range msgs {
		for j := range msgs[i].Images {
			data := msgs[i].Images[j].Data
			if !strings.HasPrefix(data, blobRefPrefix) {
				continue
			}
			key := strings.TrimPrefix(data, blobRefPrefix)
			if !validBlobKey(key) {
				continue
			}
			raw, err := os.ReadFile(blobPath(workspaceDir, key))
			if err != nil {
				continue
			}
			msgs[i].Images[j].Data = base64.StdEncoding.EncodeToString(raw)
		}
	}
}

// loadSessionIndex reads index.json, falling back to a rebuild from the
// sessions/ directory when the file is missing or corrupt.
func loadSessionIndex(workspaceDir string) (sessionIndex, error) {
	var idx sessionIndex
	err := store.LoadVersioned(indexPath(workspaceDir), &idx, sessionIndexMigrations)
	if err != nil {
		if os.IsNotExist(err) {
			return sessionIndex{Schema: sessionIndexSchema}, nil
		}
		store.Notice("session store: %v; rebuilding index from sessions/", err)
		return rebuildIndex(workspaceDir)
	}
	if idx.Schema == 0 {
		idx.Schema = sessionIndexSchema
	}
	return idx, nil
}

// rebuildIndex reconstructs the index by scanning the session files.
func rebuildIndex(workspaceDir string) (sessionIndex, error) {
	idx := sessionIndex{Schema: sessionIndexSchema}
	entries, err := os.ReadDir(sessionsDir(workspaceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return idx, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		sf, err := readSessionFileRaw(workspaceDir, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		idx.Sessions = append(idx.Sessions, metaForFile(sf))
	}
	return idx, nil
}

func saveSessionIndex(workspaceDir string, idx sessionIndex) error {
	idx.Schema = sessionIndexSchema
	if idx.Sessions == nil {
		idx.Sessions = []SessionMeta{}
	}
	return store.WriteJSON(indexPath(workspaceDir), idx, store.FilePerm, true)
}

func metaForFile(sf *sessionFile) SessionMeta {
	return SessionMeta{
		ID:        sf.ID,
		Title:     sessionTitle(sf.Messages),
		CreatedAt: sf.CreatedAt,
		UpdatedAt: sf.UpdatedAt,
		MsgCount:  len(sf.Messages),
		Provider:  sf.Provider,
		Model:     sf.Model,
	}
}

func upsertSessionMeta(idx *sessionIndex, meta SessionMeta) {
	for i := range idx.Sessions {
		if idx.Sessions[i].ID == meta.ID {
			idx.Sessions[i] = meta
			return
		}
	}
	idx.Sessions = append(idx.Sessions, meta)
}

func writeSessionFile(workspaceDir string, sf sessionFile) error {
	if err := store.EnsureDir(sessionsDir(workspaceDir), store.DirPerm); err != nil {
		return err
	}
	sf.Schema = sessionStoreSchema
	return store.WriteJSON(sessionPath(workspaceDir, sf.ID), sf, store.FilePerm, true)
}

func readSessionFileRaw(workspaceDir, id string) (*sessionFile, error) {
	if !validSessionID(id) {
		return nil, fmt.Errorf("session store: invalid session id %q", id)
	}
	var sf sessionFile
	if err := store.LoadVersioned(sessionPath(workspaceDir, id), &sf, sessionMigrations); err != nil {
		return nil, err
	}
	return &sf, nil
}

func readSessionFile(workspaceDir, id string) (*sessionFile, error) {
	sf, err := readSessionFileRaw(workspaceDir, id)
	if err != nil {
		return nil, err
	}
	decodeImages(workspaceDir, sf.Messages)
	return sf, nil
}

// pruneSessions keeps at most max sessions (the most recently updated) and
// removes the files of the pruned ones. Orphan blobs are collected later by
// gcBlobs.
func pruneSessions(workspaceDir string, idx *sessionIndex, max int) {
	if max <= 0 {
		return
	}
	sort.Slice(idx.Sessions, func(i, j int) bool {
		return idx.Sessions[i].UpdatedAt.After(idx.Sessions[j].UpdatedAt)
	})
	if len(idx.Sessions) <= max {
		return
	}
	for _, m := range idx.Sessions[max:] {
		_ = os.Remove(sessionPath(workspaceDir, m.ID))
		if err := logger.RemoveSession(m.ID); err != nil {
			store.Notice("logs: could not prune session: %v", err)
		}
	}
	idx.Sessions = idx.Sessions[:max]
}

var blobRefRe = regexp.MustCompile(`blob:([0-9a-f]{64})`)

// referencedBlobs returns every blob key still cited by a readable session
// file. Corrupt session files are skipped: their blobs may be collected, which
// is acceptable because the session itself cannot be read back.
func referencedBlobs(workspaceDir string) (map[string]bool, error) {
	refs := map[string]bool{}
	entries, err := os.ReadDir(sessionsDir(workspaceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return refs, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionsDir(workspaceDir), e.Name()))
		if err != nil {
			continue
		}
		for _, m := range blobRefRe.FindAllSubmatch(data, -1) {
			refs[string(m[1])] = true
		}
	}
	return refs, nil
}

// gcBlobs deletes every blob no longer referenced by a session; a blob may be
// shared by several sessions, so the reference set spans the whole workspace.
func gcBlobs(workspaceDir string) error {
	refs, err := referencedBlobs(workspaceDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(blobsDir(workspaceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || refs[e.Name()] {
			continue
		}
		_ = os.Remove(filepath.Join(blobsDir(workspaceDir), e.Name()))
	}
	return nil
}

// saveChatSession persists the current chat as the session identified by
// s.SessionID (a new one is created on the first call) and refreshes the
// workspace index. It is the autosave entry point: called at the end of every
// turn and on exit.
func saveChatSession(s *ChatSession) error {
	if s == nil || !hasConversation(s.ChatHistory) {
		return nil
	}
	workspaceDir := getChatHistoryDir()
	if err := store.EnsureDir(workspaceDir, store.DirPerm); err != nil {
		return err
	}

	// Keep the persisted history valid: a tool call left without a result (an
	// interrupted turn) would make the conversation unusable for a provider.
	if pending := pendingToolCalls(s.ChatHistory); len(pending) > 0 {
		closePendingToolCalls(s, pending)
	}

	now := time.Now().UTC()
	if err := ensureSessionIdentity(s); err != nil {
		return err
	}

	msgs, err := encodeImages(workspaceDir, s.ChatHistory)
	if err != nil {
		return err
	}

	cfg := config.Get()
	sf := sessionFile{
		ID:        s.SessionID,
		Workspace: s.RestrictedRoot,
		CreatedAt: s.SessionCreatedAt,
		UpdatedAt: now,
		Provider:  string(cfg.Provider),
		Model:     cfg.Model,
		Commit:    s.Commit,
		Messages:  msgs,
	}
	// Persist the session before the index: a crash in between leaves an
	// orphan session file, never an index pointing at a missing one.
	if err := writeSessionFile(workspaceDir, sf); err != nil {
		return err
	}

	idx, err := loadSessionIndex(workspaceDir)
	if err != nil {
		return err
	}
	upsertSessionMeta(&idx, SessionMeta{
		ID:        sf.ID,
		Title:     s.SessionTitle,
		CreatedAt: sf.CreatedAt,
		UpdatedAt: sf.UpdatedAt,
		MsgCount:  len(msgs),
		Provider:  sf.Provider,
		Model:     sf.Model,
	})
	pruneSessions(workspaceDir, &idx, MaxSessionsPerWorkspace)
	if err := saveSessionIndex(workspaceDir, idx); err != nil {
		return err
	}
	return gcBlobs(workspaceDir)
}

// listSessions returns the workspace sessions, most recently updated first.
func listSessions(workspaceDir string) ([]SessionMeta, error) {
	idx, err := loadSessionIndex(workspaceDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(idx.Sessions, func(i, j int) bool {
		return idx.Sessions[i].UpdatedAt.After(idx.Sessions[j].UpdatedAt)
	})
	return idx.Sessions, nil
}

// deleteSession removes a session and collects its now-orphan blobs.
func deleteSession(workspaceDir, id string) error {
	if !validSessionID(id) {
		return fmt.Errorf("session store: invalid session id %q", id)
	}
	if err := os.Remove(sessionPath(workspaceDir, id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	idx, err := loadSessionIndex(workspaceDir)
	if err != nil {
		return err
	}
	kept := idx.Sessions[:0]
	for _, m := range idx.Sessions {
		if m.ID != id {
			kept = append(kept, m)
		}
	}
	idx.Sessions = kept
	if err := saveSessionIndex(workspaceDir, idx); err != nil {
		return err
	}
	if err := logger.RemoveSession(id); err != nil {
		store.Notice("logs: could not delete session: %v", err)
	}
	return gcBlobs(workspaceDir)
}

// latestSessionID returns the id of the most recently updated session, or "".
func latestSessionID(workspaceDir string) string {
	metas, err := listSessions(workspaceDir)
	if err != nil || len(metas) == 0 {
		return ""
	}
	return metas[0].ID
}

func loadLatestSession(workspaceDir string) ([]types.Message, error) {
	id := latestSessionID(workspaceDir)
	if id == "" {
		return nil, nil
	}
	sf, err := readSessionFile(workspaceDir, id)
	if err != nil {
		return nil, err
	}
	return sf.Messages, nil
}

// loadChatHistory reads the most recent chat session for the current workspace,
// migrating a legacy chat.json file on first read. Errors are reported through
// the store logger and yield an empty history.
func loadChatHistory() []types.Message {
	msgs, err := loadWorkspaceChat(getChatHistoryDir())
	if err != nil {
		store.Notice("session store: %v", err)
		return nil
	}
	return msgs
}

func loadWorkspaceChat(workspaceDir string) ([]types.Message, error) {
	legacy := filepath.Join(workspaceDir, "chat.json")
	if _, err := os.Stat(indexPath(workspaceDir)); os.IsNotExist(err) {
		if _, lerr := os.Stat(legacy); lerr == nil {
			msgs, mErr := migrateLegacyChat(workspaceDir, legacy)
			if mErr != nil {
				store.Notice("session store: legacy migration failed: %v", mErr)
			} else {
				return msgs, nil
			}
		}
	}
	return loadLatestSession(workspaceDir)
}

// migrateLegacyChat imports a pre-multi-session chat.json into the new layout.
func migrateLegacyChat(workspaceDir, legacy string) ([]types.Message, error) {
	data, err := os.ReadFile(legacy)
	if err != nil {
		return nil, err
	}
	var msgs []types.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", store.ErrCorrupt, legacy, err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	created := time.Now().UTC()
	if info, err := os.Stat(legacy); err == nil {
		created = info.ModTime().UTC()
	}
	id, err := newSessionID(created)
	if err != nil {
		return nil, err
	}
	if err := store.EnsureDir(workspaceDir, store.DirPerm); err != nil {
		return nil, err
	}
	if err := store.EnsureDir(blobsDir(workspaceDir), store.DirPerm); err != nil {
		return nil, err
	}
	persisted, err := encodeImages(workspaceDir, msgs)
	if err != nil {
		return nil, err
	}

	cwd, _ := os.Getwd()
	cfg := config.Get()
	sf := sessionFile{
		ID:        id,
		Workspace: cwd,
		CreatedAt: created,
		UpdatedAt: created,
		Provider:  string(cfg.Provider),
		Model:     cfg.Model,
		Messages:  persisted,
	}
	if err := writeSessionFile(workspaceDir, sf); err != nil {
		return nil, err
	}

	meta := metaForFile(&sf)
	meta.Title = sessionTitle(msgs)
	idx := sessionIndex{Schema: sessionIndexSchema, Sessions: []SessionMeta{meta}}
	if err := saveSessionIndex(workspaceDir, idx); err != nil {
		return nil, err
	}

	// Never read the legacy file again. The presence of index.json is the
	// authoritative guard; the rename is best-effort tidiness.
	_ = os.Rename(legacy, legacy+".migrated")
	return sf.Messages, nil
}
