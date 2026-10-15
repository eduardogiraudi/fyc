package agui

import (
	"sort"
	"time"
)

// SnapshotThread returns the serialisable level-2 state of a thread: its parked
// interrupts (answered or not), the per-thread remember grants and the
// idempotency map. An interrupt whose TTL elapsed while it was still open is
// reconciled as cancelled, so a restart never resurrects a dead interrupt.
func (b *Broker) SnapshotThread(threadID string) InterruptSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureInterruptMapsLocked()

	now := time.Now()
	snap := InterruptSnapshot{Resolved: map[string]string{}}
	for _, id := range b.order[threadID] {
		p := b.parked[id]
		if p == nil {
			continue
		}
		if !p.answered && !p.expiresAt.IsZero() && now.After(p.expiresAt) {
			p.cancelled = true
		}
		snap.Open = append(snap.Open, ParkedInterrupt{
			Interrupt:   p.interrupt,
			ThreadID:    p.threadID,
			RunID:       p.runID,
			ToolCallID:  p.toolCallID,
			Ident:       p.ident,
			Request:     p.request,
			RememberKey: p.rememberKey,
			ExpiresAt:   p.expiresAt,
			Answered:    p.answered,
			Consumed:    p.consumed,
			Cancelled:   p.cancelled,
			Answer:      p.answer,
			Sig:         p.sig,
		})
		if sig, ok := b.resolved[id]; ok {
			snap.Resolved[id] = sig
		}
	}
	for key := range b.remember[threadID] {
		snap.Remember = append(snap.Remember, key)
	}
	sort.Strings(snap.Remember)
	return snap
}

// RestoreThread installs a persisted level-2 state for a thread that is not yet
// known to the broker. Interrupts whose TTL elapsed while the process was down
// are dropped: a resume for them could only fail, so they must not block new
// input on the thread (the TTL reconciliation the plan calls for).
func (b *Broker) RestoreThread(threadID string, snap InterruptSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureInterruptMapsLocked()
	if len(b.order[threadID]) > 0 {
		return // live state wins over the persisted copy
	}

	now := time.Now()
	for _, rec := range snap.Open {
		if rec.ThreadID != "" && rec.ThreadID != threadID {
			continue
		}
		id := rec.Interrupt.ID
		if id == "" {
			continue
		}
		if !rec.Answered && !rec.ExpiresAt.IsZero() && now.After(rec.ExpiresAt) {
			continue
		}
		b.parked[id] = &parkedInterrupt{
			interrupt:   rec.Interrupt,
			threadID:    threadID,
			runID:       rec.RunID,
			toolCallID:  rec.ToolCallID,
			ident:       rec.Ident,
			request:     rec.Request,
			rememberKey: rec.RememberKey,
			expiresAt:   rec.ExpiresAt,
			answered:    rec.Answered,
			consumed:    rec.Consumed,
			cancelled:   rec.Cancelled,
			answer:      rec.Answer,
			sig:         rec.Sig,
		}
		b.order[threadID] = append(b.order[threadID], id)
	}
	for _, key := range snap.Remember {
		if b.remember[threadID] == nil {
			b.remember[threadID] = map[string]bool{}
		}
		b.remember[threadID][key] = true
	}
	for id, sig := range snap.Resolved {
		b.resolved[id] = sig
	}
}
