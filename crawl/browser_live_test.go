package crawl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBrowserLiveRenderingEventsCancellationAndRecovery(t *testing.T) {
	if os.Getenv("FYC_CRAWL_BROWSER_TEST") != "1" {
		t.Skip("set FYC_CRAWL_BROWSER_TEST=1 with a local browser and installed Playwright driver")
	}
	if !BrowserAvailable() {
		t.Skip("no supported browser installed")
	}
	Shutdown()
	t.Cleanup(Shutdown)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<title>Rendered page</title><main id="root"></main><script>setTimeout(() => {
		const button = document.createElement('button'); button.textContent = 'Accept';
		button.addEventListener('click', function(e) {
			if (this !== button || e.target !== button || e.type !== 'click') throw new Error('broken native event');
			e.preventDefault(); document.getElementById('root').textContent = 'Rendered JavaScript content with native events preserved.';
		}); document.getElementById('root').append(button); button.click();
		}, 1600);</script>`)
	}))
	defer server.Close()
	policy := networkPolicy{allowPrivate: true}
	doc, err := fetchBrowser(context.Background(), server.URL, PagedOptions{}, policy)
	if err != nil || !strings.Contains(doc.content, "Rendered JavaScript content") || doc.status != 200 || doc.title != "Rendered page" {
		t.Fatalf("rendering/event semantics failed: %+v %v", doc, err)
	}
	mu.Lock()
	browser := sharedBrowser
	mu.Unlock()
	_ = browser.Close()
	if _, err := fetchBrowser(context.Background(), server.URL, PagedOptions{}, policy); err != nil {
		t.Fatal("dead browser was not recovered:", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	started := time.Now()
	_, err = fetchBrowser(ctx, server.URL, PagedOptions{}, policy)
	if !errors.Is(err, context.Canceled) || time.Since(started) > 2*time.Second {
		t.Fatal("browser ignored cancellation:", err)
	}
}
