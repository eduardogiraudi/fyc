package providers

import (
	"net/http"
	"strings"
	"time"
)

// modelsClient bounds the model-list request. The callers are interactive
// (the /models picker, the settings editor), so an endpoint that never answers
// must fail instead of freezing the prompt.
var modelsClient = &http.Client{Timeout: 20 * time.Second}

// ModelsURL resolves the URL of a provider's model-list endpoint.
//
// An explicit override (the provider's models_url) is used verbatim, query
// string included: some providers serve the chat API and the model list from
// different hosts or paths (Azure deployments, GitHub Models, catalog APIs).
// Without an override the chat base URL yields <base>/models, and a base URL
// that already carries the chat endpoint has that suffix swapped, because
// fyc accepts a full endpoint in base_url as well.
func ModelsURL(baseURL, override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, endpoint := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(base, endpoint) {
			base = strings.TrimSuffix(base, endpoint)
			break
		}
	}
	if base == "" {
		return ""
	}
	return base + "/models"
}
