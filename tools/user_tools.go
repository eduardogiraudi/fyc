package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/crawl"
	"fuckyourcli/store"
)

// executeUpdateUserPreferencesTool handles the "update_user_preferences" case.
func executeUpdateUserPreferencesTool(ctx context.Context, args map[string]any) string {
	oldStr, ok := args["old_str"].(string)
	if !ok {
		return "error: old_str argument required"
	}
	newStr, ok := args["new_str"].(string)
	if !ok {
		return "error: new_str argument required"
	}

	prefDir := store.ConfigDir()
	prefPath := filepath.Join(prefDir, "PREFERENCES.txt")

	if err := store.EnsureDir(prefDir, store.DirPerm); err != nil {
		return fmt.Sprintf("error creating dir: %v", err)
	}

	oldContent := ""
	if data, err := os.ReadFile(prefPath); err == nil {
		oldContent = string(data)
	}

	var updated string

	if strings.TrimSpace(oldContent) == "" {
		updated = newStr
	} else {
		if oldStr == "" {
			return "error: old_str cannot be empty when preferences already exist"
		}

		if !strings.Contains(oldContent, oldStr) {
			return "error: old_str not found in preferences"
		}

		count := strings.Count(oldContent, oldStr)
		if count > 1 {
			return fmt.Sprintf(
				"error: old_str matches %d occurrences, make it more specific",
				count,
			)
		}

		updated = strings.Replace(oldContent, oldStr, newStr, 1)
	}

	safeAbsPath := prefPath

	allowed, suggestion := askUpdatePermission(ctx, safeAbsPath, oldContent, updated)
	if !allowed {
		if suggestion != "" {
			return "error: update cancelled by the user: " + suggestion
		}
		return "error: update cancelled by the user"
	}
	_ = suggestion

	tmp := prefPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0644); err != nil {
		return fmt.Sprintf("error writing tmp: %v", err)
	}

	if err := os.Rename(tmp, prefPath); err != nil {
		return fmt.Sprintf("error rename: %v", err)
	}

	return "preferences updated"
}

// executeReadUserPreferencesTool handles the "read_user_preferences" case.
func executeReadUserPreferencesTool() string {
	prefPath := filepath.Join(store.ConfigDir(), "PREFERENCES.txt")

	data, err := os.ReadFile(prefPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return fmt.Sprintf("error reading preferences: %v", err)
	}

	return string(data)
}

// executeQuestionaryTool handles the "questionary" case.
// Note: uses questionaryMu (global sync.Mutex in tools.go).
// executeQuestionaryTool handles the "questionary" case. Every question is
// routed through the Interactor so the tool behaves identically in the TUI and
// in headless server mode (where interactive input is unavailable and the tool
// is also hidden from the model).
// Note: uses questionaryMu (global sync.Mutex in tools.go).
func executeQuestionaryTool(ctx context.Context, args map[string]any) string {
	questionaryMu.Lock()
	defer questionaryMu.Unlock()

	questionsRaw, ok := args["questions"].([]interface{})
	if !ok {
		return "error: questions argument must be an array"
	}
	if len(questionsRaw) > 10 {
		return "error: maximum 10 questions allowed"
	}

	questions := make([]Question, 0, len(questionsRaw))
	for i, qRaw := range questionsRaw {
		q, ok := qRaw.(map[string]interface{})
		if !ok {
			return fmt.Sprintf("error: question %d is not a valid object", i)
		}
		question, _ := q["question"].(string)
		if question == "" {
			return fmt.Sprintf("error: question %d missing question text", i)
		}
		optionsRaw, _ := q["options"].([]interface{})
		allowInput, _ := q["allow_input"].(bool)
		var options []string
		for _, o := range optionsRaw {
			if s, ok := o.(string); ok {
				options = append(options, s)
			}
		}
		if len(options) == 0 && !allowInput {
			return fmt.Sprintf("error: question %d has no options and allow_input is false", i)
		}
		questions = append(questions, Question{Question: question, Options: options, AllowInput: allowInput})
	}

	resp, err := interact(ctx, InteractionRequest{Kind: KindQuestion, Questions: questions})
	if err != nil {
		return "error: " + err.Error()
	}
	answers := resp.Answers
	if len(answers) != len(questions) {
		return "error: interactive input not available in server mode"
	}

	var sb strings.Builder
	sb.WriteString("[")
	for i, a := range answers {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf("%q", a))
	}
	sb.WriteString("]")
	return sb.String()
}

// executeWebSearchTool handles the "web_search" case.
func executeWebSearchTool(args map[string]any) string {
	// The tool is hidden from the model when no browser is available; refuse a
	// direct call too (e.g. a stale tool list or a sub-agent).
	if reason := WebSearchBlockReason(); reason != "" {
		return "error: web search is unavailable: " + reason
	}

	input, ok := args["input"].(string)
	if !ok {
		return "error: input argument required"
	}

	engine, _ := args["engine"].(string)
	if engine == "" {
		engine = "startpage"
	}

	find, _ := args["find"].(string)

	result, err := crawl.CrawlPagedOpts(input, engine, crawl.PagedOptions{
		Page:     intArg(args, "page"),
		MaxChars: intArg(args, "max_chars"),
		Find:     find,
	})
	if err != nil {
		return fmt.Sprintf("error executing crawler: %v", err)
	}
	return result
}

// executeSkillsDiscoveryTool handles the "skills_discovery" case.
func executeSkillsDiscoveryTool(restrictedRoot string) string {
	skills := DiscoverSkills(restrictedRoot)
	if len(skills) == 0 {
		return "No skills found."
	}
	var sb strings.Builder
	for name, desc := range skills {
		if desc != "" {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
		} else {
			sb.WriteString(fmt.Sprintf("- %s\n", name))
		}
	}
	return sb.String()
}

// executeReadSkillTool handles the "read_skill" case.
func executeReadSkillTool(args map[string]any, restrictedRoot string) string {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return "error: name argument required"
	}
	fileArg, _ := args["file"].(string)
	if fileArg == "" {
		fileArg = "SKILL.md"
	}

	skillDir := findSkillDir(name, restrictedRoot)
	if skillDir == "" {
		return fmt.Sprintf("error: skill '%s' not found", name)
	}

	// Path traversal protection: clean the path and reject absolute or escaping paths.
	relPath := filepath.Clean(fileArg)
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "..") {
		return "error: path traversal detected"
	}

	skillRoot, err := os.OpenRoot(skillDir)
	if err != nil {
		return fmt.Sprintf("error opening skill: %v", err)
	}
	defer skillRoot.Close()

	f, err := skillRoot.Open(relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("error: file '%s' not found in skill '%s'", fileArg, name)
		}
		return fmt.Sprintf("error reading skill file: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("error reading skill file: %v", err)
	}
	return string(data)
}
