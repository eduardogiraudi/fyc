package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

// testWorkspace points the cache and the working directory at throw-away
// locations and returns the resulting per-workspace directory.
func testWorkspace(t *testing.T) string {
	t.Helper()
	cache := t.TempDir()
	workdir := t.TempDir()
	t.Setenv("FYC_CACHE_DIR", cache)
	t.Chdir(workdir)
	store.SetLogger(func(string, ...any) {})
	t.Cleanup(func() { store.SetLogger(nil) })
	return getChatHistoryDir()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestSaveChatSessionRoundTripWithImages(t *testing.T) {
	ws := testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/work"}
	s.ChatHistory = []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "tool", Name: "computer", Content: "shot", Images: []types.ImageAttachment{
			{MIMEType: "image/png", Data: b64("PNGDATA")},
		}},
	}
	if err := saveChatSession(s); err != nil {
		t.Fatalf("saveChatSession: %v", err)
	}
	if s.SessionID == "" {
		t.Fatal("session id was not assigned")
	}

	// The persisted JSON references a blob, not the inline base64 payload.
	raw, err := os.ReadFile(sessionPath(ws, s.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), b64("PNGDATA")) {
		t.Fatal("inline base64 payload leaked into the session file")
	}
	if !strings.Contains(string(raw), blobRefPrefix) {
		t.Fatal("no blob reference persisted")
	}

	// The in-memory history is never mutated by the persistence step.
	if strings.HasPrefix(s.ChatHistory[3].Images[0].Data, blobRefPrefix) {
		t.Fatal("in-memory history was rewritten to a blob reference")
	}

	// Reloading restores the base64 payload from the blob.
	msgs, err := loadWorkspaceChat(ws)
	if err != nil {
		t.Fatalf("loadWorkspaceChat: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	if got := msgs[3].Images; len(got) != 1 || got[0].MIMEType != "image/png" || got[0].Data != b64("PNGDATA") {
		t.Fatalf("image not restored: %+v", got)
	}

	blob := blobPath(ws, sha256Hex([]byte("PNGDATA")))
	if b, err := os.ReadFile(blob); err != nil || string(b) != "PNGDATA" {
		t.Fatalf("blob content = %q err=%v", b, err)
	}
}

func TestSaveChatSessionUpdatesOneSession(t *testing.T) {
	ws := testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{{Role: "user", Content: "first"}}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	id := s.SessionID
	s.ChatHistory = append(s.ChatHistory, types.Message{Role: "assistant", Content: "ok"})
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	if s.SessionID != id {
		t.Fatalf("session id changed: %q -> %q", id, s.SessionID)
	}
	metas, err := listSessions(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("index has %d sessions, want 1", len(metas))
	}
	if metas[0].MsgCount != 2 {
		t.Fatalf("msg_count = %d, want 2", metas[0].MsgCount)
	}
	if metas[0].Title != "first" {
		t.Fatalf("title = %q, want %q", metas[0].Title, "first")
	}
}

func TestLegacyChatMigration(t *testing.T) {
	ws := testWorkspace(t)
	if err := store.EnsureDir(ws, store.DirPerm); err != nil {
		t.Fatal(err)
	}
	legacy := []types.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "legacy question"}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(ws, "chat.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	msgs := loadChatHistory()
	if len(msgs) != 2 {
		t.Fatalf("migrated %d messages, want 2", len(msgs))
	}
	if _, err := os.Stat(indexPath(ws)); err != nil {
		t.Fatalf("index.json not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "chat.json.migrated")); err != nil {
		t.Fatalf("legacy chat.json not renamed: %v", err)
	}

	// A second read must use the migrated session, not re-migrate the legacy file.
	again := loadChatHistory()
	if len(again) != 2 {
		t.Fatalf("second read: %d messages, want 2", len(again))
	}
	metas, _ := listSessions(ws)
	if len(metas) != 1 {
		t.Fatalf("sessions after migration = %d, want 1", len(metas))
	}
}

func TestPruneSessionsAndBlobGC(t *testing.T) {
	ws := testWorkspace(t)
	base := time.Now().UTC()
	keys := make([]string, 3)
	idx := sessionIndex{Schema: sessionIndexSchema}
	for i := 0; i < 3; i++ {
		payload := []byte(fmt.Sprintf("payload-%d", i))
		keys[i] = sha256Hex(payload)
		if err := store.EnsureDir(blobsDir(ws), store.DirPerm); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteFile(blobPath(ws, keys[i]), payload, store.FilePerm); err != nil {
			t.Fatal(err)
		}
		id, _ := newSessionID(base)
		sf := sessionFile{
			ID:        id,
			CreatedAt: base,
			UpdatedAt: base.Add(time.Duration(i) * time.Minute),
			Messages: []types.Message{{Role: "tool", Images: []types.ImageAttachment{
				{MIMEType: "image/png", Data: blobRefPrefix + keys[i]},
			}}},
		}
		if err := writeSessionFile(ws, sf); err != nil {
			t.Fatal(err)
		}
		idx.Sessions = append(idx.Sessions, SessionMeta{ID: id, UpdatedAt: sf.UpdatedAt})
	}
	if err := saveSessionIndex(ws, idx); err != nil {
		t.Fatal(err)
	}

	pruneSessions(ws, &idx, 2)
	if err := saveSessionIndex(ws, idx); err != nil {
		t.Fatal(err)
	}
	if err := gcBlobs(ws); err != nil {
		t.Fatal(err)
	}

	if len(idx.Sessions) != 2 {
		t.Fatalf("kept %d sessions, want 2", len(idx.Sessions))
	}
	if _, err := os.Stat(blobPath(ws, keys[0])); !os.IsNotExist(err) {
		t.Fatalf("orphan blob of the pruned session was not collected: %v", err)
	}
	for _, k := range keys[1:] {
		if _, err := os.Stat(blobPath(ws, k)); err != nil {
			t.Fatalf("referenced blob was removed: %v", err)
		}
	}
}

func TestSaveClosesPendingToolCalls(t *testing.T) {
	ws := testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{
		{Role: "user", Content: "do it"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "c1", Function: types.ToolCallFunction{Name: "sh"}}}},
	}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	sf, err := readSessionFileRaw(ws, s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	last := sf.Messages[len(sf.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Fatalf("pending tool call was not closed: %+v", last)
	}
}

func TestCorruptSessionRecoveredFromBackup(t *testing.T) {
	ws := testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{{Role: "user", Content: "hello"}}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	// The second save writes the previous content to ".bak"; corrupting the
	// main file afterwards must not lose the session.
	s.ChatHistory = append(s.ChatHistory, types.Message{Role: "assistant", Content: "second"})
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath(ws, s.SessionID), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	sf, err := readSessionFileRaw(ws, s.SessionID)
	if err != nil {
		t.Fatalf("recovery from .bak failed: %v", err)
	}
	if len(sf.Messages) != 1 || sf.Messages[0].Content != "hello" {
		t.Fatalf("recovered the wrong version: %+v", sf.Messages)
	}
}

func TestCorruptIndexIsRebuilt(t *testing.T) {
	ws := testWorkspace(t)
	s := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{{Role: "user", Content: "hello"}}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath(ws), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(indexPath(ws) + store.BackupSuffix)
	metas, err := listSessions(ws)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != s.SessionID {
		t.Fatalf("index not rebuilt from sessions/: %+v", metas)
	}
}

func TestDeleteSession(t *testing.T) {
	ws := testWorkspace(t)
	s1 := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{{Role: "user", Content: "one"}}}
	if err := saveChatSession(s1); err != nil {
		t.Fatal(err)
	}
	s2 := &ChatSession{RestrictedRoot: "/w", ChatHistory: []types.Message{{Role: "user", Content: "two"}}}
	if err := saveChatSession(s2); err != nil {
		t.Fatal(err)
	}

	if err := deleteSession(ws, s1.SessionID); err != nil {
		t.Fatalf("deleteSession: %v", err)
	}
	if _, err := os.Stat(sessionPath(ws, s1.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("session file still present: %v", err)
	}
	metas, _ := listSessions(ws)
	if len(metas) != 1 || metas[0].ID != s2.SessionID {
		t.Fatalf("index after delete: %+v", metas)
	}
	if err := deleteSession(ws, "../evil"); err == nil {
		t.Fatal("deleteSession accepted a path-traversal id")
	}
}
