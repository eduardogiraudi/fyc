package config

import (
	"fmt"
	"strings"

	"fuckyourcli/ui"
)

// Threshold adjustment bounds and step used by the CLI Settings editor.
const (
	compactionStep         = 1000
	compactionMinThreshold = 1000
	compactionMaxThreshold = 1000000
)

// manageCLISettings shows the CLI settings submenu: auto-compaction, the
// pre/post tool-execution hooks, the status-bar hook, and the system prompt.
func manageCLISettings() {
	for {
		idx := ui.SelectOption("CLI Settings", []string{
			"Auto Compaction",
			"Hooks",
			"Status Bar Hook",
			"System Prompt (persona)",
			"Browser Paths (web_search)",
			"← Back",
		})

		switch idx {
		case 0:
			manageCompactionSettings()
		case 1:
			EditHooks()
		case 2:
			EditStatusBarHook()
		case 3:
			EditSystemPrompt(nil)
		case 4:
			manageBrowserPaths()
		case 5:
			return
		case ui.SelectorCanceled:
			return // q / Ctrl+C: leave the CLI Settings menu
		}
	}
}

// manageBrowserPaths edits the explicit browser executables probed by the
// crawler before the built-in locations (config.json: browser_paths). Empty
// input keeps the current value; "-" clears the list.
func manageBrowserPaths() {
	current := strings.Join(BrowserPaths(), ", ")
	prompt := "Browser executable paths, comma-separated"
	if current != "" {
		prompt += " (current: " + current + ")"
	}
	prompt += ":"
	line := ui.InputText(prompt)
	if line == "" {
		return
	}
	if line == "-" {
		SetBrowserPaths(nil)
	} else {
		SetBrowserPaths(strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';'
		}))
	}
	Save()
	fmt.Println(ui.OkStyle.Render("Browser paths updated."))
}

// manageCompactionSettings shows the inline auto-compaction editor. The
// threshold is adjusted with the left/right arrow keys; leaving the editor
// with "Back" persists the changes.
func manageCompactionSettings() {
	enabled, midTurn, threshold, ok := ui.CLISettings(
		"Auto Compaction",
		config.CompactionEnabled,
		config.CompactionMidTurn,
		EffectiveCompactionThreshold(),
		compactionStep,
		compactionMinThreshold,
		compactionMaxThreshold,
	)
	if !ok {
		return
	}
	SetCompactionEnabled(enabled)
	SetCompactionMidTurn(midTurn)
	SetCompactionThreshold(threshold)
	Save()
}
