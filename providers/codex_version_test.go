package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCodexModelsDynamicVersion(t *testing.T) {
	previousResolver, previousURL := resolveCodexClientVersion, codexBaseURL
	t.Cleanup(func() { resolveCodexClientVersion, codexBaseURL = previousResolver, previousURL })
	calls := 0
	resolveCodexClientVersion = func(ctx context.Context) string {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing model request deadline")
		}
		calls++
		return "0.161.0"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/models" || r.URL.Query().Get("client_version") != "0.161.0" || r.Header.Get("User-Agent") != "codex-cli/0.161.0" {
			t.Errorf("inconsistent version: %s, %v", r.URL, r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("token changed")
		}
		fmt.Fprint(w, `{"models":[{"slug":"gpt-5.5"}]}`)
	}))
	defer srv.Close()
	codexBaseURL = srv.URL
	models, err := fetchCodexModels("test-token")
	if err != nil || len(models) != 1 || models[0] != "gpt-5.5" || calls != 1 {
		t.Fatalf("models %v, err %v, lookups %d", models, err, calls)
	}
	if _, err := fetchCodexModels(" "); err == nil || calls != 1 {
		t.Fatal("empty token triggered version lookup")
	}
}
