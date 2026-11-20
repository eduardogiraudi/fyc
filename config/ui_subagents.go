package config

import (
	"fmt"
	"strings"

	"fuckyourcli/ui"
)

// manageSubagentProfiles is the CLI Settings → Sub-agent Profiles menu: named
// model presets the main agent can pick per sub-agent, with an ordered fallback
// chain. Nothing here touches the active provider.
func manageSubagentProfiles() {
	for {
		cfg := Get()
		names := cfg.SubagentProfileNames()
		def := "active model"
		if cfg.SubagentDefaultProfile != "" {
			def = cfg.SubagentDefaultProfile
		}
		options := make([]string, 0, len(names)+4)
		for _, name := range names {
			options = append(options, name+"  →  "+selectionLabel(cfg, cfg.SubagentProfiles[name].Selection()))
		}
		options = append(options, "+ Add profile", "Set the default profile", "Change the failure policy", "Limits (iterations, timeout)", "← Back")
		idx := ui.SelectOptionWithStyledTitle(
			ui.SelectorHeader.Render("▸  Sub-agent Profiles")+"\n"+
				ui.InfoStyle.Render("  Default: "+def+"\n"+
					"  When every model of a profile fails: "+subagentPolicyLabel(cfg.SubagentFailurePolicy())),
			options,
		)
		switch {
		case idx >= 0 && idx < len(names):
			editSubagentProfile(names[idx])
		case idx == len(names):
			addSubagentProfile()
		case idx == len(names)+1:
			selectDefaultSubagentProfile()
		case idx == len(names)+2:
			selectSubagentFailurePolicy()
		case idx == len(names)+3:
			manageSubagentLimits()
		default:
			return
		}
	}
}

// selectionLabel renders a persisted selection for menus; an entry that no
// longer resolves says so instead of hiding the problem.
func selectionLabel(cfg Config, sel CompactionModelConfig) string {
	choice, err := cfg.ResolveSelection(sel, "profile")
	if err != nil {
		return "invalid (" + err.Error() + ")"
	}
	return choice.Label()
}

// pickModelSelection asks for a configured provider and one of its models. The
// second result is false when the user backed out or nothing is configured.
func pickModelSelection(title string) (CompactionModelConfig, bool) {
	labels, refs := buildProviderList()
	if len(labels) == 0 {
		fmt.Println(ui.WarnBox.Render("No providers configured yet. Add one first from 'Add New Provider'."))
		return CompactionModelConfig{}, false
	}
	labels = append(labels, "← Back")
	idx := ui.SelectOption(title, labels)
	if idx < 0 || idx >= len(refs) {
		return CompactionModelConfig{}, false
	}
	ref := refs[idx]
	models, err := fetchModelsForRef(ref)
	if err != nil || len(models) == 0 {
		if defaults := defaultModelsForRef(ref); len(defaults) > 0 {
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. Using defaults."))
			models = defaults
		} else {
			fmt.Println(ui.InfoStyle.Render("Could not fetch models. You can enter manually."))
			manual := strings.TrimSpace(ui.InputText("Model name:"))
			if manual == "" {
				return CompactionModelConfig{}, false
			}
			models = []string{manual}
		}
	}
	mIdx := ui.SelectSearchableOption(title, models)
	if mIdx < 0 {
		return CompactionModelConfig{}, false
	}
	return compactionSelection(ref, models[mIdx]), true
}

func addSubagentProfile() {
	name := strings.TrimSpace(ui.InputText("Profile name (e.g. explore, review):"))
	if name == "" {
		return
	}
	if strings.ContainsAny(name, " \t\n") {
		fmt.Println(ui.WarnBox.Render("Profile names cannot contain spaces."))
		return
	}
	if _, exists := Get().SubagentProfiles[name]; exists {
		fmt.Println(ui.WarnBox.Render(fmt.Sprintf("Profile %q already exists. Edit it from the list.", name)))
		return
	}
	sel, ok := pickModelSelection("Model for profile " + name)
	if !ok {
		return
	}
	desc := strings.TrimSpace(ui.InputText("Description for the main agent (when to use it, optional):"))
	SetSubagentProfile(name, SubagentProfileConfig{Kind: sel.Kind, Provider: sel.Provider, Model: sel.Model, Description: desc})
	Save()
	fmt.Println(ui.OkStyle.Render(fmt.Sprintf("Profile %s: %s", name, selectionLabel(Get(), sel))))
}

func editSubagentProfile(name string) {
	for {
		cfg := Get()
		p, ok := cfg.SubagentProfiles[name]
		if !ok {
			return
		}
		info := "  Model: " + selectionLabel(cfg, p.Selection())
		for i, fb := range p.Fallback {
			info += fmt.Sprintf("\n  Fallback %d: %s", i+1, selectionLabel(cfg, fb))
		}
		if p.Description != "" {
			info += "\n  Description: " + p.Description
		}
		idx := ui.SelectOptionWithStyledTitle(
			ui.SelectorHeader.Render("▸  Profile "+name)+"\n"+ui.InfoStyle.Render(info),
			[]string{"Change the model", "Add a fallback model", "Clear fallback models", "Edit the description", "Delete this profile", "← Back"},
		)
		switch idx {
		case 0:
			if sel, ok := pickModelSelection("Model for profile " + name); ok {
				p.Kind, p.Provider, p.Model = sel.Kind, sel.Provider, sel.Model
				SetSubagentProfile(name, p)
				Save()
			}
		case 1:
			if sel, ok := pickModelSelection("Fallback model for profile " + name); ok {
				p.Fallback = append(append([]CompactionModelConfig(nil), p.Fallback...), sel)
				SetSubagentProfile(name, p)
				Save()
			}
		case 2:
			p.Fallback = nil
			SetSubagentProfile(name, p)
			Save()
		case 3:
			p.Description = strings.TrimSpace(ui.InputText("Description for the main agent:"))
			SetSubagentProfile(name, p)
			Save()
		case 4:
			DeleteSubagentProfile(name)
			Save()
			fmt.Println(ui.OkStyle.Render("Profile " + name + " deleted"))
			return
		default:
			return
		}
	}
}

func selectDefaultSubagentProfile() {
	names := Get().SubagentProfileNames()
	options := append([]string{"None (sub-agents use the active model)"}, names...)
	options = append(options, "← Back")
	idx := ui.SelectOption("Default sub-agent profile", options)
	switch {
	case idx == 0:
		SetSubagentDefaultProfile("")
	case idx >= 1 && idx <= len(names):
		SetSubagentDefaultProfile(names[idx-1])
	default:
		return
	}
	Save()
	fmt.Println(ui.OkStyle.Render("Default sub-agent profile saved"))
}

func selectSubagentFailurePolicy() {
	idx := ui.SelectOption("If every model of a profile fails", []string{
		"Retry with the active model (configuration unchanged)",
		"Fail the sub-agent (report the error to the main agent)",
		"← Back",
	})
	switch idx {
	case 0:
		SetSubagentOnFailure(CompactionFallbackCurrent)
	case 1:
		SetSubagentOnFailure(CompactionAbort)
	default:
		return
	}
	Save()
	fmt.Println(ui.OkStyle.Render("Sub-agent failure policy: " + subagentPolicyLabel(Get().SubagentFailurePolicy())))
}

func subagentPolicyLabel(p CompactionFailurePolicy) string {
	if p == CompactionAbort {
		return "fail the sub-agent"
	}
	return "retry with the active model"
}

// Sub-agent budget steps. The limits are picked with a numeric field (◀/▶), so
// there is no list of values to keep in sync with the code and no ceiling to
// hit: a user with a hard task can always go further.
const (
	subagentIterationStep = 10
	subagentTimeoutStep   = 5 // minutes
)

// manageSubagentLimits edits the sub-agent budget with the arrow-key pickers.
// Choosing the built-in value stores nothing, so config.json only carries a
// number the user actually changed.
func manageSubagentLimits() {
	cfg := Get()
	iters, ok := ui.NumberPicker("Default iterations per sub-agent",
		cfg.SubagentDefaultIterationCount(), subagentIterationStep, 1, "iterations")
	if !ok {
		return
	}
	minutes, ok := ui.NumberPicker("Time limit per sub-agent",
		int(cfg.SubagentTimeout().Minutes()), subagentTimeoutStep, 1, "minutes")
	if !ok {
		return
	}
	if iters == DefaultSubagentIterations {
		iters = 0
	}
	timeout := minutes * 60
	if timeout == DefaultSubagentTimeoutSeconds {
		timeout = 0
	}
	SetSubagentLimits(iters, timeout)
	if err := SaveE(); err != nil {
		fmt.Println(ui.ErrorStyle.Render("Could not save sub-agent limits: " + err.Error()))
		return
	}
	next := Get()
	fmt.Println(ui.OkStyle.Render(fmt.Sprintf("Sub-agent limits: %d iterations, %s time limit",
		next.SubagentDefaultIterationCount(), next.SubagentTimeout())))
}
