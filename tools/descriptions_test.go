package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDescribeToolLabels(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]interface{}
		wantPres string
		wantDone string
		wantFail string
	}{
		{"replace_anchors", map[string]interface{}{"path": "a.go"}, "patching file a.go...", "patched file a.go", "failed to patch file a.go"},
		{"update_file", map[string]interface{}{"path": "a.go"}, "patching file a.go...", "patched file a.go", "failed to patch file a.go"},
		{"get_anchors", map[string]interface{}{"path": "a.go"}, "preparing a.go for patching...", "ready to patch a.go", "failed to prepare a.go for patching"},
		{"read", map[string]interface{}{"path": "a.go"}, "reading file a.go...", "read file a.go", "failed to read file a.go"},
		{"write", map[string]interface{}{"path": "a.go"}, "writing file a.go...", "wrote file a.go", "failed to write file a.go"},
		{"make_dir", map[string]interface{}{"path": "dir"}, "creating directory dir...", "created directory dir", "failed to create directory dir"},
		{"delete", map[string]interface{}{"path": "a.go"}, "deleting a.go...", "deleted a.go", "failed to delete a.go"},
		{"sniff", nil, "reading process log...", "read process log", "failed to read process log"},
		{"kill", map[string]interface{}{"pid": float64(42)}, "stopping process 42...", "stopped process 42", "failed to stop process 42"},
		{"ls", nil, "listing directory ....", "listed directory .", "failed to list directory ."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeTool(tc.name, tc.args)
			if got.Present != tc.wantPres || got.Done != tc.wantDone || got.Failed != tc.wantFail {
				t.Fatalf("DescribeTool(%q) = %+v, want present=%q done=%q failed=%q",
					tc.name, got, tc.wantPres, tc.wantDone, tc.wantFail)
			}
		})
	}
}

// TestDescribeToolMissingArgs guards the fallback path: when an argument is not
// known yet (or missing) the label must stay grammatical — "patching the file",
// never a dangling "patching file " nor a doubled noun "patching file the file".
func TestDescribeToolMissingArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]interface{}
		wantPres string
	}{
		{"replace_anchors", nil, "patching the file..."},
		{"read", nil, "reading the file..."},
		{"get_anchors", nil, "preparing the file for patching..."},
		{"write", nil, "writing the file..."},
		{"make_dir", nil, "creating the directory..."},
		{"read_media", nil, "reading the image..."},
		{"read_mcp_resource", nil, "reading the resource..."},
		{"sh", nil, "running the command..."},
		{"read_skill", nil, "reading the skill..."},
		{"git_blame", nil, "blaming the file..."},
		{"git_switch", nil, "switching to the branch..."},
		{"git_clone", nil, "cloning the repository..."},
		{"computer", map[string]interface{}{"action": "key"}, "pressing the key combination..."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeTool(tc.name, tc.args)
			if got.Present != tc.wantPres {
				t.Fatalf("DescribeTool(%q) present = %q, want %q", tc.name, got.Present, tc.wantPres)
			}
			if strings.Contains(got.Present, "  ") || strings.HasSuffix(got.Present, " ...") {
				t.Fatalf("DescribeTool(%q) present = %q is malformed", tc.name, got.Present)
			}
		})
	}
}

// TestDescribeToolNoJargon guards the "universal messages" contract: every tool
// known to the CLI must produce human wording, never the internal tool name or
// implementation jargon.
func TestDescribeToolNoJargon(t *testing.T) {
	names := []string{
		"get_anchors", "replace_anchors", "update_file", "write", "delete", "make_dir",
		"read", "read_media", "ls", "grep", "context_snapshot", "web_search", "sh",
		"kill", "sniff", "skills_discovery", "read_skill", "read_user_preferences",
		"update_user_preferences", "list_mcp_resources", "list_mcp_resource_templates",
		"read_mcp_resource", "copy", "rename", "move", "questionary", "spawn_subagents",
		"computer", "lsp_active_servers", "lsp_hover", "lsp_definition", "lsp_references",
		"lsp_diagnostics", "lsp_workspace_symbols",
	}
	banned := []string{"executing", "crafting", "replace_anchors", "update_file", "unified diff", "get_anchors"}

	for _, name := range names {
		lbl := DescribeTool(name, map[string]interface{}{
			"path":   "x",
			"agents": []interface{}{map[string]interface{}{}},
		})
		for _, s := range []string{lbl.Present, lbl.Done, lbl.Failed} {
			if strings.TrimSpace(s) == "" {
				t.Errorf("%s: empty label", name)
			}
			lower := strings.ToLower(s)
			for _, bad := range banned {
				if strings.Contains(lower, bad) {
					t.Errorf("%s: label %q leaks jargon %q", name, s, bad)
				}
			}
		}
	}
}

func TestDescribeToolSpawnSubagentsSingular(t *testing.T) {
	lbl := DescribeTool("spawn_subagents", map[string]interface{}{
		"agents": []interface{}{map[string]interface{}{}},
	})
	if lbl.Done != "spawned 1 sub-agent" {
		t.Fatalf("singular spawn label = %q", lbl.Done)
	}
}

// TestDescribeToolShLabelIsSingleLine guards the regression where the sh label
// was cut on bytes at an arbitrary offset: a multi-line command turned the
// transcript label into several rows (and could split a UTF-8 rune).
func TestDescribeToolShLabelIsSingleLine(t *testing.T) {
	cmd := "python3 -c \"\nimport sys\nprint(sys.version)\n\""
	lbl := DescribeTool("sh", map[string]interface{}{"command": cmd})
	for _, s := range []string{lbl.Present, lbl.Done, lbl.Failed} {
		if strings.ContainsAny(s, "\n\r") {
			t.Fatalf("label spans multiple lines: %q", s)
		}
	}
	if !strings.HasPrefix(lbl.Done, "ran 'python3 -c \" import sys") {
		t.Fatalf("unexpected done label: %q", lbl.Done)
	}

	// A long UTF-8 command must be truncated on rune boundaries.
	got := DescribeTool("sh", map[string]interface{}{"command": strings.Repeat("è", 100)}).Done
	if !utf8.ValidString(got) {
		t.Fatalf("label is not valid UTF-8: %q", got)
	}
}
