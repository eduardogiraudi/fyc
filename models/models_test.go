package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleCatalog = `{
  "deepseek": {
    "id": "deepseek", "name": "DeepSeek",
    "models": {
      "deepseek-v4-pro": {
        "id": "deepseek-v4-pro", "name": "DeepSeek V4 Pro",
        "reasoning": true,
        "reasoning_options": [{"type":"effort","values":["low","high","max"]}],
        "interleaved": {"field":"reasoning_content"}
      }
    }
  },
  "google": {
    "id": "google", "name": "Google",
    "models": {
      "gemini-2.5-flash": {
        "id": "gemini-2.5-flash", "name": "Gemini 2.5 Flash",
        "reasoning": true,
        "reasoning_options": [{"type":"budget_tokens","min":0,"max":32768}],
        "interleaved": true
      }
    }
  },
  "deepinfra": {
    "id": "deepinfra", "name": "Deep Infra",
    "models": {
      "meta-llama/Llama-3.3-70B": {
        "id": "meta-llama/Llama-3.3-70B", "name": "Llama 3.3 70B",
        "reasoning": false
      }
    }
  }
}`

func TestParseAndLookup(t *testing.T) {
	c := parseBytes([]byte(sampleCatalog))
	if p, n := c.Stats(); p != 3 || n != 3 {
		t.Fatalf("Stats() = (%d,%d), want (3,3)", p, n)
	}

	pid, m, ok := c.LookupModel("deepseek-v4-pro")
	if !ok || pid != "deepseek" || !m.Reasoning {
		t.Fatalf("lookup failed: pid=%q ok=%v m=%+v", pid, ok, m)
	}
	if len(m.ReasoningOptions) != 1 || strings.Join(m.ReasoningOptions[0].Values, ",") != "low,high,max" {
		t.Fatalf("reasoning options = %+v", m.ReasoningOptions)
	}

	// Suffix match: an id only present as "org/model".
	if _, _, ok := c.LookupModel("Llama-3.3-70B"); !ok {
		t.Fatal("suffix lookup failed")
	}

	if _, _, ok := c.LookupModel("does-not-exist"); ok {
		t.Fatal("unexpected match for an unknown model")
	}
	if _, _, ok := c.LookupModel("  "); ok {
		t.Fatal("empty id must not match")
	}
}

func TestInterleavedForms(t *testing.T) {
	c := parseBytes([]byte(sampleCatalog))
	_, ds, _ := c.LookupModel("deepseek-v4-pro")
	if ds.Interleaved == nil || ds.Interleaved.Field != "reasoning_content" {
		t.Fatalf("object interleaved not decoded: %+v", ds.Interleaved)
	}
	_, gm, _ := c.LookupModel("gemini-2.5-flash")
	if gm.Interleaved == nil || gm.Interleaved.Field != "" {
		t.Fatalf("boolean interleaved should decode with an empty field, got %+v", gm.Interleaved)
	}
}

// useConfigDir points the catalog at a throw-away config directory.
func useConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", dir)
	return dir
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	useConfigDir(t) // no catalog file
	if got := Load(); len(got) != 0 {
		t.Fatalf("expected an empty catalog when the file is missing, got %d providers", len(got))
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := useConfigDir(t)
	// A provider id that cannot appear in the real models.dev dump, so the
	// loaded file is unmistakable.
	const override = `{"__override__":{"id":"__override__","models":{"m":{"id":"m"}}}}`
	if err := os.WriteFile(filepath.Join(dir, catalogFileName), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if _, ok := c["__override__"]; !ok {
		t.Fatalf("catalog file not loaded: %v", c)
	}
	if p, _ := c.Stats(); p != 1 {
		t.Fatalf("expected only the loaded catalog, got %d providers", p)
	}
}

func withServer(t *testing.T, body string, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := DataURL
	DataURL = srv.URL
	t.Cleanup(func() { DataURL = old })
}

func TestUpdateWritesOverride(t *testing.T) {
	dir := useConfigDir(t)
	withServer(t, sampleCatalog, http.StatusOK)

	providers, count, err := Update(context.Background())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if providers != 3 || count != 3 {
		t.Fatalf("Update = (%d,%d), want (3,3)", providers, count)
	}
	if _, err := os.Stat(filepath.Join(dir, catalogFileName)); err != nil {
		t.Fatalf("catalog not written: %v", err)
	}
	if _, ok := Load()["deepseek"]; !ok {
		t.Fatal("Load did not pick up the downloaded catalog")
	}
}

func TestUpdateRejectsBadPayloads(t *testing.T) {
	useConfigDir(t)
	withServer(t, "not json", http.StatusOK)
	if _, _, err := Update(context.Background()); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}

	withServer(t, "{}", http.StatusOK)
	if _, _, err := Update(context.Background()); err == nil {
		t.Fatal("expected an error for an empty catalog")
	}

	withServer(t, sampleCatalog, http.StatusInternalServerError)
	if _, _, err := Update(context.Background()); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestReasoningInfo(t *testing.T) {
	dir := useConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, catalogFileName), []byte(sampleCatalog), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ReasoningInfo("deepseek-v4-pro")
	if !strings.Contains(got, "reasoning") || !strings.Contains(got, "effort=low|high|max") {
		t.Fatalf("ReasoningInfo = %q", got)
	}
	if got := ReasoningInfo("gemini-2.5-flash"); !strings.Contains(got, "budget_tokens") {
		t.Fatalf("ReasoningInfo = %q", got)
	}
	if got := ReasoningInfo("Llama-3.3-70B"); !strings.Contains(got, "no reasoning") {
		t.Fatalf("ReasoningInfo = %q", got)
	}
	if got := ReasoningInfo("unknown-model"); got != "" {
		t.Fatalf("ReasoningInfo(unknown) = %q, want empty", got)
	}
}
