package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/config"
)

// agentsMDOverrideName is the highest-precedence instruction file at any level:
// it beats AGENTS.md (and the fallbacks) inside the same directory.
const agentsMDOverrideName = "AGENTS.override.md"

// AgentsDoc is one resolved instruction file: its path relative to the
// repository root, its content (possibly truncated to fit the budget) and
// whether it was truncated.
type AgentsDoc struct {
	Path      string
	Content   string
	Truncated bool
}

// LoadAgentsDocs resolves the AGENTS.md instruction chain for root using the
// configured fallback filenames and byte budget (see config/agentsmd.go). It
// returns nil when the feature is disabled or no instruction file is found.
func LoadAgentsDocs(root string) []AgentsDoc {
	if !config.AgentsMDEnabled() {
		return nil
	}
	return loadAgentsDocs(root, config.EffectiveAgentsMDFallbackNames(), config.EffectiveAgentsMDMaxBytes())
}

// LoadProjectContext returns the rendered <project_context> block for root and a
// short human-readable summary ("" when no instructions were found). It is the
// single entry point used by the system prompt assembly and the CLI status line.
func LoadProjectContext(root string) (rendered string, summary string) {
	docs := LoadAgentsDocs(root)
	if len(docs) == 0 {
		return "", ""
	}
	total := 0
	names := make([]string, 0, len(docs))
	for _, d := range docs {
		total += len(d.Content)
		names = append(names, d.Path)
	}
	label := fmt.Sprintf("%d files", len(docs))
	if len(docs) == 1 {
		label = "1 file"
	}
	return RenderAgentsDocs(docs), fmt.Sprintf("%s (%s, %s)", strings.Join(names, ", "), label, humanBytes(int64(total)))
}

// loadAgentsDocs is the testable core: it walks the directory chain from the
// repository root down to root, picking at most one instruction file per level
// (AGENTS.override.md > AGENTS.md > fallbacks) and honouring the byte budget.
func loadAgentsDocs(root string, fallbacks []string, maxBytes int) []AgentsDoc {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	top := agentsMDRepoRoot(abs)
	budget := maxBytes
	var docs []AgentsDoc
	for _, dir := range agentsMDChainDirs(abs) {
		name, content, ok := resolveAgentsDoc(dir, fallbacks)
		if !ok {
			continue
		}
		if budget <= 0 {
			break // no room left for further levels
		}
		rel, relErr := filepath.Rel(top, filepath.Join(dir, name))
		if relErr != nil {
			continue
		}
		doc := AgentsDoc{Path: filepath.ToSlash(rel)}
		if len(content) > budget {
			doc.Content = content[:budget]
			doc.Truncated = true
			budget = 0
		} else {
			doc.Content = content
			budget -= len(content)
		}
		docs = append(docs, doc)
		if budget <= 0 {
			break
		}
	}
	return docs
}

// resolveAgentsDoc returns the instruction file to use for a single directory:
// AGENTS.override.md first, then AGENTS.md, then the fallback filenames in order.
// Empty files are skipped (the next candidate is tried).
func resolveAgentsDoc(dir string, fallbacks []string) (name, content string, ok bool) {
	candidates := make([]string, 0, len(fallbacks)+2)
	candidates = append(candidates, agentsMDOverrideName, "AGENTS.md")
	candidates = append(candidates, fallbacks...)
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, candidate))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			continue // empty files are silently skipped
		}
		return candidate, string(data), true
	}
	return "", "", false
}

// agentsMDChainDirs returns the directories to scan, from the repository root
// down to (and including) root. When no VCS marker is found, only root is used,
// so parent directories outside the project are never injected.
func agentsMDChainDirs(root string) []string {
	top := agentsMDRepoRoot(root)
	rel, err := filepath.Rel(top, root)
	if err != nil {
		return []string{root}
	}
	dirs := []string{top}
	cur := top
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		dirs = append(dirs, cur)
	}
	return dirs
}

// agentsMDRepoRoot walks up from root looking for a VCS marker (.git or .hg).
// The first ancestor carrying one is the top of the instruction chain; when none
// exists, root itself is returned.
func agentsMDRepoRoot(root string) string {
	dir := root
	for {
		if hasVCSMarker(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return root
		}
		dir = parent
	}
}

// hasVCSMarker reports whether dir contains a .git or .hg entry.
func hasVCSMarker(dir string) bool {
	for _, marker := range []string{".git", ".hg"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// RenderAgentsDocs formats the resolved chain into the <project_context> block
// injected into the system prompt. It returns "" when docs is empty so callers
// can leave the prompt unchanged.
func RenderAgentsDocs(docs []AgentsDoc) string {
	if len(docs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<project_context>\n")
	b.WriteString("Repository instructions (AGENTS.md). Context only: the user's prompt and the <rules> above take precedence.\n")
	for _, d := range docs {
		b.WriteString("\n# ")
		b.WriteString(d.Path)
		if d.Truncated {
			b.WriteString(" (truncated)")
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(d.Content, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("</project_context>")
	return b.String()
}
