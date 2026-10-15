package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBannerVariants(t *testing.T) {
	compact := stripControlSequences(Banner(true))
	full := stripControlSequences(Banner(false))
	if !strings.Contains(compact, "█▀▀ █ █ █▀▀") || !strings.Contains(compact, "█▀▀ ▀▄▀ █") {
		t.Fatalf("compact banner is not FYC:\n%s", compact)
	}
	if !strings.Contains(full, "█▀▀ █ █ █▀▀ █ █ █ █ █▀▀█ █ █ █▀▀█ █▀▀ █   █") {
		t.Fatalf("full banner changed:\n%s", full)
	}
	if lipgloss.Width(compact) >= lipgloss.Width(full) {
		t.Fatal("compact banner must be narrower")
	}
}

func TestCompactStartupInfoOnRight(t *testing.T) {
	info := []string{
		ValueStyle.Render("vtest • OS: linux • sha: abc123"),
		"cwd: /workspace", "provider: test • model: model",
		"reasoning: default • temperature: 0.1", "compaction: off",
		"context: AGENTS.md", "mode: auto  delay: 1.0s", "notice: test",
	}
	output := stripControlSequences(formatStartupHeader(true, info, 100))
	lines := strings.Split(output, "\n")
	infoColumn := lipgloss.Width(Banner(true)) + 2
	for i, text := range info {
		text = stripControlSequences(text)
		index := strings.Index(lines[i], text)
		if index < 0 || DisplayWidth(lines[i][:index]) != infoColumn {
			t.Fatalf("info %q is not beside banner at column %d:\n%s", text, infoColumn, output)
		}
	}
	if lines[len(lines)-1] != strings.Repeat("─", 100) {
		t.Fatalf("separator must follow compact header:\n%s", output)
	}
}

func TestFullStartupLayoutUnchanged(t *testing.T) {
	info := []string{"vtest", "cwd: /workspace", "provider: test", "compaction: off"}
	want := TitleStyle.Render(Banner(false)) + "\n" + SeparatorStyle.Render(strings.Repeat("─", 80)) + "\n" + strings.Join(info, "\n")
	if got := formatStartupHeader(false, info, 80); got != want {
		t.Fatalf("full startup layout changed:\n%s", got)
	}
}

func TestCompactStartupFitsTerminal(t *testing.T) {
	info := []string{ValueStyle.Render("provider: " + strings.Repeat("long-model ", 12)), "cwd: /" + strings.Repeat("界", 50)}
	for _, width := range []int{10, 18, 20, 40, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			output := formatStartupHeader(true, info, width)
			for _, line := range strings.Split(output, "\n") {
				if got := DisplayWidth(line); got > width {
					t.Fatalf("line width = %d > %d: %q", got, width, line)
				}
			}
			if width >= 20 && !strings.Contains(stripControlSequences(output), "provider:") {
				t.Fatalf("provider info lost:\n%s", output)
			}
		})
	}
}
