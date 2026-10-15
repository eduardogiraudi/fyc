package config

import (
	"fmt"
	"fuckyourcli/providers"
	"fuckyourcli/ui"
)

// providerEditable is an interface that exposes the editable fields of a provider
// and the operations to read/update them. It allows editProvider to be
// generic instead of being duplicated for OpenAI, Responses and Anthropic.
type providerEditable interface {
	getName() string
	getBaseURL() string
	getModel() string

	setName(v string)
	setBaseURL(v string)
	setAPIKey(v string)
	setModel(v string)

	// fetchModels retrieves the available models for this provider.
	fetchModels() ([]string, error)
	// isActiveModel returns true if config.Model points to this provider/model.
	isActiveModel(currentModel string) bool
	// syncActiveModel updates config.Model if this is the active provider.
	syncActiveModel(newModel string)
}

// --- Concrete implementations ---

// openAIProviderEditor wraps config.OpenAICompatibleProviders[idx].
type openAIProviderEditor struct{ idx int }

func (e openAIProviderEditor) getName() string { return config.OpenAICompatibleProviders[e.idx].Name }
func (e openAIProviderEditor) getBaseURL() string {
	return config.OpenAICompatibleProviders[e.idx].BaseURL
}
func (e openAIProviderEditor) getModel() string { return config.OpenAICompatibleProviders[e.idx].Model }
func (e openAIProviderEditor) setName(v string) { config.OpenAICompatibleProviders[e.idx].Name = v }
func (e openAIProviderEditor) setBaseURL(v string) {
	config.OpenAICompatibleProviders[e.idx].BaseURL = v
}
func (e openAIProviderEditor) setAPIKey(v string) { config.OpenAICompatibleProviders[e.idx].APIKey = v }
func (e openAIProviderEditor) setModel(v string)  { config.OpenAICompatibleProviders[e.idx].Model = v }
func (e openAIProviderEditor) fetchModels() ([]string, error) {
	p := config.OpenAICompatibleProviders[e.idx]
	return providers.GetOpenAICompatibleModels(p.BaseURL, p.APIKey)
}
func (e openAIProviderEditor) isActiveModel(m string) bool {
	return config.Provider == ProviderOpenAICompatible && config.Model == m
}
func (e openAIProviderEditor) syncActiveModel(m string) { config.Model = m }

// responsesProviderEditor wraps config.OpenAIResponsesCompatibleProviders[idx].
type responsesProviderEditor struct{ idx int }

func (e responsesProviderEditor) getName() string {
	return config.OpenAIResponsesCompatibleProviders[e.idx].Name
}
func (e responsesProviderEditor) getBaseURL() string {
	return config.OpenAIResponsesCompatibleProviders[e.idx].BaseURL
}
func (e responsesProviderEditor) getModel() string {
	return config.OpenAIResponsesCompatibleProviders[e.idx].Model
}
func (e responsesProviderEditor) setName(v string) {
	config.OpenAIResponsesCompatibleProviders[e.idx].Name = v
}
func (e responsesProviderEditor) setBaseURL(v string) {
	config.OpenAIResponsesCompatibleProviders[e.idx].BaseURL = v
}
func (e responsesProviderEditor) setAPIKey(v string) {
	config.OpenAIResponsesCompatibleProviders[e.idx].APIKey = v
}
func (e responsesProviderEditor) setModel(v string) {
	config.OpenAIResponsesCompatibleProviders[e.idx].Model = v
}
func (e responsesProviderEditor) fetchModels() ([]string, error) {
	p := config.OpenAIResponsesCompatibleProviders[e.idx]
	return providers.GetOpenAIResponsesCompatibleModels(p.BaseURL, p.APIKey)
}
func (e responsesProviderEditor) isActiveModel(m string) bool {
	return config.Provider == ProviderOpenAIResponsesCompatible && config.Model == m
}
func (e responsesProviderEditor) syncActiveModel(m string) { config.Model = m }

// anthropicProviderEditor wraps config.AnthropicProviders[idx].
type anthropicProviderEditor struct{ idx int }

func (e anthropicProviderEditor) getName() string    { return config.AnthropicProviders[e.idx].Name }
func (e anthropicProviderEditor) getBaseURL() string { return config.AnthropicProviders[e.idx].BaseURL }
func (e anthropicProviderEditor) getModel() string   { return config.AnthropicProviders[e.idx].Model }
func (e anthropicProviderEditor) setName(v string)   { config.AnthropicProviders[e.idx].Name = v }
func (e anthropicProviderEditor) setBaseURL(v string) {
	config.AnthropicProviders[e.idx].BaseURL = v
}
func (e anthropicProviderEditor) setAPIKey(v string) { config.AnthropicProviders[e.idx].APIKey = v }
func (e anthropicProviderEditor) setModel(v string)  { config.AnthropicProviders[e.idx].Model = v }
func (e anthropicProviderEditor) fetchModels() ([]string, error) {
	p := config.AnthropicProviders[e.idx]
	return providers.GetAnthropicModels(p.BaseURL, p.APIKey)
}
func (e anthropicProviderEditor) isActiveModel(m string) bool {
	return config.Provider == ProviderAnthropic && config.Model == m
}
func (e anthropicProviderEditor) syncActiveModel(m string) { config.Model = m }

// --- Generic editor ---

// editProvider shows the edit menu for any provider that implements
// providerEditable. It replaces editProvider, editResponsesProvider, editAnthropicProvider.
func editProvider(ed providerEditable) {
	field := ui.SelectOption(
		fmt.Sprintf("Editing provider '%s'. What to change?", ed.getName()),
		[]string{"Name", "Base URL", "API Key", "Model", "← Cancel"},
	)
	switch field {
	case 0: // Name
		newName := ui.InputText(fmt.Sprintf("Current name: %s. New name:", ed.getName()))
		if newName != "" {
			ed.setName(newName)
			Save()
			fmt.Println(ui.OkStyle.Render("Name updated to " + newName))
		}

	case 1: // Base URL
		newURL := ui.InputText(fmt.Sprintf("Current base URL: %s. New URL:", ed.getBaseURL()))
		if newURL != "" {
			ed.setBaseURL(newURL)
			Save()
			fmt.Println(ui.OkStyle.Render("Base URL updated"))
		}

	case 2: // API Key
		newKey := ui.InputPassword("Enter new API key (leave empty to clear):")
		ed.setAPIKey(newKey)
		Save()
		if newKey == "" {
			fmt.Println(ui.OkStyle.Render("API key cleared"))
		} else {
			fmt.Println(ui.OkStyle.Render("API key updated"))
		}

	case 3: // Model
		updateProviderModel(ed)

	case 4: // Cancel
	}
}

// updateProviderModel handles the fetch -> manual fallback -> selection flow
// for model modification, reusable for any editor.
func updateProviderModel(ed providerEditable) {
	fmt.Println(ui.InfoStyle.Render("Fetching available models..."))
	models, err := ed.fetchModels()

	if err != nil || len(models) == 0 {
		fmt.Println(ui.InfoStyle.Render("Could not fetch models. Enter manually."))
		manual := ui.InputText("Enter model name:")
		if manual == "" {
			return
		}
		oldModel := ed.getModel()
		ed.setModel(manual)
		if ed.isActiveModel(oldModel) {
			ed.syncActiveModel(manual)
		}
		Save()
		fmt.Println(ui.OkStyle.Render("Model updated to " + manual))
		return
	}

	mIdx := ui.SelectOption("Select new model", models)
	if mIdx < 0 {
		return // dismissed (q / Ctrl+C / Esc)
	}
	chosen := models[mIdx]
	oldModel := ed.getModel()
	ed.setModel(chosen)
	if ed.isActiveModel(oldModel) {
		ed.syncActiveModel(chosen)
	}
	Save()
	fmt.Println(ui.OkStyle.Render("Model updated to " + chosen))
}

// --- Manage submenus ---

// manageAllProviders shows the submenu to choose which type of provider to manage.
func manageAllProviders() {
	idx := ui.SelectOptionWithStyledTitle(
		ui.SelectorHeader.Render("▸  Manage Providers"),
		[]string{
			"OpenAI Compatible (Chat Completions)",
			"OpenAI Responses API Compatible",
			"Anthropic Messages API",
			"← Back",
		},
	)
	switch idx {
	case 0:
		manageProviderList(
			"OpenAI Compatible",
			func() int { return len(config.OpenAICompatibleProviders) },
			func(i int) string {
				p := config.OpenAICompatibleProviders[i]
				return fmt.Sprintf("%s (%s - %s)", p.Name, p.BaseURL, p.Model)
			},
			func(i int) providerEditable { return openAIProviderEditor{i} },
			func(i int) string {
				name := config.OpenAICompatibleProviders[i].Name
				RemoveOpenAICompatibleProvider(name)
				return name
			},
		)
	case 1:
		manageProviderList(
			"OpenAI Responses",
			func() int { return len(config.OpenAIResponsesCompatibleProviders) },
			func(i int) string {
				p := config.OpenAIResponsesCompatibleProviders[i]
				return fmt.Sprintf("%s (%s - %s)", p.Name, p.BaseURL, p.Model)
			},
			func(i int) providerEditable { return responsesProviderEditor{i} },
			func(i int) string {
				name := config.OpenAIResponsesCompatibleProviders[i].Name
				RemoveOpenAIResponsesCompatibleProvider(name)
				return name
			},
		)
	case 2:
		manageProviderList(
			"Anthropic",
			func() int { return len(config.AnthropicProviders) },
			func(i int) string {
				p := config.AnthropicProviders[i]
				return fmt.Sprintf("%s (%s - %s)", p.Name, p.BaseURL, p.Model)
			},
			func(i int) providerEditable { return anthropicProviderEditor{i} },
			func(i int) string {
				name := config.AnthropicProviders[i].Name
				RemoveAnthropicProvider(name)
				return name
			},
		)
	}
}

// manageProviderList is the generic Edit/Remove submenu for any type of provider.
// It replaces manageOpenAICompatibleProviders, manageResponsesProviders, manageAnthropicProviders.
func manageProviderList(
	typeName string,
	count func() int,
	label func(i int) string,
	editor func(i int) providerEditable,
	remove func(i int) string, // removes and returns the removed name
) {
	if count() == 0 {
		fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("No %s providers configured.", typeName)))
		return
	}

	action := ui.SelectOptionWithStyledTitle(
		ui.SelectorHeader.Render("▸  Manage "+typeName+" Providers"),
		[]string{"Edit a provider", "Remove a provider", "← Back"},
	)

	buildOptions := func() []string {
		opts := make([]string, count())
		for i := range opts {
			opts[i] = label(i)
		}
		return opts
	}

	switch action {
	case 0:
		idx := ui.SelectOption("Select provider to edit", buildOptions())
		if idx < 0 {
			return // dismissed
		}
		editProvider(editor(idx))
	case 1:
		idx := ui.SelectOption("Select provider to remove", buildOptions())
		if idx < 0 {
			return // dismissed
		}
		removed := remove(idx)
		Save()
		fmt.Println(ui.OkStyle.Render(fmt.Sprintf("%s provider '%s' removed.", typeName, removed)))
	}
}
