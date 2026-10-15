package tools

import (
	"fmt"
	"strings"
)

// ToolLabel holds the universal, human-readable messages the UI shows for a
// tool call: Present while it runs (spinner), Done on success and Failed on
// error. The wording never leaks internal tool or implementation names, so the
// same phrases work across providers and inside the sub-agent panels.
type ToolLabel struct {
	Present string
	Done    string
	Failed  string
}

func (l ToolLabel) with(spinner, done, failed string) ToolLabel {
	return ToolLabel{Present: spinner, Done: done, Failed: failed}
}

// nameOr returns v, or fallback when v is blank. It keeps the labels
// grammatical when an argument is missing or not yet known, so the UI never
// renders a dangling "patching file ...".
func nameOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// target composes "<kind> <value>" for a label, falling back to the given
// definite phrase when the value is missing: target("main.go", "file", "the file")
// yields "file main.go", while an empty value yields "the file".
func target(value, kind, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if kind == "" {
		return value
	}
	return kind + " " + value
}

// DescribeTool returns the UI labels for a tool call, keyed on the tool name
// and (where useful) on its arguments.
func DescribeTool(name string, args map[string]interface{}) ToolLabel {
	str := func(key string) string {
		s, _ := args[key].(string)
		return s
	}

	switch name {
	case "get_anchors":
		file := nameOr(str("path"), "the file")
		return ToolLabel{}.with(
			fmt.Sprintf("preparing %s for patching...", file),
			fmt.Sprintf("ready to patch %s", file),
			fmt.Sprintf("failed to prepare %s for patching", file),
		)

	case "read":
		file := target(str("path"), "file", "the file")
		return ToolLabel{}.with(
			fmt.Sprintf("reading %s...", file),
			fmt.Sprintf("read %s", file),
			fmt.Sprintf("failed to read %s", file),
		)

	case "read_media":
		image := target(str("path"), "image", "the image")
		return ToolLabel{}.with(
			fmt.Sprintf("reading %s...", image),
			fmt.Sprintf("read %s", image),
			fmt.Sprintf("failed to read %s", image),
		)

	case "replace_anchors", "update_file":
		file := target(str("path"), "file", "the file")
		return ToolLabel{}.with(
			fmt.Sprintf("patching %s...", file),
			fmt.Sprintf("patched %s", file),
			fmt.Sprintf("failed to patch %s", file),
		)

	case "write":
		file := target(str("path"), "file", "the file")
		return ToolLabel{}.with(
			fmt.Sprintf("writing %s...", file),
			fmt.Sprintf("wrote %s", file),
			fmt.Sprintf("failed to write %s", file),
		)

	case "delete":
		path := nameOr(str("path"), "the file")
		return ToolLabel{}.with(
			fmt.Sprintf("deleting %s...", path),
			fmt.Sprintf("deleted %s", path),
			fmt.Sprintf("failed to delete %s", path),
		)

	case "make_dir":
		dir := target(str("path"), "directory", "the directory")
		return ToolLabel{}.with(
			fmt.Sprintf("creating %s...", dir),
			fmt.Sprintf("created %s", dir),
			fmt.Sprintf("failed to create %s", dir),
		)

	case "copy":
		source, dest := nameOr(str("source"), "the source"), nameOr(str("destination"), "the destination")
		return ToolLabel{}.with(
			fmt.Sprintf("copying %s to %s...", source, dest),
			fmt.Sprintf("copied %s to %s", source, dest),
			fmt.Sprintf("failed to copy %s to %s", source, dest),
		)

	case "rename":
		path, newName := nameOr(str("path"), "the file"), nameOr(str("new_name"), "a new name")
		return ToolLabel{}.with(
			fmt.Sprintf("renaming %s to %s...", path, newName),
			fmt.Sprintf("renamed %s to %s", path, newName),
			fmt.Sprintf("failed to rename %s to %s", path, newName),
		)

	case "move":
		source, dest := nameOr(str("source"), "the source"), nameOr(str("destination"), "the destination")
		return ToolLabel{}.with(
			fmt.Sprintf("moving %s to %s...", source, dest),
			fmt.Sprintf("moved %s to %s", source, dest),
			fmt.Sprintf("failed to move %s to %s", source, dest),
		)

	case "ls":
		path := str("path")
		if path == "" {
			path = "."
		}
		return ToolLabel{}.with(
			fmt.Sprintf("listing directory %s...", path),
			fmt.Sprintf("listed directory %s", path),
			fmt.Sprintf("failed to list directory %s", path),
		)

	case "grep":
		pattern, path := nameOr(str("pattern"), "the pattern"), str("path")
		if path == "" {
			path = "."
		}
		return ToolLabel{}.with(
			fmt.Sprintf("searching for '%s' in %s...", pattern, path),
			fmt.Sprintf("searched for '%s' in %s", pattern, path),
			fmt.Sprintf("failed to search for '%s' in %s", pattern, path),
		)

	case "context_snapshot":
		return ToolLabel{}.with(
			"scanning the codebase...",
			"scanned the codebase",
			"failed to scan the codebase",
		)

	case "web_search":
		input := str("input")
		if input == "" {
			return ToolLabel{}.with(
				"searching the web...",
				"searched the web",
				"failed to search the web",
			)
		}
		if strings.HasPrefix(input, "http") {
			return ToolLabel{}.with(
				fmt.Sprintf("reading %s...", input),
				fmt.Sprintf("read %s", input),
				fmt.Sprintf("failed to read %s", input),
			)
		}
		return ToolLabel{}.with(
			fmt.Sprintf("searching the web for '%s'...", input),
			fmt.Sprintf("searched the web for '%s'", input),
			fmt.Sprintf("failed to search the web for '%s'", input),
		)

	case "sh":
		cmd := str("command")
		if strings.TrimSpace(cmd) == "" {
			return ToolLabel{}.with(
				"running the command...",
				"ran the command",
				"failed to run the command",
			)
		}
		// Collapse the command to a single line and cap it: a multi-line command
		// (heredoc, python -c) must not turn the transcript label into several
		// rows, and cutting on bytes could split a UTF-8 rune. The full command is
		// shown by the permission prompt's "See details" viewer.
		cmd = truncateOneLine(cmd, 60)
		return ToolLabel{}.with(
			fmt.Sprintf("running '%s'...", cmd),
			fmt.Sprintf("ran '%s'", cmd),
			fmt.Sprintf("failed to run '%s'", cmd),
		)

	case "kill":
		if pid, ok := args["pid"].(float64); ok && pid > 0 {
			p := fmt.Sprintf("%d", int(pid))
			return ToolLabel{}.with(
				fmt.Sprintf("stopping process %s...", p),
				fmt.Sprintf("stopped process %s", p),
				fmt.Sprintf("failed to stop process %s", p),
			)
		}
		return ToolLabel{}.with(
			"stopping process...",
			"stopped process",
			"failed to stop process",
		)

	case "sniff":
		return ToolLabel{}.with(
			"reading process log...",
			"read process log",
			"failed to read process log",
		)

	case "skills_discovery":
		return ToolLabel{}.with(
			"listing skills...",
			"listed skills",
			"failed to list skills",
		)

	case "read_skill":
		if skill := str("name"); strings.TrimSpace(skill) != "" {
			return ToolLabel{}.with(
				fmt.Sprintf("reading skill '%s'...", skill),
				fmt.Sprintf("read skill '%s'", skill),
				fmt.Sprintf("failed to read skill '%s'", skill),
			)
		}
		return ToolLabel{}.with(
			"reading the skill...",
			"read the skill",
			"failed to read the skill",
		)

	case "read_user_preferences":
		return ToolLabel{}.with(
			"reading preferences...",
			"read preferences",
			"failed to read preferences",
		)

	case "update_user_preferences":
		return ToolLabel{}.with(
			"updating preferences...",
			"updated preferences",
			"failed to update preferences",
		)

	case "list_mcp_resources":
		return ToolLabel{}.with(
			"listing MCP resources...",
			"listed MCP resources",
			"failed to list MCP resources",
		)

	case "list_mcp_resource_templates":
		return ToolLabel{}.with(
			"listing MCP resource templates...",
			"listed MCP resource templates",
			"failed to list MCP resource templates",
		)

	case "read_mcp_resource":
		resource := target(str("uri"), "MCP resource", "the resource")
		return ToolLabel{}.with(
			fmt.Sprintf("reading %s...", resource),
			fmt.Sprintf("read %s", resource),
			fmt.Sprintf("failed to read %s", resource),
		)

	case "lsp_active_servers":
		return ToolLabel{}.with(
			"starting language servers...",
			"listed active language servers",
			"failed to list active language servers",
		)
	case "lsp_hover":
		return ToolLabel{}.with(
			"querying LSP hover...",
			"queried LSP hover",
			"failed to query LSP hover",
		)
	case "lsp_definition":
		return ToolLabel{}.with(
			"querying LSP definition...",
			"queried LSP definition",
			"failed to query LSP definition",
		)
	case "lsp_references":
		return ToolLabel{}.with(
			"querying LSP references...",
			"queried LSP references",
			"failed to query LSP references",
		)
	case "lsp_diagnostics":
		return ToolLabel{}.with(
			"querying LSP diagnostics...",
			"queried LSP diagnostics",
			"failed to query LSP diagnostics",
		)
	case "lsp_workspace_symbols":
		return ToolLabel{}.with(
			"querying LSP workspace symbols...",
			"queried LSP workspace symbols",
			"failed to query LSP workspace symbols",
		)

	case "git_status":
		return ToolLabel{}.with(
			"checking git status...",
			"checked git status",
			"failed to check git status",
		)
	case "git_diff":
		return ToolLabel{}.with(
			"reading the git diff...",
			"read the git diff",
			"failed to read the git diff",
		)
	case "git_log":
		return ToolLabel{}.with(
			"reading the git history...",
			"read the git history",
			"failed to read the git history",
		)
	case "git_show":
		rev := str("rev")
		if rev == "" {
			rev = "HEAD"
		}
		return ToolLabel{}.with(
			fmt.Sprintf("showing git object %s...", rev),
			fmt.Sprintf("showed git object %s", rev),
			fmt.Sprintf("failed to show git object %s", rev),
		)
	case "git_blame":
		path := nameOr(str("path"), "the file")
		return ToolLabel{}.with(
			fmt.Sprintf("blaming %s...", path),
			fmt.Sprintf("blamed %s", path),
			fmt.Sprintf("failed to blame %s", path),
		)
	case "git_ls_files":
		return ToolLabel{}.with(
			"listing git files...",
			"listed git files",
			"failed to list git files",
		)
	case "git_config":
		key := str("key")
		if key != "" {
			return ToolLabel{}.with(
				fmt.Sprintf("reading git config %s...", key),
				fmt.Sprintf("read git config %s", key),
				fmt.Sprintf("failed to read git config %s", key),
			)
		}
		return ToolLabel{}.with(
			"reading git config...",
			"read git config",
			"failed to read git config",
		)
	case "git_add":
		return ToolLabel{}.with(
			"staging changes...",
			"staged changes",
			"failed to stage changes",
		)
	case "git_commit":
		return ToolLabel{}.with(
			"committing...",
			"committed",
			"failed to commit",
		)
	case "git_switch":
		branch := nameOr(str("branch"), "the branch")
		return ToolLabel{}.with(
			fmt.Sprintf("switching to %s...", branch),
			fmt.Sprintf("switched to %s", branch),
			fmt.Sprintf("failed to switch to %s", branch),
		)
	case "git_restore":
		return ToolLabel{}.with(
			"restoring files...",
			"restored files",
			"failed to restore files",
		)
	case "git_stash":
		action := str("action")
		if action == "" {
			action = "list"
		}
		return ToolLabel{}.with(
			fmt.Sprintf("git stash %s...", action),
			fmt.Sprintf("git stash %s done", action),
			fmt.Sprintf("failed to run git stash %s", action),
		)
	case "git_init":
		return ToolLabel{}.with(
			"initializing a git repository...",
			"initialized a git repository",
			"failed to initialize a git repository",
		)
	case "git_branch":
		action := str("action")
		if action == "" {
			action = "list"
		}
		return ToolLabel{}.with(
			fmt.Sprintf("git branch %s...", action),
			fmt.Sprintf("git branch %s done", action),
			fmt.Sprintf("failed to run git branch %s", action),
		)
	case "git_remote":
		action := str("action")
		if action == "" {
			action = "list"
		}
		return ToolLabel{}.with(
			fmt.Sprintf("git remote %s...", action),
			fmt.Sprintf("git remote %s done", action),
			fmt.Sprintf("failed to run git remote %s", action),
		)
	case "git_fetch":
		return ToolLabel{}.with(
			"fetching from the remote...",
			"fetched from the remote",
			"failed to fetch from the remote",
		)
	case "git_pull":
		return ToolLabel{}.with(
			"pulling the remote changes...",
			"pulled the remote changes",
			"failed to pull the remote changes",
		)
	case "git_push":
		return ToolLabel{}.with(
			"pushing to the remote...",
			"pushed to the remote",
			"failed to push to the remote",
		)
	case "git_clone":
		url := nameOr(str("url"), "the repository")
		return ToolLabel{}.with(
			fmt.Sprintf("cloning %s...", url),
			fmt.Sprintf("cloned %s", url),
			fmt.Sprintf("failed to clone %s", url),
		)
	case "git_reset":
		mode := str("mode")
		if mode == "" {
			mode = "mixed"
		}
		return ToolLabel{}.with(
			fmt.Sprintf("git reset --%s...", mode),
			fmt.Sprintf("git reset --%s done", mode),
			fmt.Sprintf("failed to run git reset --%s", mode),
		)
	case "git_merge":
		branch := nameOr(str("branch"), "the branch")
		return ToolLabel{}.with(
			fmt.Sprintf("merging %s...", branch),
			fmt.Sprintf("merged %s", branch),
			fmt.Sprintf("failed to merge %s", branch),
		)
	case "git_rebase":
		upstream := nameOr(str("upstream"), "the upstream branch")
		return ToolLabel{}.with(
			fmt.Sprintf("rebasing onto %s...", upstream),
			fmt.Sprintf("rebased onto %s", upstream),
			fmt.Sprintf("failed to rebase onto %s", upstream),
		)
	case "git_cherry_pick":
		return ToolLabel{}.with(
			"cherry-picking...",
			"cherry-picked",
			"failed to cherry-pick",
		)
	case "git_revert":
		return ToolLabel{}.with(
			"reverting...",
			"reverted",
			"failed to revert",
		)
	case "git_tag":
		action := str("action")
		if action == "" {
			action = "list"
		}
		return ToolLabel{}.with(
			fmt.Sprintf("git tag %s...", action),
			fmt.Sprintf("git tag %s done", action),
			fmt.Sprintf("failed to run git tag %s", action),
		)

	case "spawn_subagents":
		agents, _ := args["agents"].([]interface{})
		count := len(agents)
		if count == 1 {
			return ToolLabel{}.with(
				"spawning sub-agent...",
				"spawned 1 sub-agent",
				"failed to spawn the sub-agent",
			)
		}
		return ToolLabel{}.with(
			fmt.Sprintf("spawning %d sub-agents...", count),
			fmt.Sprintf("spawned %d sub-agents", count),
			fmt.Sprintf("failed to spawn %d sub-agents", count),
		)

	case "questionary":
		return ToolLabel{}.with(
			"asking the user...",
			"asked the user",
			"failed to ask the user",
		)

	case "computer":
		action := str("action")
		switch action {
		case "screenshot":
			return ToolLabel{}.with("capturing the screen...", "captured the screen", "failed to capture the screen")
		case "move":
			return ToolLabel{}.with("moving the mouse...", "moved the mouse", "failed to move the mouse")
		case "click", "double_click", "right_click":
			return ToolLabel{}.with("clicking...", "clicked", "failed to click")
		case "drag":
			return ToolLabel{}.with("dragging...", "dragged", "failed to drag")
		case "scroll":
			return ToolLabel{}.with("scrolling...", "scrolled", "failed to scroll")
		case "type":
			return ToolLabel{}.with("typing text...", "typed text", "failed to type text")
		case "key":
			keys := nameOr(str("keys"), "the key combination")
			return ToolLabel{}.with(
				fmt.Sprintf("pressing %s...", keys),
				fmt.Sprintf("pressed %s", keys),
				fmt.Sprintf("failed to press %s", keys),
			)
		default:
			return ToolLabel{}.with("controlling the computer...", "controlled the computer", "failed to control the computer")
		}

	default:
		return ToolLabel{}.with(
			fmt.Sprintf("running %s...", name),
			fmt.Sprintf("finished %s", name),
			fmt.Sprintf("failed: %s", name),
		)
	}
}
