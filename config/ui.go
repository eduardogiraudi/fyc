package config

import (
	"fmt"

	"fuckyourcli/ui"
)

// RunConfig runs the configuration UI.
// Returns true if the user wants to enter chat at the end.
func RunConfig() bool {
	for {
		currentInfo := GetCurrentProviderInfo()
		styledTitle := ui.SelectorHeader.Render("⚙  Configuration") + "\n" +
			ui.InfoStyle.Render("  Current: "+currentInfo)

		idx := ui.SelectOptionWithStyledTitle(styledTitle, []string{
			"Add New Provider",
			"Select / Switch Provider",
			"Manage Providers",
			"Manage MCP Servers",
			"Enable/Disable Tools",
			"Git Permissions",
			"CLI Settings",
			"Exit",
			"Start Chatting",
		})

		switch idx {
		case 0:
			if addNewProvider() {
				return true
			}
		case 1:
			if selectProvider() {
				return true
			}
		case 2:
			manageAllProviders()
		case 3:
			manageMCPServers()
		case 4:
			ToggleTools(nil)
		case 5:
			EditGitPermissions()
		case 6:
			manageCLISettings()
		case 7:
			return false
		case 8:
			return true
		case ui.SelectorCanceled:
			return false // q / Ctrl+C: leave the config menu
		}
	}
}

// FirstRunChoice is the action the user picked after the first-run wizard.
type FirstRunChoice int

const (
	// FirstRunChat starts the chat.
	FirstRunChat FirstRunChoice = iota
	// FirstRunConfig opens the full configuration menu.
	FirstRunConfig
	// FirstRunQuit exits without starting the chat.
	FirstRunQuit
)

// RunFirstTimeSetup forces the provider choice on a fresh install, then asks
// what to do next. It loops through "Add New Provider" until at least one
// provider is configured and returns the action the user chose. Both
// `fuckyourcli` and `fuckyourcli config` use it, so they behave identically.
func RunFirstTimeSetup() FirstRunChoice {
	fmt.Println(ui.SelectorHeader.Render("First run — choose a provider"))
	fmt.Println(ui.InfoStyle.Render("  No provider configured yet."))
	for NeedsSetup() {
		if !addNewProvider() {
			// "← Back" or a setup that produced nothing: offer a way out.
			if ui.SelectOption("No provider configured. Quit?", []string{"Try again", "Quit"}) != 0 {
				return FirstRunQuit
			}
		}
	}
	fmt.Println()
	switch ui.SelectOption("Setup complete — what next?", []string{"Start chatting", "Configuration", "Exit"}) {
	case 1:
		return FirstRunConfig
	case 2:
		return FirstRunQuit
	case ui.SelectorCanceled:
		return FirstRunQuit
	default:
		return FirstRunChat
	}
}
