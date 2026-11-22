package reasoning

import (
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/models"
)

const ctxCatalog = `{
  "azure-host": {
    "id": "azure-host", "name": "Host", "api": "https://host.example.com/v1",
    "models": {"deepseek-chat": {"id": "deepseek-chat", "name": "d", "limit": {"context": 32768}}}
  },
  "mistral": {
    "id": "mistral", "name": "Mistral", "api": "https://api.mistral.ai/v1",
    "models": {"mistral-large": {"id": "mistral-large", "name": "m", "limit": {"context": 128000}}}
  },
  "aaa-aggregator": {
    "id": "aaa-aggregator", "name": "Agg",
    "models": {"deepseek-chat": {"id": "deepseek-chat", "name": "d", "limit": {"context": 8000}}}
  },
  "deepseek": {
    "id": "deepseek", "name": "DeepSeek",
    "models": {"deepseek-chat": {"id": "deepseek-chat", "name": "d", "limit": {"context": 131072}}}
  }
}`

func useCatalog(t *testing.T, doc string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYC_CONFIG_DIR", dir)
	models.Invalidate()
	t.Cleanup(models.Invalidate)
}

func TestCatalogContextWindowOrder(t *testing.T) {
	useCatalog(t, ctxCatalog)
	cases := []struct {
		name, base, model string
		want              int
	}{
		{"host that serves the model wins", "https://host.example.com/v1", "deepseek-chat", 32768},
		{"mistral endpoint with a deepseek model uses the first-party entry, not the aggregator", "https://api.mistral.ai/v1", "deepseek-chat", 131072},
		{"unknown endpoint uses the first-party entry", "https://gw.internal/v1", "deepseek-chat", 131072},
		{"mistral model on mistral", "https://api.mistral.ai/v1", "mistral-large", 128000},
		{"unknown model is zero", "https://api.mistral.ai/v1", "deepseek-4.1", 0},
	}
	for _, tc := range cases {
		if got := CatalogContextWindow(tc.base, tc.model); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
