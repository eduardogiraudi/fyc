package main

import (
	"fmt"
	"sort"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/providers"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

func init() {
	// Sub-agents build their providers through the same path as the chat and
	// the compactor (newProvider), so a profile cannot drift from the
	// credentials, base URL and reasoning rules of its provider entry.
	tools.SubagentProviderFactory = func(cfg config.Config, choice config.CompactorChoice, systemPrompt string) (types.Provider, error) {
		p, err := newProvider(cfg, choice, nil)
		if err != nil {
			return nil, err
		}
		if g, ok := p.(*providers.GeminiProvider); ok {
			g.SystemInstruction = systemPrompt
		}
		return p, nil
	}
}

// withSubagentProfiles lists the configured profiles in the spawn_subagents
// tool definition, so the main agent knows which names it may pass and when to
// use each. Other tools, and spawn_subagents without profiles, are returned
// unchanged. The definition is copied: the shared tool list is never mutated.
func withSubagentProfiles(t types.Tool) types.Tool {
	if t.Function.Name != "spawn_subagents" {
		return t
	}
	cfg := config.Get()
	if len(cfg.SubagentProfiles) == 0 {
		return t
	}
	names := make([]string, 0, len(cfg.SubagentProfiles))
	for name := range cfg.SubagentProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("Optional name of a sub-agent profile (selects the model for this agent). Available:")
	for _, name := range names {
		sb.WriteString(fmt.Sprintf("\n- %s", name))
		if d := strings.TrimSpace(cfg.SubagentProfiles[name].Description); d != "" {
			sb.WriteString(": " + d)
		}
	}
	if def := strings.TrimSpace(cfg.SubagentDefaultProfile); def != "" {
		sb.WriteString("\nOmit to use the default profile: " + def + ".")
	} else {
		sb.WriteString("\nOmit to use the same model as the main agent.")
	}

	agents, ok := t.Function.Parameters.Properties["agents"]
	if !ok || agents.Items == nil {
		return t
	}
	items := *agents.Items
	props := make(map[string]types.ToolProperty, len(items.Properties))
	for k, v := range items.Properties {
		props[k] = v
	}
	prop := props["profile"]
	prop.Description = sb.String()
	props["profile"] = prop
	items.Properties = props
	agents.Items = &items
	params := t.Function.Parameters
	outer := make(map[string]types.ToolProperty, len(params.Properties))
	for k, v := range params.Properties {
		outer[k] = v
	}
	outer["agents"] = agents
	params.Properties = outer
	t.Function.Parameters = params
	return t
}
