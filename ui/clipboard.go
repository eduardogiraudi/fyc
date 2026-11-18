package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// clipboardTools are the native helpers tried in order: the first one present on
// PATH takes the text. They cover the desktop cases where OSC 52 is disabled.
var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
	{"clip.exe"}, // WSL
	{"termux-clipboard-set"},
}

// clipboardMu serialises the OSC 52 fallback with itself: the sequence is
// written straight to the terminal, outside the renderer's bookkeeping.
var clipboardMu sync.Mutex

// CopyToClipboard puts text on the system clipboard and reports which route
// took it: a native helper, the CLIPBOARD selection this process owns (X11), or
// the OSC 52 sequence. The order prefers whoever keeps the text alive after we
// exit, and ends with the only option left over ssh.
func CopyToClipboard(text string) (string, error) {
	if text == "" {
		return "", fmt.Errorf("nothing selected")
	}
	var firstErr error
	for _, tool := range clipboardTools {
		if _, err := exec.LookPath(tool[0]); err != nil {
			continue
		}
		cmd := exec.Command(tool[0], tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", tool[0], err)
			}
			continue
		}
		return tool[0], nil
	}
	// No helper on PATH: own the selection ourselves instead of trusting a
	// terminal sequence most Linux terminals drop (VTE implements no OSC 52).
	if err := x11ClipboardSet(text); err == nil {
		return "x11", nil
	} else if firstErr == nil && !errors.Is(err, errNoX11) {
		firstErr = err
	}
	// The renderer owns stdout, but the sequence is short and the frame is
	// repainted right after (the notice changes the status line), which repairs
	// a repaint it might have landed in the middle of.
	clipboardMu.Lock()
	_, err := os.Stdout.WriteString(ansi.SetSystemClipboard(text))
	clipboardMu.Unlock()
	if err != nil {
		if firstErr != nil {
			return "", fmt.Errorf("%w (osc52: %w)", firstErr, err)
		}
		return "", err
	}
	return "osc52", nil
}

// OSC52TerminalSupport reports whether the terminal may implement OSC 52
// writes. VTE (GNOME Terminal, Terminator, Tilix, xfce4-terminal) does not: the
// sequence is dropped without a reply, so writing it proves nothing and the
// confirmation must not claim the clipboard was set.
func OSC52TerminalSupport() bool {
	return os.Getenv("VTE_VERSION") == ""
}

// selectedVideo is the reverse-video sequence a terminal selection is drawn
// with.
const selectedVideo = "\x1b[7m"

// SelectionHighlight paints s as selected. Every style inside s ends the
// reverse video it is wrapped in, so it is turned back on after each reset:
// otherwise the highlight would stop at the first coloured fragment.
func SelectionHighlight(s string) string {
	out := strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+selectedVideo)
	out = strings.ReplaceAll(out, "\x1b[m", "\x1b[m"+selectedVideo)
	return selectedVideo + out + "\x1b[0m"
}
