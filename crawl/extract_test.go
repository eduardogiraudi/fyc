package crawl

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".html")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExtractionFixtures(t *testing.T) {
	for _, name := range []string{"docs", "forum", "article"} {
		t.Run(name, func(t *testing.T) {
			md, title, err := extractMarkdown(context.Background(), fixture(t, name), "https://example.test/redirected/page")
			if err != nil || title == "" || md == "" {
				t.Fatalf("title=%q markdown=%q error=%v", title, md, err)
			}
			if strings.Contains(md, "Navigation noise") || strings.Contains(md, "Footer noise") || strings.Contains(md, "Hidden content") || strings.Contains(md, "window.tracking") {
				t.Fatalf("extraction retained chrome: %s", md)
			}
			switch name {
			case "docs":
				for _, want := range []string{"--timeout", "--token", "|", "```", "client.Run()", "https://example.test/guide", "https://example.test/reference/options#timeout"} {
					if !strings.Contains(md, want) {
						t.Errorf("missing %q in %s", want, md)
					}
				}
			case "forum":
				for _, want := range []string{"First reply", "Second reply", "30 seconds", "https://example.test/examples"} {
					if !strings.Contains(md, want) {
						t.Errorf("missing %q in %s", want, md)
					}
				}
			case "article":
				if !strings.Contains(md, "https://example.test/redirected/more") || !strings.Contains(md, "Shared requests") {
					t.Fatalf("article lost content: %s", md)
				}
			}
		})
	}
}

func TestTablesPreserveCells(t *testing.T) {
	for _, table := range []string{
		`<table><tr><th>Option</th><th>Value</th></tr><tr><td>timeout</td><td>30</td></tr></table>`,
		`<table><tr><td>Option</td><td>Value</td></tr><tr><td>timeout</td><td>30</td></tr></table>`,
	} {
		md, err := toMarkdown(context.Background(), table)
		if err != nil || strings.Count(md, "|") < 6 || !strings.Contains(md, "---") || !strings.Contains(md, "timeout") {
			t.Fatalf("table conversion: %q %v", md, err)
		}
	}
}

func TestExtractionFallbackPreservesSmallDocuments(t *testing.T) {
	got, err := extractPageHTML(`<html><body><h1>Product</h1><a href="/buy">Buy</a><p>Available now.</p></body></html>`, "https://example.test/product")
	if err != nil || !strings.Contains(got, "Product") || !strings.Contains(got, "https://example.test/buy") {
		t.Fatalf("small document fallback=%q %v", got, err)
	}
}

func TestExtractionLimitsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := extractMarkdown(ctx, fixture(t, "docs"), "https://example.test/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, _, err := extractMarkdown(context.Background(), strings.Repeat("x", maxRawDocBytes+1), "https://example.test/"); !errors.Is(err, errDocumentTooLarge) {
		t.Fatalf("size limit missing: %v", err)
	}
	if _, err := parseDocument(context.Background(), "<body>"+strings.Repeat("<b>x</b>", maxDocumentNodes)+"</body>"); !errors.Is(err, errDocumentTooLarge) {
		t.Fatalf("node limit missing: %v", err)
	}
}

func TestChallengeDetection(t *testing.T) {
	for _, raw := range []string{fixture(t, "captcha"), `<title>Access denied</title>`, `<script id="anubis_challenge">{}</script>`, `<form action="/anomaly.js">Pick the ducks</form>`} {
		if !blockedHTML([]byte(raw)) {
			t.Errorf("missed challenge: %s", raw)
		}
	}
	for _, raw := range []string{fixture(t, "docs"), `<title>CAPTCHA implementation guide</title><body><p>CAPTCHA API documentation.</p></body>`, `<main>Our API docs discuss CAPTCHA.</main>`} {
		if blockedHTML([]byte(raw)) {
			t.Errorf("false challenge: %s", raw)
		}
	}
}
