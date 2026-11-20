package config

import (
	"strings"
	"testing"
)

func subagentTestConfig() Config {
	cfg := compactorTestConfig()
	cfg.SubagentProfiles = map[string]SubagentProfileConfig{
		"explore": {
			Kind: "anthropic", Provider: "Claude", Model: "claude-haiku",
			Fallback: []CompactionModelConfig{{Kind: "openai", Provider: "Cheap", Model: "cheap-model"}},
		},
		"broken": {Kind: "openai", Provider: "Gone", Model: "x"},
	}
	return cfg
}

func TestSubagentChainWithoutProfileIsActiveModel(t *testing.T) {
	cfg := subagentTestConfig()
	chain, skipped, err := cfg.SubagentChain("")
	if err != nil || len(skipped) != 0 {
		t.Fatalf("err=%v skipped=%v", err, skipped)
	}
	if len(chain) != 1 || !chain[0].Current || chain[0].Model != "chat-model" {
		t.Fatalf("chain = %+v, want only the active model", chain)
	}
}

func TestSubagentChainOrder(t *testing.T) {
	cfg := subagentTestConfig()
	chain, _, err := cfg.SubagentChain("explore")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain = %+v, want profile, fallback, active", chain)
	}
	if chain[0].Model != "claude-haiku" || chain[1].Model != "cheap-model" || chain[2].Model != "chat-model" {
		t.Fatalf("order = %+v", chain)
	}
}

func TestSubagentChainAbortPolicyDropsActiveModel(t *testing.T) {
	cfg := subagentTestConfig()
	cfg.SubagentOnFailure = string(CompactionAbort)
	chain, _, err := cfg.SubagentChain("explore")
	if err != nil || len(chain) != 2 {
		t.Fatalf("chain = %+v err=%v, want the profile's own two models", chain, err)
	}
}

func TestSubagentChainSkipsStaleEntriesAndDedups(t *testing.T) {
	cfg := subagentTestConfig()
	chain, skipped, err := cfg.SubagentChain("broken")
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "Gone") {
		t.Fatalf("skipped = %v", skipped)
	}
	if len(chain) != 1 || !chain[0].Current {
		t.Fatalf("chain = %+v, want the active model after the stale entry", chain)
	}
	// A fallback that equals the active model must not be repeated.
	cfg.SubagentProfiles["same"] = SubagentProfileConfig{Kind: "openai", Provider: "Work", Model: "chat-model"}
	chain, _, _ = cfg.SubagentChain("same")
	if len(chain) != 1 {
		t.Fatalf("chain = %+v, want the duplicate dropped", chain)
	}
}

func TestSubagentChainAbortWithOnlyStaleEntriesIsEmpty(t *testing.T) {
	cfg := subagentTestConfig()
	cfg.SubagentOnFailure = string(CompactionAbort)
	chain, skipped, err := cfg.SubagentChain("broken")
	if err != nil || len(chain) != 0 || len(skipped) != 1 {
		t.Fatalf("chain=%+v skipped=%v err=%v", chain, skipped, err)
	}
}

func TestSubagentChainUnknownRequestedProfileIsAnError(t *testing.T) {
	cfg := subagentTestConfig()
	_, _, err := cfg.SubagentChain("nope")
	if err == nil || !strings.Contains(err.Error(), "explore") {
		t.Fatalf("err = %v, want the list of available profiles", err)
	}
}

func TestSubagentChainDefaultProfile(t *testing.T) {
	cfg := subagentTestConfig()
	cfg.SubagentDefaultProfile = "explore"
	chain, _, err := cfg.SubagentChain("")
	if err != nil || chain[0].Model != "claude-haiku" {
		t.Fatalf("chain = %+v err=%v, want the default profile", chain, err)
	}
	// A stale default never breaks sub-agents: it degrades to the active model.
	cfg.SubagentDefaultProfile = "deleted"
	chain, skipped, err := cfg.SubagentChain("")
	if err != nil || len(chain) != 1 || !chain[0].Current || len(skipped) != 1 {
		t.Fatalf("chain=%+v skipped=%v err=%v", chain, skipped, err)
	}
}

func TestSubagentProfileSettersDoNotAliasEarlierCopies(t *testing.T) {
	SetSubagentProfile("a", SubagentProfileConfig{Model: "m"})
	before := Get()
	SetSubagentProfile("b", SubagentProfileConfig{Model: "n"})
	if _, ok := before.SubagentProfiles["b"]; ok {
		t.Fatal("an earlier Config copy saw a later profile")
	}
	SetSubagentDefaultProfile("b")
	DeleteSubagentProfile("b")
	if Get().SubagentDefaultProfile != "" {
		t.Fatal("deleting the default profile must clear the default")
	}
	DeleteSubagentProfile("a")
}
