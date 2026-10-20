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
			"Compaction Model",
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
			manageCompactorSettings()
		case 2:
			EditHooks()
		case 3:
			EditStatusBarHook()
		case 4:
			EditSystemPrompt(nil)
		case 5:
			manageBrowserPaths()
		case 6:
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

// manageCompactorSettings edits the model that writes the compaction summary
// and what a failed compactor request does (config.json: compaction_model and
// compaction_on_failure).
func manageCompactorSettings() {
	for {
		cfg := Get()
		idx := ui.SelectOptionWithStyledTitle(
			ui.SelectorHeader.Render("▸  Compaction Model")+"\n"+
				ui.InfoStyle.Render("  Model: "+cfg.CompactorLabel()+"\n"+
					"  On API failure: "+failurePolicyLabel(cfg.CompactionFailurePolicy())),
			[]string{"Select the compaction model", "Change the failure policy", "← Back"},
		)
		switch idx {
		case 0:
			selectCompactionModel()
		case 1:
			selectCompactionFailurePolicy()
		default:
			return
		}
	}
}

// selectCompactionModel offers "current" plus every configured provider and its
// models. The choice is pinned here and never applied to the active provider:
// applyModelForRef would rewrite the provider entry the chat is using.
func selectCompactionModel() {
	labels, refs := buildProviderList()
	if len(labels) == 0 {
		fmt.Println(ui.WarnBox.Render("No providers configured yet. Add one first from 'Add New Provider'."))
		return
	}
	labels = append(labels, "← Back")
	idx := ui.SelectOptionWithStyledTitle(
		ui.SelectorHeader.Render("▸  Compaction Model")+"\n"+
			ui.InfoStyle.Render("  Current: "+Get().CompactorLabel()),
		append([]string{"Active model (current, follows /models)"}, labels...),
	)
	if idx <= 0 || idx > len(refs) {
		if idx == 0 {
			SetCompactionModel(CompactionModelConfig{})
			Save()
			fmt.Println(ui.OkStyle.Render("Compaction model: current (the active model at compaction time)"))
		}
		return
	}
	ref := refs[idx-1]
	models, err := fetchModelsForRef(ref)
	if err != nil || len(models) == 0 {
		if defaults := defaultModelsForRef(ref); len(defaults) > 0 {
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. Using defaults."))
			models = defaults
		} else {
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. You can enter manually."))
			manual := ui.InputText("Compaction model name:")
			if manual == "" {
				return
			}
			models = []string{manual}
		}
	}
	mIdx := ui.SelectOption("Select the model that writes the compaction summary", models)
	if mIdx < 0 {
		return
	}
	SetCompactionModel(compactionSelection(ref, models[mIdx]))
	Save()
	fmt.Println(ui.OkStyle.Render("Compaction model: " + Get().CompactorLabel()))
}

// selectCompactionFailurePolicy chooses what a failed compactor request does.
func selectCompactionFailurePolicy() {
	idx := ui.SelectOption("If the compaction request fails", []string{
		"Retry with the active model (configuration unchanged)",
		"Interrupt compaction (history untouched, lossless)",
		"← Back",
	})
	switch idx {
	case 0:
		SetCompactionOnFailure(CompactionFallbackCurrent)
	case 1:
		SetCompactionOnFailure(CompactionAbort)
	default:
		return
	}
	Save()
	fmt.Println(ui.OkStyle.Render("Compaction failure policy: " + failurePolicyLabel(Get().CompactionFailurePolicy())))
}

// compactionSelection pins a provider entry and a model for the compactor.
func compactionSelection(ref providerRef, model string) CompactionModelConfig {
	sel := CompactionModelConfig{Kind: string(ref.kind), Model: model}
	switch ref.kind {
	case kindOpenAI:
		sel.Provider = config.OpenAICompatibleProviders[ref.index].Name
	case kindResponses:
		sel.Provider = config.OpenAIResponsesCompatibleProviders[ref.index].Name
	case kindAnthropic:
		sel.Provider = config.AnthropicProviders[ref.index].Name
	}
	return sel
}

// failurePolicyLabel renders a policy for the menus and the banner.
func failurePolicyLabel(p CompactionFailurePolicy) string {
	if p == CompactionAbort {
		return "interrupt compaction (lossless)"
	}
	return "retry with the active model"
}
