package agui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fuckyourcli/types"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	if st, err := fs.Load("missing"); err != nil || st != nil {
		t.Fatalf("Load(missing) = %v, %v; want nil, nil", st, err)
	}

	st := &ThreadState{
		Seeded:   true,
		Messages: []types.Message{{Role: "user", Content: "hi"}},
		Interrupts: InterruptSnapshot{
			Open:     []ParkedInterrupt{{Interrupt: Interrupt{ID: "int_1"}}},
			Remember: []string{"k"},
			Resolved: map[string]string{"int_0": "sig"},
		},
	}
	if err := fs.Save("thread 1", st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := fs.Load("thread 1")
	if err != nil || got == nil {
		t.Fatalf("Load = %v, %v", got, err)
	}
	if !got.Seeded || len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Fatalf("messages not round-tripped: %+v", got.Messages)
	}
	if got.ThreadID != "thread 1" || got.Schema != threadStoreSchema {
		t.Fatalf("metadata not persisted: %+v", got)
	}
	if len(got.Interrupts.Open) != 1 || got.Interrupts.Open[0].Interrupt.ID != "int_1" {
		t.Fatalf("interrupts not round-tripped: %+v", got.Interrupts)
	}
	if got.Interrupts.Resolved["int_0"] != "sig" {
		t.Fatalf("resolved map lost: %+v", got.Interrupts.Resolved)
	}

	if err := fs.Delete("thread 1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if st, _ := fs.Load("thread 1"); st != nil {
		t.Fatal("thread survived Delete")
	}
}

func TestFileStoreHashesThreadIDs(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)
	if err := fs.Save("../../etc/passwd", &ThreadState{Seeded: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "threads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one stored file, got %d", len(entries))
	}
	if entries[0].Name() == "../../etc/passwd.json" {
		t.Fatal("thread id escaped the store directory")
	}
}

// newRunContext builds the run context a level-2 interaction needs.
func newRunContext(threadID, runID string) context.Context {
	return WithRun(context.Background(), NewSink(io.Discard, func() {}, threadID, runID), threadID, runID)
}

func TestBrokerSnapshotAndRestore(t *testing.T) {
	const threadID, runID = "thread_x", "run_y"
	b := NewBroker(5 * time.Minute)
	b.SetInterruptMode(true)

	ctx := newRunContext(threadID, runID)
	if _, err := b.Request(ctx, InteractionRequest{
		Kind: KindApproval, Title: "Run sh?", Options: []string{"Allow", "Deny"}, AllowIndex: 0, DenyIndex: 1,
	}, "sh:rm"); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Request = %v, want ErrInterrupted", err)
	}
	if open := b.OpenInterrupts(threadID); len(open) != 1 {
		t.Fatalf("open interrupts = %d, want 1", len(open))
	}
	snap := b.SnapshotThread(threadID)
	if len(snap.Open) != 1 {
		t.Fatalf("snapshot open = %d, want 1", len(snap.Open))
	}
	intrID := snap.Open[0].Interrupt.ID

	// A fresh broker (simulating a restart) rediscovers the open interrupt.
	b2 := NewBroker(5 * time.Minute)
	b2.SetInterruptMode(true)
	b2.RestoreThread(threadID, snap)
	if open := b2.OpenInterrupts(threadID); len(open) != 1 || open[0].ID != intrID {
		t.Fatalf("restored open interrupts = %+v", open)
	}

	payload := json.RawMessage(`{"approved":true,"remember":true}`)
	if err := b2.Resume(threadID, []ResumeEntry{{InterruptID: intrID, Status: ResumeStatusResolved, Payload: payload}}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if open := b2.OpenInterrupts(threadID); len(open) != 0 {
		t.Fatalf("interrupt still open after resume: %+v", open)
	}
	snap2 := b2.SnapshotThread(threadID)
	if _, ok := snap2.Resolved[intrID]; !ok {
		t.Fatal("resolved entry not captured")
	}
	if len(snap2.Remember) != 1 || snap2.Remember[0] != "sh:rm" {
		t.Fatalf("remember grants = %v, want [sh:rm]", snap2.Remember)
	}

	// The idempotent replay and the remember grant both survive another restart.
	b3 := NewBroker(5 * time.Minute)
	b3.SetInterruptMode(true)
	b3.RestoreThread(threadID, snap2)
	if err := b3.Resume(threadID, []ResumeEntry{{InterruptID: intrID, Status: ResumeStatusResolved, Payload: payload}}); err != nil {
		t.Fatalf("idempotent resume after restore: %v", err)
	}
	if !b3.isRemembered(threadID, "sh:rm") {
		t.Fatal("remember grant lost across restart")
	}
}

func TestBrokerRestoreDropsExpiredInterrupts(t *testing.T) {
	const threadID, runID = "thread_e", "run_e"
	b := NewBroker(time.Minute)
	b.SetInterruptMode(true)
	if _, err := b.Request(newRunContext(threadID, runID), InteractionRequest{
		Kind: KindApproval, Title: "x", AllowIndex: 0, DenyIndex: 1,
	}, ""); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Request = %v, want ErrInterrupted", err)
	}
	// Push the deadline into the past instead of relying on a sub-millisecond
	// TTL: a 1ns TTL is not observable when the wall clock is coarse (Windows
	// granularity is ~0.5ms), so the "already expired" premise held on Linux
	// only and the interrupt was wrongly resurrected there.
	b.mu.Lock()
	for _, p := range b.parked {
		p.expiresAt = time.Now().Add(-time.Second)
	}
	b.mu.Unlock()
	// The TTL already elapsed: the snapshot marks it cancelled and a restart
	// must not resurrect it, otherwise it would block a new turn forever.
	snap := b.SnapshotThread(threadID)
	b2 := NewBroker(time.Minute)
	b2.SetInterruptMode(true)
	b2.RestoreThread(threadID, snap)
	if open := b2.OpenInterrupts(threadID); len(open) != 0 {
		t.Fatalf("expired interrupt resurrected: %+v", open)
	}
}

func TestMemoryStoreKeepsNothing(t *testing.T) {
	var ms ThreadStore = MemoryStore{}
	if st, err := ms.Load("t"); st != nil || err != nil {
		t.Fatalf("Load = %v, %v; want nil, nil", st, err)
	}
	if err := ms.Save("t", &ThreadState{Seeded: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if st, _ := ms.Load("t"); st != nil {
		t.Fatal("MemoryStore persisted a thread")
	}
}
