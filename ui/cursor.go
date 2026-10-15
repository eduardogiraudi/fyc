// Package ui — shared terminal cursor/screen utilities.
//
// Centralises ANSI escape sequences so that terminal.go, markdown_stream.go,
// and any future UI code use the same primitives instead of hardcoding escapes.
package ui

import (
	"fmt"
	"sync"
)

// ── Screen clearing ──────────────────────────────────────────────────────────

// ClearToEOS clears everything from the cursor to the end of screen (\033[J).
func ClearToEOS() { fmt.Print("\033[J") }

// ClearLine clears the current line from column 0 (\r\033[2K).
func ClearLine() { fmt.Print("\r\033[2K") }

// CursorUp moves the cursor up n lines (\033[nA).
func CursorUp(n int) {
	if n <= 0 {
		return
	}
	fmt.Printf("\033[%dA", n)
}

// EraseLines moves the cursor up n lines and clears to end of screen.
// Equivalent to CursorUp(n) followed by ClearToEOS().
func EraseLines(n int) {
	if n <= 0 {
		ClearLine()
		return
	}
	fmt.Printf("\033[%dA\r\033[J", n)
}

// ── ANSI color helpers ───────────────────────────────────────────────────────

// parseHex parses a hex color string (#RRGGBB or #RGB) to (R, G, B).
// Returns (0,0,0) on malformed input.
func parseHex(s string) (uint8, uint8, uint8) {
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return 0, 0, 0
	}
	var r, g, b uint8
	for i := 0; i < 6; i++ {
		c := s[i]
		var v uint8
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, 0, 0
		}
		switch i {
		case 0:
			r = v << 4
		case 1:
			r |= v
		case 2:
			g = v << 4
		case 3:
			g |= v
		case 4:
			b = v << 4
		case 5:
			b |= v
		}
	}
	return r, g, b
}

// FgANSI returns a 24-bit foreground ANSI escape sequence for the given hex color.
// Example: FgANSI("#89b4fa") → "\x1b[38;2;137;180;250m"
func FgANSI(hex string) string {
	r, g, b := parseHex(hex)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// BgANSI returns a 24-bit background ANSI escape sequence for the given hex color.
// Example: BgANSI("#143417") → "\x1b[48;2;20;52;23m"
func BgANSI(hex string) string {
	r, g, b := parseHex(hex)
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

// ── Spinner (ticker-based animation loop) ─────────────────────────────────────

// Spinner runs a ticker loop that calls fn on each tick until Stop() is called.
// fn is called outside the spinner's own lock — it is responsible for its own
// synchronisation. This avoids lock ordering issues with parent structs.
type Spinner struct {
	done  chan struct{}
	close sync.Once
}
