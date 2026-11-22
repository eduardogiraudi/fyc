package providers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestModelsURL(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		override string
		want     string
	}{
		{"plain base", "https://api.example.com/v1", "", "https://api.example.com/v1/models"},
		{"trailing slash", "https://api.example.com/v1/", "", "https://api.example.com/v1/models"},
		{"full chat endpoint", "https://api.example.com/v1/chat/completions", "", "https://api.example.com/v1/models"},
		{"full responses endpoint", "https://api.example.com/v1/responses", "", "https://api.example.com/v1/models"},
		{"full messages endpoint", "https://api.example.com/v1/messages", "", "https://api.example.com/v1/models"},
		{"override wins", "https://api.example.com/v1", "https://catalog.example/models", "https://catalog.example/models"},
		{"override keeps query", "https://api.example.com/v1", "https://management.example/openai/models?api-version=2024-10-21", "https://management.example/openai/models?api-version=2024-10-21"},
		{"blank override", "https://api.example.com/v1", "   ", "https://api.example.com/v1/models"},
		{"empty base", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelsURL(tc.baseURL, tc.override); got != tc.want {
				t.Fatalf("ModelsURL(%q, %q) = %q, want %q", tc.baseURL, tc.override, got, tc.want)
			}
		})
	}
}

// TestGetOpenAICompatibleModelsAcceptsCatalogShapes pins the tolerant parser: a
// provider whose model list lives elsewhere often answers a bare array or a
// {"models":…} wrapper instead of the documented {"data":…}.
func TestGetOpenAICompatibleModelsAcceptsCatalogShapes(t *testing.T) {
	bodies := map[string]string{
		"data":   `{"data":[{"id":"a"},{"id":"b"}]}`,
		"bare":   `[{"id":"a"},{"slug":"b"}]`,
		"models": `{"models":[{"name":"a"},{"id":"b"}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/catalog/models" {
					t.Errorf("path = %q, want /catalog/models", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer k" {
					t.Errorf("Authorization = %q, want Bearer k", got)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			got, err := GetOpenAICompatibleModels(srv.URL+"/catalog/models", "k")
			if err != nil {
				t.Fatalf("GetOpenAICompatibleModels: %v", err)
			}
			if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("models = %v, want %v", got, want)
			}
		})
	}
}

// TestGetOpenAICompatibleModelsReportsStatus keeps a broken endpoint diagnosable
// instead of returning an empty list.
func TestGetOpenAICompatibleModelsReportsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := GetOpenAICompatibleModels(srv.URL, "")
	if err == nil {
		t.Fatal("want an error for a 401 answer")
	}
}

func TestGetAnthropicModelsUsesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/models" {
			t.Errorf("path = %q, want /proxy/models", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("x-api-key missing")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"}]}`))
	}))
	defer srv.Close()

	got, err := GetAnthropicModels(srv.URL+"/proxy/models", "k")
	if err != nil {
		t.Fatalf("GetAnthropicModels: %v", err)
	}
	if want := []string{"claude-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}
