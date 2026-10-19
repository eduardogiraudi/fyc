package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// numberedLines builds "line 0001" … "line N" content.
func zeroPaddedLines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %04d\n", i)
	}
	return sb.String()
}

func writeToolTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReadToolReturnsExactBytesWhenUnderTheCap(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "small.txt", "alpha\nbeta\n")
	if got := executeReadTool(map[string]any{"path": "small.txt"}, root, nil); got != "alpha\nbeta\n" {
		t.Fatalf("read = %q, want the exact file bytes", got)
	}
}

func TestReadToolWindowsLongFile(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "big.txt", zeroPaddedLines(3000))

	out := executeReadTool(map[string]any{"path": "big.txt"}, root, nil)
	if !strings.HasPrefix(out, "[read: lines 1-2000 of 3000") {
		t.Fatalf("note must lead the payload, got %q", out[:min(len(out), 120)])
	}
	parts := strings.Split(out, "\n")
	if len(parts) != readMaxLines+1 {
		t.Fatalf("got %d lines, want the note plus %d body lines", len(parts), readMaxLines)
	}
	if parts[1] != "line 0001" || parts[readMaxLines] != "line 2000" {
		t.Fatalf("window misaligned: first %q last %q", parts[1], parts[readMaxLines])
	}

	tail := executeReadTool(map[string]any{"path": "big.txt", "offset": 2001}, root, nil)
	if !strings.HasPrefix(tail, "[read: lines 2001-3000 of 3000 — end of file]") {
		t.Fatalf("tail note = %q", strings.SplitN(tail, "\n", 2)[0])
	}
	tailParts := strings.Split(tail, "\n")
	if len(tailParts) != 1001 || tailParts[1] != "line 2001" || tailParts[1000] != "line 3000" {
		t.Fatalf("tail window wrong: %d lines, first %q last %q", len(tailParts)-1, tailParts[1], tailParts[len(tailParts)-1])
	}
}

func TestReadToolHonoursLimitAndOffset(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "big.txt", zeroPaddedLines(500))

	out := executeReadTool(map[string]any{"path": "big.txt", "offset": 10, "limit": 5}, root, nil)
	parts := strings.Split(out, "\n")
	if !strings.HasPrefix(parts[0], "[read: lines 10-14 of 500") {
		t.Fatalf("note = %q", parts[0])
	}
	if len(parts) != 6 || parts[1] != "line 0010" || parts[5] != "line 0014" {
		t.Fatalf("window wrong: %q", out)
	}
}

func TestReadToolByteBudgetCutsInsideOneLine(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "minified.js", strings.Repeat("x", 300*1024))

	out := executeReadTool(map[string]any{"path": "minified.js"}, root, nil)
	first, body, _ := strings.Cut(out, "\n")
	if !strings.Contains(first, "byte budget") || !strings.Contains(first, "longer than the byte budget") {
		t.Fatalf("note = %q", first)
	}
	if len(body) != readMaxBytes {
		t.Fatalf("body = %d bytes, want %d", len(body), readMaxBytes)
	}
}

func TestReadToolRejectsOffsetPastEOF(t *testing.T) {
	root := t.TempDir()
	writeToolTestFile(t, root, "small.txt", "one\ntwo\n")
	if got := executeReadTool(map[string]any{"path": "small.txt", "offset": 99}, root, nil); !strings.Contains(got, "beyond the end of the file") {
		t.Fatalf("got %q", got)
	}
}

func TestGrepToolCapsMatchesWithLeadingNote(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&sb, "needle %03d\n", i)
	}
	writeToolTestFile(t, root, "hits.txt", sb.String())

	out := executeGrepTool(map[string]any{"pattern": "needle"}, root, nil)
	parts := strings.Split(out, "\n")
	if !strings.HasPrefix(parts[0], "[grep: showing 200 of 250 matches") {
		t.Fatalf("note = %q", parts[0])
	}
	if len(parts) != 201 {
		t.Fatalf("got %d lines, want note + %d matches", len(parts), grepMaxMatches)
	}

	short := executeGrepTool(map[string]any{"pattern": "needle", "head_limit": 50}, root, nil)
	if !strings.HasPrefix(short, "[grep: showing 50 of 250 matches") {
		t.Fatalf("head_limit note = %q", strings.SplitN(short, "\n", 2)[0])
	}

	full := executeGrepTool(map[string]any{"pattern": "needle", "head_limit": 1000}, root, nil)
	if strings.HasPrefix(full, "[grep:") || strings.Count(full, "needle") != 250 {
		t.Fatalf("uncapped answer must have no note: %q", full[:80])
	}
}

func TestGetAnchorsToolWindowsLongFile(t *testing.T) {
	root, _ := setupAnchorFile(t, zeroPaddedLines(2500))

	out := executeGetAnchorsTool(map[string]any{"path": "file.txt"}, root, nil)
	parts := strings.Split(out, "\n")
	if !strings.HasPrefix(parts[0], "[get_anchors: lines 1-2000 of 2500") || !strings.Contains(parts[0], "stay valid") {
		t.Fatalf("note = %q", parts[0])
	}
	if len(parts) != readMaxLines+1 {
		t.Fatalf("got %d lines, want note + %d", len(parts), readMaxLines)
	}
	if !strings.HasSuffix(parts[1], "§line 0001") {
		t.Fatalf("first anchored line = %q", parts[1])
	}

	tail := executeGetAnchorsTool(map[string]any{"path": "file.txt", "offset": 2001}, root, nil)
	tailParts := strings.Split(tail, "\n")
	if !strings.HasPrefix(tailParts[0], "[get_anchors: lines 2001-2500 of 2500 — end of file") {
		t.Fatalf("tail note = %q", tailParts[0])
	}
	if len(tailParts) != 501 || !strings.HasSuffix(tailParts[500], "§line 2500") {
		t.Fatalf("tail window wrong: %d lines", len(tailParts)-1)
	}
}
