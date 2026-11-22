package main

import (
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/types"
	"fuckyourcli/ui"
)

// initInstructionFile is the file /init targets. The AGENTS.md override file is
// only reported, never written: it outranks the file we generate.
const initInstructionFile = "AGENTS.md"

// initUsage is the "/init help" text.
const initUsage = `Usage: /init [specification]

Generates or refreshes the repository instruction file AGENTS.md (the open
agents.md format) for the current workspace.

  /init                 analyse the repository and write/refresh AGENTS.md
  /init <specification> same, but the file must follow your specification
                        (sections, style, what to document or to ignore)
  /init help            show this message

The command does not write the file itself: it hands the model a constrained
task, so AGENTS.md only documents commands and structure the model verified in
the repository. The file is reloaded into the system prompt from the next turn.`

// handleInitCommand implements /init [specification]. It returns the task sent
// to the model; the file is written through the normal tools, so the user keeps
// the usual permission prompts and the edit stays undoable.
func handleInitCommand(s *ChatSession, input string) (bool, string) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "/init"))
	switch strings.ToLower(arg) {
	case "help", "-h", "--help", "?":
		printInitUsage(s.commandSink())
		return true, ""
	}
	sink := s.commandSink()
	sink.WriteText(ui.InfoStyle.Render("Analysing the repository to write " + initInstructionFile + "…"))
	sink.WriteNewline()
	return true, buildInitPrompt(s, arg)
}

// printInitUsage prints the usage block, one line per WriteText call because
// the sink renders markdown and a single block would be re-wrapped.
func printInitUsage(sink types.OutputSink) {
	for _, line := range strings.Split(initUsage, "\n") {
		sink.WriteText(ui.InfoStyle.Render(line))
		sink.WriteNewline()
	}
}

// buildInitPrompt renders the task /init hands to the model. spec is the
// optional user specification; when present it wins over the default structure.
func buildInitPrompt(s *ChatSession, spec string) string {
	root := "."
	if s != nil && s.RestrictedRoot != "" {
		root = s.RestrictedRoot
	}

	var b strings.Builder
	b.WriteString("[INIT] Create or update the repository instruction file `" + initInstructionFile + "` in `" + root + "`.\n\n")
	b.WriteString("AGENTS.md is the open format for agent instructions (agents.md): plain Markdown, no required fields, no front matter, no schema — any headings work. Agents load the nearest AGENTS.md in the directory tree, so the closest file beats a parent's, and an explicit user prompt beats both. Write it as living documentation for a new teammate who cannot ask questions.\n\n")
	b.WriteString("Procedure:\n")
	b.WriteString("1. Explore before writing: the layout, the manifests (go.mod, package.json, pyproject.toml, Cargo.toml, Makefile, …), the CI workflows, the lint/test configuration, the README and any existing AGENTS.md / CLAUDE.md / GEMINI.md.\n")
	b.WriteString("2. Document only commands that exist in this repository and that you verified in those files. Never invent commands, flags, paths or version numbers.\n")
	b.WriteString("3. Prefer the high-signal, non-obvious facts: the exact build/test/lint commands (including the quirk that makes them work here), the layout a reader cannot guess, and the invariants that are easy to break.\n")
	b.WriteString("4. Leave out what the code already says: no generic advice (\"write clean code\"), no instructions scoped to one kind of task (\"when fixing a bug, do X\"), no long code excerpts, no secrets, no personal paths, no license or CI boilerplate.\n")
	b.WriteString("5. Typical sections — adapt them, never pad them: Project overview (one or two lines), Build & test commands, Layout, Conventions & gotchas, PR/workflow. One line per bullet, imperative voice.\n")
	b.WriteString("6. Create the file with the `write` tool when it does not exist yet. When it does, read it first and extend it with `get_anchors` and `replace_anchors`, keeping every statement that is still true instead of rewriting from scratch (rejecting wholesale rewrites is the point of the anchored tools).\n")
	b.WriteString("7. Keep the whole file well under 32 KiB: the loader truncates the instruction chain above that budget, and a shorter file is read more carefully.\n")
	b.WriteString("8. A monorepo may carry one AGENTS.md per package: write a nested file only when the specification asks for that subproject, otherwise stop at the root one.\n\n")
	b.WriteString(initExistingState(root))
	if spec != "" {
		b.WriteString("The user's specification below decides the content and the shape of the file: follow it literally, keep its structure and its wording where it is already good, and let it override the default sections above.\n\n")
		b.WriteString("<specification>\n")
		b.WriteString(spec)
		b.WriteString("\n</specification>\n\n")
	}
	b.WriteString("Do not modify any other file. When the file is written, answer in at most 4 lines: what it covers and which commands you verified. The new instructions are picked up from the next turn.\n")
	return b.String()
}

// initExistingState describes the instruction files already present, so the
// prompt can ask for an update instead of an overwrite and can warn about an
// override file that would outrank the generated one.
func initExistingState(root string) string {
	var found []string
	for _, name := range []string{initInstructionFile, "AGENTS.override.md", "CLAUDE.md", "GEMINI.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err == nil && strings.TrimSpace(string(data)) != "" {
			found = append(found, name)
		}
	}

	var b strings.Builder
	for _, name := range []string{initInstructionFile, "AGENTS.override.md"} {
		for _, f := range found {
			if f == name {
				b.WriteString("`" + name + "` already exists at that level: read it, keep what is correct and only add or fix what is missing or wrong.\n")
			}
		}
	}
	var fallbacks []string
	for _, f := range found {
		if f == "CLAUDE.md" || f == "GEMINI.md" {
			fallbacks = append(fallbacks, f)
		}
	}
	if len(fallbacks) > 0 {
		b.WriteString("These compatibility files exist (" + strings.Join(fallbacks, ", ") + "): they are only consulted when no AGENTS.md is present, so writing AGENTS.md takes precedence over them. Do not delete or edit them.\n")
	}
	if b.Len() == 0 {
		b.WriteString("No instruction file exists yet at that level: create it.\n")
	}
	b.WriteString("\n")
	return b.String()
}
