package config

import (
	"fuckyourcli/ui"
)

// EditStatusBarHook shows the status-bar hook editor. The provider is selected
// first (the default applies to every provider without an override), then its
// hook is edited and persisted. Back in the hook editor returns to the provider
// selector; Back there returns to the caller.
// Returns true when something changed.
func EditStatusBarHook() bool {
	changed := false
	for {
		keys := StatusProviderKeys()
		options := make([]string, 0, len(keys)+1)
		for _, k := range keys {
			if k == "" {
				options = append(options, "All providers (default)")
			} else {
				options = append(options, k)
			}
		}
		options = append(options, "← Back")

		idx := ui.SelectOption("Status Bar Hook — select provider", options)
		if idx < 0 || idx >= len(keys) {
			return changed // "← Back"
		}
		key := keys[idx]

		h := StatusHookFor(key)
		title := "all providers"
		if key != "" {
			title = key
		}

		values, ok := ui.StatusHookSettings(title, ui.StatusHookValues{
			Enabled:        h.Enabled,
			IncludeSecrets: h.IncludeSecrets,
			Command:        h.Command,
			Interval:       h.IntervalSeconds,
			RefreshOnTurn:  h.RefreshOnTurn,
		})
		if !ok {
			// Discarded: back to the provider selector.
			continue
		}

		next := StatusBarHook{
			Enabled:         values.Enabled,
			Command:         values.Command,
			IntervalSeconds: values.Interval,
			RefreshOnTurn:   values.RefreshOnTurn,
			IncludeSecrets:  values.IncludeSecrets,
		}
		if h != next {
			changed = true
		}
		if key == "" {
			SetStatusBarHook(next)
		} else {
			SetStatusBarHookForProvider(key, next)
		}
		Save()
	}
}
