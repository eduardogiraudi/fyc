// Package models holds the models.dev catalog: a database of model capabilities
// stored as a standalone JSON file, never embedded in the binary. Only the
// fields fyc needs for reasoning management are modelled; the rest is ignored.
//
// The file lives at Path (the config directory). `fuckyourcli models update`
// overwrites it in place; the copy shipped with a release is installed by
// setup.sh / setup.ps1 / install.sh / install.ps1.
package models

import "encoding/json"

// Catalog is the whole models.dev document, keyed by provider id.
type Catalog map[string]Provider

// Provider is one entry of the catalog.
type Provider struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Env  []string `json:"env,omitempty"`
	NPM  string   `json:"npm,omitempty"`
	Doc  string   `json:"doc,omitempty"`
	// API is the provider base URL: present only for OpenAI-compatible
	// providers, it lets fyc match its own base_url to a catalog provider.
	API    string           `json:"api,omitempty"`
	Models map[string]Model `json:"models"`
}

// Model is a single model entry. Only reasoning-relevant fields are kept.
type Model struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Family           string            `json:"family,omitempty"`
	Reasoning        bool              `json:"reasoning,omitempty"`
	ReasoningOptions []ReasoningOption `json:"reasoning_options,omitempty"`
	// Interleaved describes how reasoning is re-fed into the next turn.
	Interleaved *Interleaved `json:"interleaved,omitempty"`
	ToolCall    bool         `json:"tool_call,omitempty"`
	// Temperature is nil when the catalog does not document it. An explicit
	// false means the model rejects temperature/top_p (the absence of the field
	// must not be read as a rejection).
	Temperature *bool  `json:"temperature,omitempty"`
	Status      string `json:"status,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	LastUpdated string `json:"last_updated,omitempty"`
	Limit       Limit  `json:"limit,omitempty"`
	CanonicalID string `json:"canonical_model_id,omitempty"`
}

// ReasoningOption is one documented way a model exposes its thinking control.
// The "type" discriminates: "effort" carries Values, "budget_tokens" carries
// Min/Max, "toggle" carries neither.
type ReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Min    *int     `json:"min,omitempty"`
	Max    *int     `json:"max,omitempty"`
}

// Interleaved is "bool | { field: string }" in the models.dev schema, so it
// needs a custom decoder. An empty Field means "interleaved, field unknown".
type Interleaved struct {
	Field string
}

// UnmarshalJSON accepts both the boolean and the object form.
func (i *Interleaved) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '{' {
		var obj struct {
			Field string `json:"field"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		i.Field = obj.Field
		return nil
	}
	return nil // boolean form: interleaved but no named field
}

// Limit holds the context/output token limits.
type Limit struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// Stats returns the number of providers and models in the catalog.
func (c Catalog) Stats() (providers, modelCount int) {
	for _, p := range c {
		providers++
		modelCount += len(p.Models)
	}
	return providers, modelCount
}
