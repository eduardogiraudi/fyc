package config

import (
	"fmt"
	"sort"
	"strings"
)

// SubagentProfileConfig is a named model preset for sub-agents. The main agent
// picks one by name in spawn_subagents; the user defines them in config.json
// (or under CLI Settings → Sub-agent Profiles).
//
// Model selection reuses the compaction selection shape: Kind/Provider/Model
// address a configured provider entry and pin a model. Fallback lists further
// selections tried, in order, when the previous one fails before producing
// output. Nothing here ever rewrites the active provider.
type SubagentProfileConfig struct {
	Kind     string `json:"kind,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Description tells the main agent when to use this profile.
	Description string `json:"description,omitempty"`
	// Fallback are tried in order after the profile's own model.
	Fallback []CompactionModelConfig `json:"fallback,omitempty"`
}

// Selection returns the profile's own model as a selection.
func (p SubagentProfileConfig) Selection() CompactionModelConfig {
	return CompactionModelConfig{Kind: p.Kind, Provider: p.Provider, Model: p.Model}
}

// SubagentFailurePolicy returns the effective policy applied after every model
// of a profile failed. An absent or unknown value keeps the default: retry with
// the model active at that moment.
func (c Config) SubagentFailurePolicy() CompactionFailurePolicy {
	switch CompactionFailurePolicy(strings.ToLower(strings.TrimSpace(c.SubagentOnFailure))) {
	case CompactionAbort:
		return CompactionAbort
	default:
		return CompactionFallbackCurrent
	}
}

// SubagentProfileNames lists the configured profile names, sorted.
func (c Config) SubagentProfileNames() []string {
	names := make([]string, 0, len(c.SubagentProfiles))
	for name := range c.SubagentProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SubagentChain is the ordered list of models a sub-agent may use.
//
// Resolution order:
//  1. the requested profile; when empty, SubagentDefaultProfile;
//  2. the profile's own model, then its Fallback entries, in order;
//  3. the model active right now, unless the policy is "abort".
//
// With no profile at all the chain is just the active model, exactly the
// behaviour before profiles existed. An entry that no longer resolves (provider
// deleted, model empty) is skipped and reported in skipped, so one stale entry
// never disables the rest of the chain. Duplicate targets are dropped: a
// fallback must not repeat a request that just failed for the same reason.
// An unknown requested profile is an error (the model made a typo); an unknown
// default profile is not (the user's file is stale): it degrades to the active
// model.
func (c Config) SubagentChain(profile string) (chain []CompactorChoice, skipped []error, err error) {
	name := strings.TrimSpace(profile)
	explicit := name != ""
	if !explicit {
		name = strings.TrimSpace(c.SubagentDefaultProfile)
	}
	add := func(choice CompactorChoice) {
		for _, existing := range chain {
			if existing.SameTarget(choice) {
				return
			}
		}
		chain = append(chain, choice)
	}
	profileInPlay := false
	if name != "" {
		p, ok := c.SubagentProfiles[name]
		switch {
		case ok:
			profileInPlay = true
			what := fmt.Sprintf("sub-agent profile %q", name)
			selections := append([]CompactionModelConfig{p.Selection()}, p.Fallback...)
			for i, sel := range selections {
				choice, rerr := c.ResolveSelection(sel, what)
				if rerr != nil {
					label := what
					if i > 0 {
						label = fmt.Sprintf("%s fallback #%d", what, i)
					}
					skipped = append(skipped, fmt.Errorf("%s: %w", label, rerr))
					continue
				}
				add(choice)
			}
		case explicit:
			return nil, nil, fmt.Errorf("unknown sub-agent profile %q (available: %s)", name, c.profileList())
		default:
			skipped = append(skipped, fmt.Errorf("default sub-agent profile %q is not defined", name))
		}
	}
	// Without a profile (none requested, no usable default) the active model is
	// the only model there is, whatever the policy. With one, the policy decides
	// whether the active model is the last resort or the sub-agent fails.
	if !profileInPlay || c.SubagentFailurePolicy() == CompactionFallbackCurrent {
		add(c.ActiveChoice())
	}
	return chain, skipped, nil
}

func (c Config) profileList() string {
	names := c.SubagentProfileNames()
	if len(names) == 0 {
		return "none configured"
	}
	return strings.Join(names, ", ")
}
