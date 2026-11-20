package config

import (
	"fmt"
	"strconv"
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
			"Sub-agent Profiles",
			"Hooks",
			"Status Bar Hook",
			"System Prompt (persona)",
			"Browser Paths (web_search)",
			fmt.Sprintf("Sandbox GPU, Linux only (current: %s)", onOff(config.Sandbox.GPUEnabled())),
			fmt.Sprintf("Max Media (current: %d)", EffectiveMaxMedia()),
			fmt.Sprintf("Chat TUI: alt screen (current: %s)", onOff(config.AltScreen)),
			"← Back",
		})

		switch idx {
		case 0:
			manageCompactionSettings()
		case 1:
			manageCompactorSettings()
		case 2:
			manageSubagentProfiles()
		case 3:
			EditHooks()
		case 4:
			EditStatusBarHook()
		case 5:
			EditSystemPrompt(nil)
		case 6:
			manageBrowserPaths()
		case 7:
			manageSandboxGPU()
		case 8:
			manageMaxMedia()
		case 9:
			manageAltScreen()
		case 10:
			return
		case ui.SelectorCanceled:
			return // q / Ctrl+C: leave the CLI Settings menu
		}
	}
}

// manageAltScreen switches the chat UI between the inline REPL (default) and the
// alt-screen full TUI (config: "alt_screen": true). The choice applies to the
// next chat start.
func manageAltScreen() {
	idx := ui.SelectOption("Chat TUI rendering", []string{
		"Inline REPL (default): output stays in the terminal scrollback",
		"Alt screen: full-screen TUI, transcript scrolled with pgup/pgdn",
		"← Back",
	})
	if idx != 0 && idx != 1 {
		return
	}
	SetAltScreen(idx == 1)
	if err := SaveE(); err != nil {
		fmt.Println(ui.ErrorStyle.Render("Could not save chat TUI setting: " + err.Error()))
		return
	}
	label := "inline REPL"
	if idx == 1 {
		label = "alt screen"
	}
	fmt.Println(ui.OkStyle.Render("Chat TUI: " + label + " (takes effect on the next chat)"))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// manageSandboxGPU toggles GPU device passthrough into the Linux `sh` sandbox
// (config: "sandbox": {"gpu": false} disables it; default is on).
func manageSandboxGPU() {
	idx := ui.SelectOption("Expose GPU devices in the sh sandbox (Linux)", []string{
		"On (default): CUDA / Vulkan / OpenCL / ROCm work inside sh",
		"Off: strictest isolation, no GPU inside sh",
		"← Back",
	})
	if idx != 0 && idx != 1 {
		return
	}
	SetSandboxGPU(idx == 0)
	if err := SaveE(); err != nil {
		fmt.Println(ui.ErrorStyle.Render("Could not save sandbox GPU setting: " + err.Error()))
		return
	}
	fmt.Println(ui.OkStyle.Render("Sandbox GPU: " + onOff(idx == 0)))
}

func manageMaxMedia() {
	for {
		line := strings.TrimSpace(ui.InputText(fmt.Sprintf(
			"Max media images per request (current: %d, default: %d):", EffectiveMaxMedia(), DefaultMaxMedia,
		)))
		if line == "" {
			return
		}
		limit, err := strconv.Atoi(line)
		if err != nil || limit <= 0 {
			fmt.Println(ui.ErrorStyle.Render("Enter a positive integer."))
			continue
		}
		SetMaxMedia(limit)
		if err := SaveE(); err != nil {
			fmt.Println(ui.ErrorStyle.Render("Could not save max media: " + err.Error()))
			return
		}
		fmt.Println(ui.OkStyle.Render(fmt.Sprintf("Max media updated: %d", EffectiveMaxMedia())))
		return
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
	mIdx := ui.SelectSearchableOption("Select the model that writes the compaction summary", models)
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
