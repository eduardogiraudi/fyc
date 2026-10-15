package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/ui"
)

// systemPromptHelp is the English help block shown in the System Prompt
// submenu. It documents the substitution contract.
const systemPromptHelp = `Only environment variables are substituted — no shell invoked.
Use ${VAR} to inject a dynamic value, e.g. "Today is ${FYC_TODAY}".
Built-in variables: ${FYC_TODAY}, ${FYC_CWD}, ${FYC_OS}, ${FYC_SHELL}, ${FYC_PREFERENCES}.
Any other ${VAR} is read from the environment; if unset it becomes an empty string.`

// EditSystemPrompt opens the System Prompt submenu: edit the persona in
// $EDITOR, preview the rendered result, or reset to the built-in default.
//
// print receives every user-facing line; pass nil to print to stdout (the
// config-menu context). In chat, pass a writer bound to the output sink so the
// UI is not corrupted by direct stdout writes.
func EditSystemPrompt(print func(string)) {
	if print == nil {
		print = func(s string) { fmt.Println(s) }
	}
	for {
		current := "built-in default"
		if !SystemPromptIsDefault() {
			current = "custom (" + SystemPromptPath() + ")"
		}
		title := ui.SelectorHeader.Render("System Prompt (persona)") + "\n" +
			ui.InfoStyle.Render("  Active: "+current) + "\n\n" +
			ui.HintStyle.Render("  "+strings.ReplaceAll(systemPromptHelp, "\n", "\n  "))

		idx := ui.SelectOptionWithStyledTitle(title, []string{
			"Edit in " + editorDisplayName(),
			"Preview rendered prompt",
			"Reset to default",
			"← Back",
		})

		switch idx {
		case 0:
			editSystemPromptInEditor(print)
		case 1:
			previewSystemPrompt(print)
		case 2:
			resetSystemPromptInteractive(print)
		case 3:
			return
		case ui.SelectorCanceled:
			return // q / Ctrl+C: leave the System Prompt menu
		}
	}
}

// editSystemPromptInEditor opens the template file in the user's editor,
// seeding it with the built-in default the first time.
func editSystemPromptInEditor(print func(string)) {
	path := SystemPromptPath()
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, []byte(DefaultSystemPrompt), 0600); err != nil {
			print(ui.InfoStyle.Render("Could not create " + path + ": " + err.Error()))
			return
		}
	}

	editor := resolveEditor()
	if editor == "" {
		print(ui.InfoStyle.Render("No editor found. Set $EDITOR (or $VISUAL) and try again."))
		return
	}

	fields := strings.Fields(editor)
	args := append(append([]string{}, fields[1:]...), path)
	cmd := exec.Command(fields[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	ui.PauseForExternalPrompt()
	err := cmd.Run()
	ui.ResumeExternalPrompt()

	if err != nil {
		print(ui.InfoStyle.Render("Editor exited with an error: " + err.Error()))
		return
	}
	if SystemPromptIsDefault() {
		print(ui.OkStyle.Render("System prompt reset to the built-in default."))
	} else {
		print(ui.OkStyle.Render("System prompt saved: " + path))
	}
}

// previewSystemPrompt prints the effective, rendered system prompt.
func previewSystemPrompt(print func(string)) {
	render := RenderSystemPrompt(SystemPromptTemplate(), builtinSystemPromptVars())
	print(ui.TitleStyle.Render("Rendered system prompt (persona)"))
	print(ui.SeparatorStyle.Render(strings.Repeat("─", 40)))
	for _, line := range strings.Split(render, "\n") {
		print(line)
	}
	print(ui.SeparatorStyle.Render(strings.Repeat("─", 40)))
}

// resetSystemPromptInteractive asks for confirmation and restores the default.
func resetSystemPromptInteractive(print func(string)) {
	if SystemPromptIsDefault() {
		print(ui.InfoStyle.Render("Already using the built-in default."))
		return
	}
	if ui.SelectOption("Reset the system prompt to the built-in default?", []string{
		"No, keep current", "Yes, reset",
	}) != 1 {
		return
	}
	if err := ResetSystemPrompt(); err != nil {
		print(ui.InfoStyle.Render("Could not reset: " + err.Error()))
		return
	}
	print(ui.OkStyle.Render("System prompt reset to the built-in default."))
}

// builtinSystemPromptVars computes the FYC_* values for the current process.
func builtinSystemPromptVars() map[string]string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return map[string]string{
		"FYC_TODAY":       time.Now().Format("Monday, January 2, 2006"),
		"FYC_CWD":         cwd,
		"FYC_OS":          runtime.GOOS,
		"FYC_SHELL":       ShellNote(runtime.GOOS),
		"FYC_PREFERENCES": loadPreferences(),
	}
}

// loadPreferences mirrors the preferences file used by the chat session.
func loadPreferences() string {
	data, err := os.ReadFile(filepath.Join(store.ConfigDir(), "PREFERENCES.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveEditor picks the editor command, honouring FYC_EDITOR/$VISUAL/$EDITOR
// and falling back to the first available of nano/vim/vi.
func resolveEditor() string {
	for _, name := range []string{"FYC_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	for _, candidate := range []string{"nano", "vim", "vi"} {
		if p, err := exec.LookPath(candidate); err == nil {
			return p
		}
	}
	return ""
}

// editorDisplayName returns a short name for the editor that will actually be
// launched (e.g. "nano", "code"), so the menu can name it instead of showing
// a literal "$EDITOR". It falls back to "$EDITOR" only when nothing resolves.
func editorDisplayName() string {
	editor := resolveEditor()
	if fields := strings.Fields(editor); len(fields) > 0 {
		return filepath.Base(fields[0])
	}
	return "$EDITOR"
}
