package tools

import (
	"fmt"
	"strings"
)

// gitReadOnWorkspace runs a read-only git tool across the workspace. A call
// that names a path belongs to exactly one root (the one that owns it); without
// a path every allowed root that is a repository answers, each section prefixed
// by its root so the model can tell them apart.
func gitReadOnWorkspace(ws Workspace, args map[string]any, run func(map[string]any, string) string) string {
	if namesGitPath(args) {
		return run(args, gitWorkspaceRoot(args, ws))
	}
	roots := ws.Roots()
	if len(roots) <= 1 {
		return run(args, ws.primaryRoot())
	}

	type section struct {
		root string
		text string
	}
	var sections []section
	for _, root := range roots {
		if _, ok := gitRepoRootFor(root); !ok {
			continue
		}
		sections = append(sections, section{root: root, text: run(args, root)})
	}
	if len(sections) == 0 {
		// No root is a repository: let the primary root's own error explain it.
		return run(args, ws.primaryRoot())
	}
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("## %s\n%s", s.root, s.text))
	}
	return b.String()
}

// namesGitPath reports whether a git call carries an explicit path argument.
func namesGitPath(args map[string]any) bool {
	if p, _ := args["path"].(string); strings.TrimSpace(p) != "" {
		return true
	}
	for _, p := range extractStringList(args, "paths") {
		if strings.TrimSpace(p) != "" {
			return true
		}
	}
	return false
}
