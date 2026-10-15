package crawl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func seed(t *testing.T, url, content string) {
	t.Helper()
	cachePut("\x00"+url, content) // engine "" → key "\x00"+input
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad json: %v\n%s", err, s)
	}
	return m
}

func TestCrawlPagedOpts(t *testing.T) {
	url := "https://example.test/doc"
	filler := strings.Repeat("lorem ipsum dolor sit amet\n", 400) // ~10.8k chars
	seed(t, url, "# Intro\n"+filler+"## Pricing\n"+filler+"unique-needle here\n"+filler+"## FAQ\n"+filler)

	// Page 1: window + outline + next_page
	out, err := CrawlPagedOpts(url, "", PagedOptions{MaxChars: 5000})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out)
	if m["page"].(float64) != 1 || m["next_page"].(float64) != 2 || m["outline"] == nil {
		t.Errorf("page1 payload: %v", m)
	}
	pages := int(m["pages"].(float64))

	// Find only (no page): matches, no content
	out, err = CrawlPagedOpts(url, "", PagedOptions{MaxChars: 5000, Find: "UNIQUE-needle"})
	if err != nil {
		t.Fatal(err)
	}
	m = decode(t, out)
	if _, has := m["content"]; has {
		t.Error("find-only must not return content")
	}
	f := m["find"].(map[string]any)
	if f["total_matches"].(float64) != 1 {
		t.Errorf("find: %v", f)
	}
	hit := int(f["pages"].([]any)[0].(float64))

	// Reading the page reported by find really contains the term
	out, err = CrawlPagedOpts(url, "", PagedOptions{MaxChars: 5000, Page: hit})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decode(t, out)["content"].(string), "unique-needle") {
		t.Errorf("page %d lacks the term", hit)
	}

	// Last page has no next_page and no outline
	out, _ = CrawlPagedOpts(url, "", PagedOptions{MaxChars: 5000, Page: pages})
	m = decode(t, out)
	if _, has := m["next_page"]; has || m["outline"] != nil {
		t.Errorf("last page payload: %v", m)
	}

	// Out of range → explicit error
	_, err = CrawlPagedOpts(url, "", PagedOptions{MaxChars: 5000, Page: pages + 1})
	var pe *PageOutOfRangeError
	if !errors.As(err, &pe) || pe.Pages != pages {
		t.Errorf("want PageOutOfRangeError, got %v", err)
	}
	_, err = CrawlPagedOpts(url, "", PagedOptions{MaxChars: 5000, Page: -2})
	if !errors.As(err, &pe) {
		t.Errorf("negative page should error, got %v", err)
	}

	// Legacy wrapper: page 0 → 1, past the end → error
	out, err = CrawlPaged(url, "", 0, 5000)
	if err != nil || decode(t, out)["page"].(float64) != 1 {
		t.Errorf("legacy page 0: %v %v", out, err)
	}
	if _, err = CrawlPaged(url, "", 99, 5000); err == nil {
		t.Error("legacy past-end should error")
	}
}
