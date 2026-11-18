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

	m.textBuf = "Error: boom\n\n"
	if cmds := m.flushCompletedTextCmds(); len(cmds) == 0 {
		t.Fatal("real text must still be flushed")
	}
	if strings.TrimSpace(m.textBuf) != "" {
		t.Fatalf("completed text should be consumed, got %q", m.textBuf)
	}
}

func TestMarkdownSoftBreakAcrossTokens(t *testing.T) {
	m := &chatModel{width: 80, transcript: &transcriptState{}}
	m.textBuf = "Run this\n"
	if cmds := m.flushCompletedTextCmds(); len(cmds) != 0 {
		t.Fatal("a soft break must remain buffered")
	}
	m.textBuf += "command.\n\n"
	m.flushCompletedTextCmds()
	if len(m.transcript.lines) != 1 {
		t.Fatalf("paragraph split across %d rows", len(m.transcript.lines))
	}
	m.transcript.selActive = true
	m.transcript.selAnchor = cellPos{row: 0, col: 0}
	m.transcript.selCursor = cellPos{row: 0, col: 80}
	if got := m.transcript.SelectionText(); got != "Run this command." {
		t.Fatalf("copied text = %q", got)
	}
}
