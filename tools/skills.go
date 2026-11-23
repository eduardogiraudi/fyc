package tools

import (
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/plugins"
	"fuckyourcli/store"
)

// discoverSkills scans known locations for skill directories containing SKILL.md files.
// Returns a map of skill name → description (from YAML frontmatter).
func DiscoverSkills(restrictedRoot string) map[string]string {
	skills := map[string]string{}

	locations := getSkillLocations(restrictedRoot)

	for _, base := range locations {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillPath := filepath.Join(base, e.Name(), "SKILL.md")
			data, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			desc := extractSkillDescription(string(data))
			skills[e.Name()] = desc
		}
	}
	for _, installed := range plugins.Load(restrictedRoot) {
		for _, skill := range installed.Package.Skills {
			skills[skill.Name] = skill.Description
		}
	}
	return skills
}

// extractSkillDescription parses the "description" field from a SKILL.md YAML frontmatter.
func extractSkillDescription(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	end := strings.Index(content[3:], "---")
	if end == -1 {
		return ""
	}
	frontmatter := content[3 : end+3]
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return ""
}

// findSkillPath returns the path to a skill's SKILL.md, searching global and project locations.
// Project-level skills take priority over global ones.
func FindSkillPath(name, restrictedRoot string) string {
	result := ""
	for _, base := range getSkillLocations(restrictedRoot) {
		p := filepath.Join(base, name, "SKILL.md")
		if _, err := os.Stat(p); err == nil {
			result = p
		}
	}
	for _, installed := range plugins.Load(restrictedRoot) {
		for _, skill := range installed.Package.Skills {
			if skill.Name == name {
				return filepath.Join(installed.Entry.Path, filepath.FromSlash(skill.Path))
			}
		}
	}
	return result
}

// findSkillDir returns the base directory of a skill, searching global and project locations.
// Project-level skills take priority over global ones.
func findSkillDir(name, restrictedRoot string) string {
	if strings.Contains(name, "--") {
		if p := FindSkillPath(name, restrictedRoot); p != "" {
			return filepath.Dir(p)
		}
	}
	locations := getSkillLocations(restrictedRoot)
	for i := len(locations) - 1; i >= 0; i-- {
		p := filepath.Join(locations[i], name)
		info, err := os.Stat(p)
		if err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}

func getSkillLocations(restrictedRoot string) []string {
	return getSkillLocationsIn([]string{restrictedRoot})
}

// getSkillLocationsIn lists every skill location, global ones first and then the
// project-level directories of each allowed root: the later entries win, so the
// primary root is searched last (see findSkillDir).
func getSkillLocationsIn(roots []string) []string {
	home, _ := os.UserHomeDir()
	out := []string{
		filepath.Join(store.ConfigDir(), "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		out = append(out,
			filepath.Join(root, ".claude", "skills"),
			filepath.Join(root, ".fyc", "skills"),
			filepath.Join(root, ".agents", "skills"),
		)
	}
	return out
}

// DiscoverSkillsIn is DiscoverSkills across every allowed root.
func DiscoverSkillsIn(ws Workspace) map[string]string {
	skills := map[string]string{}
	for _, base := range getSkillLocationsIn(ws.Roots()) {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(base, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			skills[e.Name()] = extractSkillDescription(string(data))
		}
	}
	for _, root := range ws.Roots() {
		for _, installed := range plugins.Load(root) {
			for _, skill := range installed.Package.Skills {
				skills[skill.Name] = skill.Description
			}
		}
	}
	return skills
}

// FindSkillPathIn is FindSkillPath across every allowed root.
func FindSkillPathIn(ws Workspace, name string) string {
	result := ""
	for _, base := range getSkillLocationsIn(ws.Roots()) {
		p := filepath.Join(base, name, "SKILL.md")
		if _, err := os.Stat(p); err == nil {
			result = p
		}
	}
	for _, root := range ws.Roots() {
		for _, installed := range plugins.Load(root) {
			for _, skill := range installed.Package.Skills {
				if skill.Name == name {
					return filepath.Join(installed.Entry.Path, filepath.FromSlash(skill.Path))
				}
			}
		}
	}
	return result
}

// findSkillDirIn is findSkillDir across every allowed root.
func findSkillDirIn(name string, ws Workspace) string {
	if strings.Contains(name, "--") {
		if p := FindSkillPathIn(ws, name); p != "" {
			return filepath.Dir(p)
		}
	}
	locations := getSkillLocationsIn(ws.Roots())
	for i := len(locations) - 1; i >= 0; i-- {
		p := filepath.Join(locations[i], name)
		info, err := os.Stat(p)
		if err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}
