package crawl

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestSERPFixtures(t *testing.T) {
	for _, engine := range []string{"startpage", "duckduckgo", "brave"} {
		t.Run(engine, func(t *testing.T) {
			results, err := extractSearchResults(context.Background(), fixture(t, engine), searchURL(engine, "query", PagedOptions{}), engine)
			if err != nil || len(results) != 2 {
				t.Fatalf("results=%+v error=%v", results, err)
			}
			if results[0].Title != "Example docs" || results[0].URL != "https://example.com/docs" || results[0].Snippet != "API reference and options." {
				t.Fatalf("wrong result: %+v", results[0])
			}
			if results[1].URL != "https://example.org/guide" {
				t.Fatal(results[1])
			}
		})
	}
}

func TestSearchFallbackOnErrorsEmptyAndBlocks(t *testing.T) {
	engines, _ := searchEngines("startpage")
	for _, firstErr := range []error{nil, errors.New("navigation failed"), &CrawlError{Status: 403, Blocked: true, Cause: errors.New("CAPTCHA")}} {
		var called []string
		doc, err := trySearchEngines(context.Background(), engines, func(ctx context.Context, engine string) (fetchedDocument, error) {
			called = append(called, engine)
			if engine == "startpage" {
				return fetchedDocument{}, firstErr
			}
			return fetchedDocument{results: []searchResult{{Title: "Found", URL: "https://example.com"}}}, nil
		})
		if err != nil || len(doc.results) != 1 || strings.Join(called, ",") != "startpage,duckduckgo" {
			t.Fatalf("fallback=%v %+v %v", called, doc, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := trySearchEngines(ctx, engines, func(context.Context, string) (fetchedDocument, error) {
		cancel()
		return fetchedDocument{}, errors.New("other error")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fallback swallowed cancellation: %v", err)
	}
}

func TestSearchPaginationAndOptions(t *testing.T) {
	opts, err := normalizeSearchOptions(PagedOptions{Page: 7, SearchPage: 2, Region: "IT", Language: "it-IT"})
	if err != nil || opts.Page != 7 || opts.SearchPage != 2 || opts.Region != "it" {
		t.Fatalf("options=%+v %v", opts, err)
	}
	for engine, key := range map[string]string{"duckduckgo": "s", "brave": "offset", "startpage": "page"} {
		u, _ := url.Parse(searchURL(engine, "C++ & Go", opts))
		if u.Query().Get("q") != "C++ & Go" || u.Query().Get(key) == "" {
			t.Fatal(u)
		}
		if engine == "duckduckgo" && (u.Query().Get("s") != "0" || u.Query().Get("kl") != "it-it") {
			t.Fatal(u)
		}
		if engine == "brave" && (u.Query().Get("offset") != "1" || u.Query().Get("country") != "IT" || u.Query().Get("search_lang") != "it") {
			t.Fatal(u)
		}
		if engine == "startpage" && (u.Query().Get("page") != "2" || u.Query().Get("language") != "italiano") {
			t.Fatal(u)
		}
	}
	for _, bad := range []PagedOptions{{SearchPage: -1}, {SearchPage: 11}, {Region: "bad/value"}, {Language: "not a language"}} {
		if _, err := normalizeSearchOptions(bad); err == nil {
			t.Fatalf("accepted bad options %+v", bad)
		}
	}
	if _, err := searchEngines("unknown"); err == nil {
		t.Fatal("unknown engine accepted")
	}
}

func TestSERPRecognitionDoesNotMatchDocumentationHosts(t *testing.T) {
	for _, raw := range []string{"https://developers.google.com/docs", "https://evilduckduckgo.com/?q=x", "https://www.startpage.com/privacy", "https://duckduckgo.com/privacy"} {
		if isSERP(raw) {
			t.Errorf("false SERP: %s", raw)
		}
	}
}

func TestStructuredResultsHaveIndependentBudget(t *testing.T) {
	results := make([]searchResult, 30)
	for i := range results {
		results[i] = searchResult{Title: "Title", URL: "https://example.test/", Snippet: strings.Repeat("x", 700)}
	}
	payload := map[string]any{}
	addSearchResults(payload, results, 2000)
	shown := payload["results"].([]searchResult)
	if len(shown) != 1 || payload["results_truncated"] != true || len(marshalPayload(shown)) > 1000 {
		t.Fatalf("budget not enforced: %+v", payload)
	}
}
