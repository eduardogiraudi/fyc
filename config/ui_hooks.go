package config

import "fuckyourcli/ui"

// EditHooks shows the tool-hooks editor and persists the changes.
// Returns true when something changed.
func EditHooks() bool {
	h := config.Hooks
	preOn, preCmd, postOn, postCmd, ok := ui.HooksSettings(
		h.PreTool.Enabled, h.PreTool.Command,
		h.PostTool.Enabled, h.PostTool.Command,
	)
	if !ok {
		return false
	}

	changed := h.PreTool.Enabled != preOn || h.PreTool.Command != preCmd ||
		h.PostTool.Enabled != postOn || h.PostTool.Command != postCmd

	SetPreToolHook(preOn, preCmd)
	SetPostToolHook(postOn, postCmd)
	Save()
	return changed
}
