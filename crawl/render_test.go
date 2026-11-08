package crawl

import (
	"strings"
	"testing"
)

func TestRenderRetryIsNotFooledByNavigationChrome(t *testing.T) {
	raw := "<nav>" + strings.Repeat("Navigation ", 100) + "</nav><main>Loading</main><script>loadApplication()</script>"
	text := visibleHTMLText(raw)
	if text != "Loading" || !needsRendering([]byte(raw), text) {
		t.Fatal("navigation hid an empty SPA shell:", text)
	}
	if needsRendering([]byte("<main>Short static page</main>"), "Short static page") {
		t.Fatal("short static HTML unnecessarily requires a browser")
	}
}
