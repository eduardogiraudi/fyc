package tools

import "sync"

// defaultTailBufferBytes caps how much output is kept in memory for a single
// persistent process (512 KiB). Older bytes are discarded from the front, so a
// chatty server can never grow the buffer without bound.
const defaultTailBufferBytes = 512 * 1024

// tailBuffer is a concurrency-safe byte buffer that keeps only the most recent
// max bytes written to it. The persistent-process reader goroutine writes while
// sniff/kill/snapshot read from other goroutines, so every access is guarded.
type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	max   int
	total int64 // total bytes ever written, including the discarded ones
}

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = defaultTailBufferBytes
	}
	return &tailBuffer{max: max}
}

// Write appends p, trimming the oldest bytes from the front once the buffer
// grows past twice its capacity. Trimming in one shot (instead of on every
// write at capacity) amortizes the copy cost for very chatty processes while
// keeping memory bounded by 2*max.
func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	b.data = append(b.data, p...)
	if len(b.data) > 2*b.max {
		cut := len(b.data) - b.max
		copied := copy(b.data, b.data[cut:])
		b.data = b.data[:copied]
	}
	return n, nil
}

// Snapshot returns a copy of the most recent (up to) max bytes and the total
// number of bytes ever written. When the buffer trimmed in the middle of a
// line, the leading element may be a partial line: callers that care about
// whole lines should drop it (see persistentProcess.sniff).
func (b *tailBuffer) Snapshot() ([]byte, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.data
	if len(kept) > b.max {
		kept = kept[len(kept)-b.max:]
	}
	return append([]byte(nil), kept...), b.total
}
