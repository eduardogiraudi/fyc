package types

import "testing"

func TestToolResultFailed(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain error", "error: path argument required", true},
		{"error with word", "error writing tmp: disk full", true},
		{"mcp error", "MCP error: server not connected", true},
		{"lsp error", "lsp error: file argument required", true},
		{"copy error", "copy error: no such file", true},
		{"subagent error", "subagent error: no active provider found", true},
		{"failed to start", "failed to start sandboxed command: fork/exec", true},
		{"command failed", "command failed: exit status 1", true},
		{"leading blank line", "\n\nerror: boom", true},
		{"empty result", "", false},
		{"success", "patched file main.go", false},
		{"json payload", "{\n  \"ok\": true\n}", false},
		{"failure only on a later line", "build ok\nfailed to compile foo\n", false},
		{"sentence containing a word error", "the file error: nope", false},
		{"context snapshot warning", "[context_snapshot warning] no symbols found in x.go", false},
		{"update cancelled", "error: update cancelled by the user", true},
		{"update cancelled with suggestion", "error: update cancelled by the user: use write instead", true},
		{"write denied", "error: write denied by the user", true},
		{"copy denied", "error: copy denied", true},
		{"mcp tool denied", "error: MCP tool call denied", true},
		{"source missing", "error: source does not exist: no such file", true},
		{"uncancelled success", "updated", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolResultFailed(tc.text); got != tc.want {
				t.Fatalf("ToolResultFailed(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
