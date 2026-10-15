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
