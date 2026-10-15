// Package releasenotes builds reviewable Keep a Changelog sections without Git I/O.
package releasenotes

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	conventional   = regexp.MustCompile(`^([a-z]+)(?:\(([^()\r\n]+)\))?(!)?: (\S.*)$`)
	breakingFooter = regexp.MustCompile(`(?m)^BREAKING(?: CHANGE|-CHANGE):\s*(.*)$`)
	releaseHeading = regexp.MustCompile(`^## \[([^\]]+)\](?: - (\d{4}-\d{2}-\d{2}))?$`)
	groups         = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}
)

func ValidVersion(version string) bool {
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return false
	}
	if match[4] != "" {
		for _, part := range strings.Split(match[4], ".") {
			if strings.Trim(part, "0123456789") == "" && len(part) > 1 && part[0] == '0' {
				return false
			}
		}
	}
	return true
}

type Commit struct {
	Hash    string
	Message string
}

// ParseLog accepts git log --format='%H%x00%B%x00', including multiline bodies.
func ParseLog(data string) ([]Commit, error) {
	fields := strings.Split(data, "\x00")
	if strings.TrimSpace(fields[len(fields)-1]) != "" || len(fields)%2 != 1 {
		return nil, fmt.Errorf("invalid NUL-delimited commit log")
	}
	var commits []Commit
	for i := 0; i < len(fields)-1; i += 2 {
		hash := strings.TrimSpace(fields[i])
		if len(hash) < 7 || strings.Trim(hash, "0123456789abcdefABCDEF") != "" {
			return nil, fmt.Errorf("invalid commit hash %q", hash)
		}
		commits = append(commits, Commit{hash, strings.TrimSpace(fields[i+1])})
	}
	return commits, nil
}

// Split only unindented typed headers; prose and fenced examples stay in the body.
func splitEntries(message string) []string {
	var entries []string
	var current []string
	fence := ""
	flush := func() {
		if entry := strings.TrimSpace(strings.Join(current, "\n")); entry != "" {
			entries = append(entries, entry)
		}
		current = nil
	}
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			current = append(current, line)
			continue
		}
		if fence == "" && conventional.FindStringSubmatch(line) != nil {
			flush()
		}
		current = append(current, line)
	}
	flush()
	return entries
}

func Draft(version string, date time.Time, commits []Commit) (string, error) {
	if !ValidVersion(version) {
		return "", fmt.Errorf("version must be SemVer without a leading v")
	}
	entries := make(map[string][]string)
	for _, commit := range commits {
		hash := commit.Hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		for _, message := range splitEntries(commit.Message) {
			group, text := classifyEntry(message)
			if group != "" {
				entries[group] = append(entries[group], fmt.Sprintf("- %s (`%s`).", text, hash))
			}
		}
	}
	if len(entries) == 0 {
		entries["Changed"] = []string{"- TODO: summarize this release (no user-facing Conventional Commits found)."}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "## [%s] - %s\n", version, date.Format("2006-01-02"))
	for _, group := range groups {
		if len(entries[group]) > 0 {
			fmt.Fprintf(&out, "\n### %s\n\n%s\n", group, strings.Join(entries[group], "\n"))
		}
	}
	return out.String(), nil
}

func classifyEntry(message string) (string, string) {
	parts := strings.SplitN(message, "\n", 2)
	subject := strings.TrimSpace(parts[0])
	body := ""
	if len(parts) == 2 {
		body = parts[1]
	}
	match := conventional.FindStringSubmatch(subject)
	group, text := "Changed", "TODO: rewrite or remove legacy commit: "+subject
	if match != nil {
		text = match[4]
		switch match[1] {
		case "feat":
			group = "Added"
		case "fix":
			group = "Fixed"
		case "refactor", "perf":
			group = "Changed"
		case "revert":
			text = "Revert: " + text
		case "docs", "style", "test", "build", "ci", "chore":
			group = ""
		default:
			text = "TODO: review custom commit type: " + subject
		}
		if match[2] != "" {
			text = match[2] + ": " + text
		}
	}
	footer := breakingFooter.FindStringSubmatch(body)
	if footer != nil || (match != nil && match[3] == "!") {
		group = "Changed"
		text = "**Breaking:** " + text
		if footer != nil && strings.TrimSpace(footer[1]) != "" {
			text += " — " + strings.TrimSpace(footer[1])
		}
	}
	return group, text
}

func Validate(version, section string) error {
	if !ValidVersion(version) {
		return fmt.Errorf("version must be SemVer without a leading v")
	}
	lines := strings.Split(strings.TrimSpace(section), "\n")
	heading := releaseHeading.FindStringSubmatch(lines[0])
	if heading == nil || heading[1] != version || heading[2] == "" {
		return fmt.Errorf("keep the heading '## [%s] - YYYY-MM-DD'", version)
	}
	if _, err := time.Parse("2006-01-02", heading[2]); err != nil {
		return fmt.Errorf("invalid release date: %w", err)
	}
	if strings.Contains(strings.ToUpper(section), "TODO:") {
		return fmt.Errorf("rewrite or remove every TODO entry before approving")
	}
	group, bullets := "", 0
	seen := make(map[string]bool)
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			return fmt.Errorf("edit only the new release section")
		}
		if strings.HasPrefix(line, "### ") {
			group = strings.TrimPrefix(line, "### ")
			valid := false
			for _, allowed := range groups {
				valid = valid || group == allowed
			}
			if !valid || seen[group] {
				return fmt.Errorf("use each standard changelog category at most once: %s", strings.Join(groups, ", "))
			}
			seen[group] = true
		}
		if strings.HasPrefix(line, "- ") && strings.TrimSpace(line[2:]) != "" {
			if group == "" {
				return fmt.Errorf("put entries under a standard changelog category")
			}
			bullets++
		}
	}
	if bullets == 0 {
		return fmt.Errorf("include at least one changelog entry")
	}
	return nil
}

// Extract returns exactly one version section, never the surrounding history.
func Extract(version, changelog string) (string, error) {
	if !ValidVersion(version) {
		return "", fmt.Errorf("version must be SemVer without a leading v")
	}
	lines := strings.Split(changelog, "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		if start >= 0 && end == len(lines) {
			end = i
		}
		match := releaseHeading.FindStringSubmatch(line)
		if match != nil && match[1] == version {
			if start >= 0 {
				return "", fmt.Errorf("duplicate changelog section for %s", version)
			}
			start = i
		}
	}
	if start < 0 {
		return "", fmt.Errorf("no changelog section for %s", version)
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n")) + "\n", nil
}

func Assemble(version, section, changelog string) (string, error) {
	if err := Validate(version, section); err != nil {
		return "", err
	}
	lines := strings.SplitAfter(changelog, "\n")
	pos := 0
	for _, line := range lines {
		trimmed := strings.TrimSuffix(line, "\n")
		if match := releaseHeading.FindStringSubmatch(trimmed); match != nil && match[1] == version {
			return "", fmt.Errorf("changelog already contains %s; remove an unreleased placeholder or choose a new version", version)
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "## [Unreleased]") {
			break
		}
		pos += len(line)
	}
	prefix := strings.TrimRight(changelog[:pos], "\n")
	if prefix == "" {
		prefix = "# Changelog"
	}
	return prefix + "\n\n" + strings.TrimSpace(section) + "\n\n" + changelog[pos:], nil
}
