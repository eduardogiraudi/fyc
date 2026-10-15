package ui

import (
	"strings"
	"testing"
)

// grepLike is the shape that used to break the TUI: a matched source line with
// a leading tab, plus a very long line.
const grepLike = "helpers.go:42:\t\tif err != nil {\nmain.go:7:\tfunc main() { fmt.Println(strings.Repeat(\"x\", 200)) }"

func assertLinesFit(t *testing.T, label, block string, width int) {
	t.Helper()
	for i, line := range strings.Split(block, "\n") {
		if w := DisplayWidth(line); w > width {
			t.Fatalf("%s: line %d is %d cells wide, terminal is %d: %q", label, i, w, width, line)
		}
	}
}

func TestDisplayWidthCountsTabs(t *testing.T) {
	// "a" occupies cell 0, the tab jumps to cell 8, "b" sits on cell 8.
	if got := DisplayWidth("a\tb"); got != 9 {
		t.Fatalf(`DisplayWidth("a\tb") = %d, want 9 (tab advances to the next stop)`, got)
	}
	if got := DisplayWidth("abcdefg\th"); got != 9 {
		t.Fatalf(`DisplayWidth("abcdefg\th") = %d, want 9`, got)
	}
	if got := DisplayWidth("\x1b[31mred\x1b[0m"); got != 3 {
		t.Fatalf("escape sequences must be invisible, got width %d", got)
	}
}

func TestClampBlockFitsTerminalWidth(t *testing.T) {
	const width = 60
	fixtures := map[string]string{
		"tabs":              "a\t" + strings.Repeat("z", 80),
		"long token":        strings.Repeat("x", 300),
		"unbalanced SGR":    "\x1b[31m" + strings.Repeat("y", 200),
		"truncated CSI":     "\x1b[38;2;255" + strings.Repeat("k", 200),
		"OSC title":         "\x1b]0;pwned\x07" + strings.Repeat("o", 200),
		"wide runes":        strings.Repeat("漢", 100),
		"backspace + CR":    "abc\rdef" + strings.Repeat("g", 100),
		"multiline mixture": grepLike,
	}
	for name, in := range fixtures {
		out := ClampBlock(in, width)
		assertLinesFit(t, name, out, width)
	}
}

func TestStripControlSequences(t *testing.T) {
	out := stripControlSequences("ok \x1b[31mred\x1b]0;title\x07 tail\x01")
	if strings.Contains(out, "\x1b[31m") {
		t.Fatalf("SGR survived: %q", out)
	}
	if strings.Contains(out, "\x1b]0;") {
		t.Fatalf("OSC survived: %q", out)
	}
	if strings.ContainsRune(out, 0x01) {
		t.Fatalf("control character survived: %q", out)
	}
	if out != "ok red tail" {
		t.Fatalf("stripping lost or kept the wrong text: %q", out)
	}
	// An unterminated CSI would be swallowed by the terminal together with
	// whatever follows it, so the whole remainder is dropped.
	if got := stripControlSequences("a\x1b[38;2;255"); got != "a" {
		t.Fatalf("unterminated CSI must not reach the terminal: %q", got)
	}
}

func TestClampBlockClosesOpenStyles(t *testing.T) {
	out := ClampBlock("\x1b[38;5;208mbright text", 80)
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Fatalf("truncated line left its style open: %q", out)
	}
}

func TestFormatToolOutputNeverExceedsWidth(t *testing.T) {
	const width = 60
	cases := map[string]struct{ label, res string }{
		"grep with tabs":     {"grep pattern=TODO", grepLike},
		"long label":         {"ran 'go build -ldflags " + strings.Repeat("-X pkg.Var=v ", 8) + "./...'", "ok"},
		"long label + tabs":  {"read file.go", "\tline one\n" + strings.Repeat("b", 400)},
		"escape in payload":  {"ran 'cat log'", "\x1b[1;31mboom\x1b[0m" + strings.Repeat("z", 200)},
		"truncated CSI":      {"ran 'cat dump'", "\x1b[38;2;255" + strings.Repeat("A", 200)},
		"empty result":       {"ran 'true'", "   \n"},
		"single tiny result": {"ran 'true'", "ok"},
	}
	for name, c := range cases {
		out := FormatToolOutput(c.label, c.res, width, 2)
		if out == "" {
			continue
		}
		// FormatToolOutput prefixes the block with "\r" for the inline printer.
		assertLinesFit(t, name, strings.TrimPrefix(out, "\r"), width)
		if strings.Contains(out, "\x1b[38;2;255") {
			t.Fatalf("%s: unterminated CSI leaked into the display: %q", name, out)
		}
	}
}

func TestFormatToolOutputKeepsLabelAndResult(t *testing.T) {
	out := FormatToolOutput("read file.go", "package main", 80, 2)
	if !strings.Contains(out, "read file.go") || !strings.Contains(out, "package main") {
		t.Fatalf("output lost its label or payload: %q", out)
	}
}
