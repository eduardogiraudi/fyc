package config

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestAvailableModelsUsesModelsURL proves the /models picker lists from the
// provider's own endpoint: here the chat base URL has no model list at all, and
// a fallback would only ever return the current model.
func TestAvailableModelsUsesModelsURL(t *testing.T) {
	withConfigDir(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	defer srv.Close()

	config.Provider = ProviderOpenAICompatible
	config.Model = "m1"
	config.ActiveProviderName = "Proxy"
	config.OpenAICompatibleProviders = []OpenAICompatibleProviderConfig{{
		Name:      "Proxy",
		BaseURL:   srv.URL + "/v1",
		ModelsURL: srv.URL + "/catalog/models",
		Model:     "m1",
	}}

	models, err := GetAvailableModelsForCurrentProvider()
	if err != nil {
		t.Fatalf("GetAvailableModelsForCurrentProvider: %v", err)
	}
	if want := []string{"m1", "m2"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
}

// TestAvailableModelsDerivesFromBaseURL pins the default: without an override
// the listing keeps asking <base_url>/models.
func TestAvailableModelsDerivesFromBaseURL(t *testing.T) {
	withConfigDir(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m3"}]}`))
	}))
	defer srv.Close()

	config.Provider = ProviderOpenAICompatible
	config.Model = "m1"
	config.ActiveProviderName = "Local"
	config.OpenAICompatibleProviders = []OpenAICompatibleProviderConfig{{
		Name:    "Local",
		BaseURL: srv.URL + "/v1",
		Model:   "m1",
	}}

	models, err := GetAvailableModelsForCurrentProvider()
	if err != nil {
		t.Fatalf("GetAvailableModelsForCurrentProvider: %v", err)
	}
	if want := []string{"m1", "m3"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
}
