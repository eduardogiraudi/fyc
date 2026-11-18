package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCompletedBlockBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{"single newline", "paragraph\n", 0},
		{"incomplete whitespace line", "paragraph\n  ", 0},
		{"blank line", "paragraph\n\n", len("paragraph\n\n")},
		{"CRLF", "paragraph\r\n\r\nnext", len("paragraph\r\n\r\n")},
		{"soft break", "run this\ncommand\n", 0},
		{"open fence", "```sh\nprintf hello\n\n", 0},
		{"different fence", "```sh\n~~~\n\n", 0},
		{"shorter fence", "````sh\n```\n\n", 0},
		{"fence with suffix", "```sh\n```oops\n\n", 0},
		{"closed fence", "```sh\necho hi\n```\n\nnext", len("```sh\necho hi\n```\n\n")},
		{"tilde fence", "~~~~sh\n~~~\n\n~~~~\n\nnext", len("~~~~sh\n~~~\n\n~~~~\n\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LastCompletedBlockBoundary(tc.text); got != tc.want {
				t.Fatalf("boundary = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMarkdownHasNoDecorativePadding(t *testing.T) {
	r := NewMarkdownRenderer(80)
	for _, tc := range []struct {
		name, markdown, want string
	}{
		{"paragraph", "hello world", "hello world"},
		{"inline command", "Run `go test ./...` now.", "Run go test ./... now."},
		{"code", "```sh\necho hello\n  echo indented\n```", "echo hello\n  echo indented"},
		{"unicode", "Ciao 世界 👋", "Ciao 世界 👋"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ansi.Strip(r.Render(tc.markdown))
			if strings.TrimRight(got, "\n") != tc.want {
				t.Fatalf("render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMarkdownWrappingUsesFullWidth(t *testing.T) {
	r := NewMarkdownRenderer(20)
	text := "echo hello --verbose"
	got := ansi.Strip(r.Render("`" + text + "`"))
	if strings.TrimRight(got, "\n") != text {
		t.Fatalf("command fitting the viewport was wrapped: %q", got)
	}
	for _, line := range strings.Split(r.Render(strings.Repeat("hello ", 15)), "\n") {
		if DisplayWidth(line) > 20 {
			t.Fatalf("paragraph exceeds viewport: %q", line)
		}
		if strings.HasSuffix(ansi.Strip(line), " ") {
			t.Fatalf("paragraph retains padding: %q", line)
		}
	}
}

func TestPanelSoftBreakIsNotFlushed(t *testing.T) {
	pb := NewPanelBuffer("test", "test")
	pb.WriteText("Run this\n")
	if len(pb.segments) != 0 {
		t.Fatal("a single newline prematurely flushed the paragraph")
	}
	pb.WriteText("command.\n\n")
	if len(pb.segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(pb.segments))
	}
	got := ansi.Strip(strings.Join(pb.segments[0].Lines, "\n"))
	if got != "Run this command." {
		t.Fatalf("soft break became a hard break: %q", got)
	}
}
