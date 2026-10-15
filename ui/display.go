package ui

import (
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// tabStopWidth is the tab stop every terminal we target uses when nothing
// reconfigures it. Width helpers must account for it: measuring a tab as zero
// cells (what reflow/ansi and charmbracelet/x/ansi both do) makes a line look
// narrower than the terminal prints it, the line then soft-wraps, and the wrap
// desynchronises the Bubble Tea renderer's line accounting.
const tabStopWidth = 8

// DisplayWidth returns the number of terminal cells s occupies when printed:
// escape sequences are invisible and tabs advance to the next tab stop.
// It expects a single line (callers split on "\n" first).
func DisplayWidth(s string) int {
	width := 0
	for i, seg := range strings.Split(s, "\t") {
		if i > 0 {
			width += tabStopWidth - (width % tabStopWidth)
		}
		width += xansi.StringWidth(seg)
	}
	return width
}

// expandTabs replaces every tab with the spaces a terminal would emit. Column
// bookkeeping restarts per line, which is what we want: each display line is
// emitted on its own row.
func expandTabs(s string, tabWidth int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	width := 0
	for i, seg := range strings.Split(s, "\t") {
		if i > 0 {
			n := tabWidth - (width % tabWidth)
			b.WriteString(strings.Repeat(" ", n))
			width += n
		}
		b.WriteString(seg)
		width += xansi.StringWidth(seg)
	}
	return b.String()
}

// stripControlSequences removes every ANSI escape sequence and every C0
// control character, keeping only "\n" and "\t". Tool output is untrusted
// payload: an unterminated CSI or an OSC (window title, clipboard) would be
// consumed by the terminal together with whatever follows it — including the
// UI we draw afterwards — so nothing but text may reach the display.
func stripControlSequences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == 0x1b:
			i += escapeSequenceLen(s[i:])
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// escapeSequenceLen returns the length of the escape sequence starting at
// s[0] (which must be ESC). A malformed or unterminated sequence consumes the
// rest of the string: the terminal would have swallowed it anyway.
func escapeSequenceLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameter/intermediate bytes, then one final byte
		for i := 2; i < len(s); i++ {
			if c := s[i]; c >= 0x40 && c <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']', 'P', 'X', '^', '_': // OSC/DCS/SOS/PM/APC: until BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return min(2, len(s))
	}
}

// clampLine guarantees the line fits in width cells: tabs are expanded, the
// text is cut on a cell boundary (escape-aware) and any style left open by the
// cut is closed, so a truncated line can never bleed into the following ones.
func clampLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = strings.ReplaceAll(line, "\r", "")
	line = expandTabs(line, tabStopWidth)
	if DisplayWidth(line) > width {
		line = xansi.Truncate(line, width, "…")
	}
	return closeStyles(line)
}

// closeStyles appends a SGR reset when the line carries escape sequences and
// does not close them itself.
func closeStyles(line string) string {
	if !strings.Contains(line, "\x1b") || strings.HasSuffix(line, "\x1b[0m") {
		return line
	}
	return line + "\x1b[0m"
}

// ClampBlock applies clampLine to every line of s.
//
// Every string handed to the terminal must go through it — Bubble Tea's
// tea.Println included, which writes queued lines verbatim, without the
// truncation it applies to the view. One over-wide line soft-wraps, the
// renderer's line count no longer matches the terminal, and the next repaint
// (status bar, input) lands on the wrong rows.
func ClampBlock(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = clampLine(l, width)
	}
	return strings.Join(lines, "\n")
}
