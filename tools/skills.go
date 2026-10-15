package tools

import (
	"os"
	"path/filepath"
	"strings"

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
	return result
}

// findSkillDir returns the base directory of a skill, searching global and project locations.
// Project-level skills take priority over global ones.
func findSkillDir(name, restrictedRoot string) string {
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
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(store.ConfigDir(), "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(restrictedRoot, ".claude", "skills"),
		filepath.Join(restrictedRoot, ".fyc", "skills"),
		filepath.Join(restrictedRoot, ".agents", "skills"),
	}
}
