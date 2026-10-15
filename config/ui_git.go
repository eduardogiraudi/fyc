package config

import (
	"strings"

	"fuckyourcli/ui"
)

// EditGitPermissions shows the editor for the git permission model and the
// default remote. It returns true when the configuration changed (and was
// saved).
//
// The permission tiers are opt-in: every mutating git tool starts disabled in
// the tool list, and even when it is enabled the matching tier must be granted
// here (or with "Allow always" in a permission prompt) before it can run.
func EditGitPermissions() bool {
	cfg := Get()

	labels := []string{
		"Allow local writes (add, commit, switch, restore, stash, init, branch, remote)",
		"Allow network operations (fetch, pull, push, clone)",
		"Allow destructive operations (reset, merge, rebase, cherry-pick, revert, tag)",
		"Allow force push (--force-with-lease only, never plain --force)",
		"← Done",
	}
	states := []bool{
		cfg.Git.AllowWrite,
		cfg.Git.AllowNetwork,
		cfg.Git.AllowDestructive,
		cfg.Git.AllowForcePush,
		true,
	}

	newStates := ui.MultiSelectOption("Git Permissions", labels, states)
	if len(newStates) == 0 {
		return false
	}

	changed := false
	setBool := func(current, next bool, apply func(bool)) {
		if current == next {
			return
		}
		apply(next)
		changed = true
	}
	if len(newStates) >= 4 {
		setBool(cfg.Git.AllowWrite, newStates[0], SetGitAllowWrite)
		setBool(cfg.Git.AllowNetwork, newStates[1], SetGitAllowNetwork)
		setBool(cfg.Git.AllowDestructive, newStates[2], SetGitAllowDestructive)
		setBool(cfg.Git.AllowForcePush, newStates[3], SetGitAllowForcePush)
	}

	remote := cfg.Git.EffectiveDefaultRemote()
	if ui.SelectOption("Default git remote (currently "+remote+")", []string{"Keep " + remote, "Change it"}) == 1 {
		input := strings.TrimSpace(ui.InputText("Default remote name (e.g. origin):"))
		if input != "" && !strings.ContainsAny(input, " \t\n\r") {
			if input != remote {
				SetGitDefaultRemote(input)
				changed = true
			}
		}
	}

	if changed {
		Save()
	}
	return changed
}
