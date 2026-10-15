package tools

import "testing"

func TestTailBufferKeepsOnlyTail(t *testing.T) {
	b := newTailBuffer(10)
	b.Write([]byte("0123456789ABCDE"))
	data, total := b.Snapshot()
	if total != 15 {
		t.Fatalf("total = %d, want 15", total)
	}
	if got := string(data); got != "56789ABCDE" {
		t.Fatalf("snapshot = %q, want last 10 bytes", got)
	}
}

func TestTailBufferAmortizedTrim(t *testing.T) {
	b := newTailBuffer(8)
	for _, chunk := range []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"} {
		b.Write([]byte(chunk))
	}
	data, total := b.Snapshot()
	if total != 24 {
		t.Fatalf("total = %d, want 24", total)
	}
	if got := string(data); got != "cccccccc" {
		t.Fatalf("snapshot = %q, want %q", got, "cccccccc")
	}
	if len(b.data) > 2*b.max {
		t.Fatalf("internal buffer grew past 2*max: %d > %d", len(b.data), 2*b.max)
	}
}

func TestTailBufferDefaultCap(t *testing.T) {
	b := newTailBuffer(0)
	if b.max != defaultTailBufferBytes {
		t.Fatalf("max = %d, want %d", b.max, defaultTailBufferBytes)
	}
}
