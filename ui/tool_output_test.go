package ui

import (
	"strings"
	"testing"
)

func TestFormatToolOutputErrorState(t *testing.T) {
	ok := FormatToolOutput("patched file a.go", "applied 3 lines", 80, 2)
	if !strings.Contains(ok, "☉") {
		t.Fatalf("success output lost its spinner icon: %q", ok)
	}
	if strings.Contains(ok, "✗") {
		t.Fatalf("success output was rendered as a failure: %q", ok)
	}

	bad := FormatToolOutput("failed to patch file a.go", "error: hunk not found", 80, 2)
	if !strings.Contains(bad, "✗") {
		t.Fatalf("failure output is missing the error icon: %q", bad)
	}
	if strings.Contains(bad, "☉") {
		t.Fatalf("failure output still uses the success icon: %q", bad)
	}
}

func TestFormatToolOutputNoOutput(t *testing.T) {
	out := FormatToolOutput("ran 'true'", "   \n", 80, 2)
	if !strings.Contains(out, "(no output)") {
		t.Fatalf("empty tool result should show a placeholder: %q", out)
	}
}

func TestFormatToolOutputAnchorEditSummary(t *testing.T) {
	for _, res := range []string{
		"updated (+3 -2 lines)\nnew anchors:\nopaque§source\nother§more",
		"updated (+0 -2 lines)\nno new anchors (lines deleted)",
	} {
		out := FormatToolOutput("patched file a.go", res, 80, ToolOutputRows)
		if !strings.Contains(out, "lines") || !strings.Contains(out, DeleteStyle.Render("-2")) {
			t.Fatalf("missing styled edit summary: %q", out)
		}
		if strings.Contains(res, "+3") && !strings.Contains(out, AddStyle.Render("+3")) {
			t.Fatalf("missing styled additions: %q", out)
		}
		for _, hidden := range []string{"anchors", "opaque", "source", "more lines hidden"} {
			if strings.Contains(out, hidden) {
				t.Fatalf("summary leaked %q: %q", hidden, out)
			}
		}
	}
}

func TestFormatToolOutputDoesNotSummarizeFailedEdit(t *testing.T) {
	res := "error: write failed\nupdated (+3 -2 lines)"
	out := FormatToolOutput("failed to patch file a.go", res, 80, ToolOutputRows)
	if !strings.Contains(out, "✗") || !strings.Contains(out, "write failed") {
		t.Fatalf("failure was hidden by edit summary: %q", out)
	}
}
