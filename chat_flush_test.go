package main

import (
	"strings"
	"testing"
)

// TestFlushIgnoresWhitespaceOnlyBuffer guards against a regression where the
// blank separator queued between tool calls (a bare "\n" in the text buffer)
// was rendered as a stray blank line whenever the next event flushed it.
func TestFlushIgnoresWhitespaceOnlyBuffer(t *testing.T) {
	m := &chatModel{}

	m.textBuf = "\n"
	if cmd := m.flushTextCmd(); cmd != nil {
		t.Fatal("flushTextCmd must ignore a whitespace-only buffer")
	}
	if m.textBuf != "" {
		t.Fatalf("buffer should be cleared, got %q", m.textBuf)
	}

	m.textBuf = "\n"
	if cmds := m.flushCompletedTextCmds(); len(cmds) != 0 {
		t.Fatal("flushCompletedTextCmds must ignore a whitespace-only block")
	}

	m.textBuf = "Error: boom\n"
	if cmds := m.flushCompletedTextCmds(); len(cmds) == 0 {
		t.Fatal("real text must still be flushed")
	}
	if strings.TrimSpace(m.textBuf) != "" {
		t.Fatalf("completed text should be consumed, got %q", m.textBuf)
	}
}
