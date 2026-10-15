package agui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

// threadStoreSchema is the on-disk format version of a persisted thread.
const threadStoreSchema = 1

// threadStoreMigrations upgrades older thread documents to the current schema.
var threadStoreMigrations = store.Migrations{
	0: func(data []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		doc["schema"] = json.RawMessage(strconv.Itoa(threadStoreSchema))
		return json.Marshal(doc)
	},
}

// ThreadState is the persistable state of one AG-UI thread: the conversation
// kept by the runner plus the level-2 interrupt bookkeeping of the broker.
type ThreadState struct {
	Schema     int               `json:"schema"`
	ThreadID   string            `json:"thread_id"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Seeded     bool              `json:"seeded"`
	Messages   []types.Message   `json:"messages,omitempty"`
	Interrupts InterruptSnapshot `json:"interrupts"`
}

// SchemaVersion implements store.SchemaVersioner.
func (ThreadState) SchemaVersion() int { return threadStoreSchema }

// InterruptSnapshot is the serialisable shape of a thread's level-2 state.
type InterruptSnapshot struct {
	// Open holds every parked interrupt of the thread (answered or not), so the
	// idempotency and "remember" rules survive a restart.
	Open []ParkedInterrupt `json:"open,omitempty"`
	// Remember holds the per-thread "allow always" grant keys.
	Remember []string `json:"remember,omitempty"`
	// Resolved maps an interrupt id to the signature of the resume that answered
	// it (the idempotency rule).
	Resolved map[string]string `json:"resolved,omitempty"`
}

// ParkedInterrupt is the persisted form of a parked interrupt.
type ParkedInterrupt struct {
	Interrupt   Interrupt          `json:"interrupt"`
	ThreadID    string             `json:"thread_id"`
	RunID       string             `json:"run_id"`
	ToolCallID  string             `json:"tool_call_id,omitempty"`
	Ident       string             `json:"ident"`
	Request     InteractionRequest `json:"request"`
	RememberKey string             `json:"remember_key,omitempty"`
	ExpiresAt   time.Time          `json:"expires_at,omitempty"`
	Answered    bool               `json:"answered,omitempty"`
	Consumed    bool               `json:"consumed,omitempty"`
	Cancelled   bool               `json:"cancelled,omitempty"`
	Answer      InteractionAnswer  `json:"answer,omitempty"`
	Sig         string             `json:"sig,omitempty"`
}

// ThreadStore persists ThreadState across process restarts.
type ThreadStore interface {
	// Load returns the stored state, or nil when the thread is unknown.
	Load(threadID string) (*ThreadState, error)
	// Save writes the state, creating it when new.
	Save(threadID string, st *ThreadState) error
	// Delete removes the stored state.
	Delete(threadID string) error
}

// MemoryStore is the default store: threads live only in memory, exactly like
// before persistence existed (zero behaviour change).
type MemoryStore struct{}

// Load implements ThreadStore.
func (MemoryStore) Load(string) (*ThreadState, error) { return nil, nil }

// Save implements ThreadStore.
func (MemoryStore) Save(string, *ThreadState) error { return nil }

// Delete implements ThreadStore.
func (MemoryStore) Delete(string) error { return nil }

// FileStore keeps one JSON document per thread under <dir>/threads/, written
// atomically and recoverable from ".bak" (see package store). The file name is
// the sha256 of the thread id, so a client-supplied id can never escape the
// directory.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore builds a FileStore rooted at dir.
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (f *FileStore) threadsDir() string { return filepath.Join(f.dir, "threads") }

func (f *FileStore) path(threadID string) string {
	sum := sha256.Sum256([]byte(threadID))
	return filepath.Join(f.threadsDir(), hex.EncodeToString(sum[:])+".json")
}

// Load implements ThreadStore.
func (f *FileStore) Load(threadID string) (*ThreadState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var st ThreadState
	if err := store.LoadVersioned(f.path(threadID), &st, threadStoreMigrations); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if st.ThreadID == "" {
		st.ThreadID = threadID
	}
	return &st, nil
}

// Save implements ThreadStore.
func (f *FileStore) Save(threadID string, st *ThreadState) error {
	if st == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st.Schema = threadStoreSchema
	st.ThreadID = threadID
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	if err := store.EnsureDir(f.threadsDir(), store.DirPerm); err != nil {
		return err
	}
	return store.WriteJSON(f.path(threadID), st, store.FilePerm, true)
}

// Delete implements ThreadStore.
func (f *FileStore) Delete(threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(f.path(threadID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
