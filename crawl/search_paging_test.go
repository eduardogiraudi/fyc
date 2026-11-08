package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestDuckDuckGoUsesIssuedPOSTCursor(t *testing.T) {
	var mu sync.Mutex
	var offsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		offset, _ := strconv.Atoi(req.Form.Get("s"))
		if offset > 0 && (req.Method != http.MethodPost || req.Form.Get("vqd") != "cursor" || req.Form.Get("kl") != "it-it") {
			t.Error("continuation dropped POST cursor or region")
		}
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<div class="result"><a class="result__a" href="https://example.com/page%d">Page %d</a></div>
		<form action="/html/" method="post"><input type="submit" value="Next">
		<input type="hidden" name="s" value="%d"><input type="hidden" name="q" value="query">
		<input type="hidden" name="vqd" value="cursor"><input type="hidden" name="kl" value="it-it"></form>`, offset, offset, offset+10)
	}))
	defer server.Close()
	doc, raw, err := fetchDuckDuckGoPage(context.Background(), server.URL+"/html/?q=query&s=0", PagedOptions{SearchPage: 3}, networkPolicy{allowPrivate: true})
	if err != nil || !strings.Contains(string(raw), "Page 20") || doc.status != 200 {
		t.Fatalf("page 3 repeated page 1: %s %v", raw, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(offsets) != "[0 10 20]" {
		t.Fatalf("guessed offsets: %v", offsets)
	}
}

func TestSearchCursorRejectsMissingOrForeignContinuations(t *testing.T) {
	for _, raw := range []string{
		"<main>No next page</main>",
		`<form action="https://evil.test/" method="post"><input type="submit" value="Next"><input type="hidden" name="s" value="10"></form>`,
	} {
		if _, err := nextSearchRequest(context.Background(), raw, "https://html.duckduckgo.com/html/"); err == nil {
			t.Fatal("unsafe or missing continuation accepted")
		}
	}
}
