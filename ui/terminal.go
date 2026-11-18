package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

func AnimateSpinner(label string, done <-chan struct{}, wg *sync.WaitGroup, doneLabel string) {
	defer wg.Done()
	i := 0
	first := true
	var lastNumLines int

	for {
		select {
		case <-done:
			if !first {
				if lastNumLines > 1 {
					CursorUp(lastNumLines - 1)
				}
				fmt.Print("\r")
			}
			ClearToEOS()
			if doneLabel != "" {
				fmt.Printf("\033[38;5;208;1m☉\033[0m %s\n", doneLabel)
			}
			return
		default:
			width, _, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil || width <= 0 {
				width = 80
			}

			// Calculate how many lines the spinner + label occupy
			// spinnerFrames have a visible length of 7 characters
			spinnerVisibleLen := 7
			fullText := strings.Repeat(" ", spinnerVisibleLen) + " " + label
			numLines := CountLines(fullText, width)

			if first {
				//fmt.Print("\n")
			} else {
				if lastNumLines > 1 {
					CursorUp(lastNumLines - 1)
				}
				fmt.Print("\r")
				ClearToEOS()
			}

			fmt.Printf("%s %s", spinnerFrames[i%len(spinnerFrames)], label)
			lastNumLines = numLines
			time.Sleep(100 * time.Millisecond)
			i++
			first = false
		}
	}
}

var spinnerFrames = []string{
	"\033[38;2;252;132;3m☉\033[0m", // The sun (focus / start of processing)
	"\033[38;2;252;132;3m◇\033[0m", // Empty diamond
	"\033[38;2;252;132;3m❖\033[0m", // Diamond with internal detail
	"\033[38;2;252;132;3m◆\033[0m", // Full diamond (maximum intensity)
	"\033[38;2;252;132;3m❖\033[0m", // Return to diamond with detail
	"\033[38;2;252;132;3m◇\033[0m", // Return to empty diamond
}

// SpinnerFrame returns the spinner frame for a given index.
func SpinnerFrame(frame int) string {
	return spinnerFrames[frame%len(spinnerFrames)]
}

func FormatDuration(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	if seconds < 60 {
		unit := "seconds"
		if seconds == 1 {
			unit = "second"
		}
		return fmt.Sprintf("%d %s", seconds, unit)
	}
	minutes := seconds / 60
	remainingSeconds := seconds % 60
	minuteUnit := "minutes"
	if minutes == 1 {
		minuteUnit = "minute"
	}
	if remainingSeconds == 0 {
		return fmt.Sprintf("%d %s", minutes, minuteUnit)
	}
	secondUnit := "seconds"
	if remainingSeconds == 1 {
		secondUnit = "second"
	}
	return fmt.Sprintf("%d %s %d %s", minutes, minuteUnit, remainingSeconds, secondUnit)
}

// PrintToolOutput renders a tool call for the non-Tea fallback. It shares the
// line builder with FormatToolOutput, so both paths are clamped to the
// terminal width.
func PrintToolOutput(label, res string) {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}
	for _, line := range toolOutputLines(label, res, width, 2) {
		fmt.Println(line)
	}
}

type Suggestion struct {
	Name        string
	Description string
}

func CountLines(s string, termWidth int) int {
	if termWidth < 20 {
		termWidth = 20
	}
	lines := strings.Split(s, "\n")
	total := 0
	for _, l := range lines {
		runeLen := len([]rune(l))
		if termWidth > 0 && runeLen > termWidth {
			total += (runeLen + termWidth - 1) / termWidth
		} else {
			total++
		}
	}
	return total
}

/*func RenderContextBar(used int, max int) string {
	if max == 0 {
		return ""
	}

	width := 30
	ratio := float64(used) / float64(max)

	if ratio > 1 {
		ratio = 1
	}

	filled := int(ratio * float64(width))
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)

	return fmt.Sprintf("%s  %s / %s %s",
		BarStyle.Render("["+bar+"]"),
		OkStyle.Render(fmt.Sprintf("%d", used)),
		OkStyle.Render(fmt.Sprintf("%d", max)),
		InfoStyle.Render("ctx"),
	)
}*/

// RenderProviderInfo returns a formatted string showing current provider, model and mode (auto/manual)
func RenderProviderInfo(providerInfo, temperature, mode string) string {
	return fmt.Sprintf("%s %s %s",
		InfoStyle.Render("[Provider: ")+TitleStyle.Render(providerInfo)+InfoStyle.Render("]"),
		InfoStyle.Render("[Temp: ")+TitleStyle.Render(temperature)+InfoStyle.Render("]"),
		ModeSegment(mode),
	)
}

// ModeSegment renders the trailing "[Mode: x]" part of RenderProviderInfo. The
// alt-screen chat draws it on its own: in that mode it is a button.
func ModeSegment(mode string) string {
	return InfoStyle.Render("[Mode: ") + TitleStyle.Render(mode) + InfoStyle.Render("]")
}

// SplitModeSegment separates the mode badge from a status line built by
// RenderProviderInfo: prefix is everything in front of it, mode the bare label
// and badge the badge text without styling (a hover repaints it on its own).
// Splitting by display columns keeps the prefix byte-exact even when a provider
// name contains wide runes.
func SplitModeSegment(line string) (prefix, mode, badge string, ok bool) {
	const marker = "[Mode: "
	plain := ansi.Strip(line)
	idx := strings.Index(plain, marker)
	if idx < 0 || !strings.HasSuffix(plain, "]") {
		return line, "", "", false
	}
	mode = plain[idx+len(marker) : len(plain)-1]
	if mode == "" {
		return line, "", "", false
	}
	badge = plain[idx:]
	col := ansi.StringWidth(plain[:idx])
	return strings.TrimRight(ansi.Cut(line, 0, col), " "), mode, badge, true
}

// TipsBlock returns the start-up hints as one rendered block: the caller draws
// it itself (the alt-screen chat transcript seeds it into the view).
func TipsBlock(altScreen bool) string {
	modeHint := "ctrl+o or /auto"
	if altScreen {
		modeHint = "ctrl+o, /auto or a click on the mode badge"
	}
	return TipStyle.Render("Type /help for commands  •  exit to quit  •  enter to send  •  /sub on to enable subagents spawning") + "\n" +
		TipStyle.Render(modeHint+" to enable/disable auto mode")
}

// StartupHeaderBlock returns the start-up banner rendered for the current
// terminal width. The caller draws it itself (the alt-screen chat transcript
// seeds it into the view).
func StartupHeaderBlock(compact bool, info []string) string {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}
	return formatStartupHeader(compact, info, width)
}

// StartupHeaderBlockAtWidth regenerates the layout from the original details.
func StartupHeaderBlockAtWidth(compact bool, info []string, width int) string {
	return ClampBlock(formatStartupHeader(compact, info, max(1, width)), max(1, width))
}

// TerminalWidth returns the terminal width in cells, falling back to 80 when the
// size cannot be read (not a TTY).
func TerminalWidth() int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return 80
}

func RenderSeparator() {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}
	fmt.Print(SeparatorStyle.Render(strings.Repeat("─", width)) + "\n")
}
