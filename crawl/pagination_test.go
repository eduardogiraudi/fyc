package crawl

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeMaxChars(t *testing.T) {
	t.Setenv("FYC_CRAWL_MAX_CHARS", "")
	cases := []struct{ in, want int }{
		{0, defaultPageChars},
		{5000, 5000},
		{1, minPageChars},
		{maxPageChars + 1, maxPageChars},
	}
	for _, c := range cases {
		if got := normalizeMaxChars(c.in); got != c.want {
			t.Errorf("normalizeMaxChars(%d) = %d, want %d", c.in, got, c.want)
		}
	}

	t.Setenv("FYC_CRAWL_MAX_CHARS", "12345")
	if got := normalizeMaxChars(0); got != 12345 {
		t.Errorf("env override: got %d, want 12345", got)
	}

	t.Setenv("FYC_CRAWL_MAX_CHARS", "not-a-number")
	if got := normalizeMaxChars(0); got != defaultPageChars {
		t.Errorf("invalid env should fall back to default: got %d, want %d", got, defaultPageChars)
	}
}

func TestSlicePageCoversDocument(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "paragraph number %02d with some words here\n\n", i)
	}
	content := sb.String()
	total := len([]rune(content))
	limit := 120

	chunk, info := slicePage(content, 1, limit)
	if info.total != total {
		t.Fatalf("total = %d, want %d", info.total, total)
	}
	if info.pages < 2 {
		t.Fatalf("expected multiple pages, got %d", info.pages)
	}
	if !strings.Contains(chunk, "[page 1/") {
		t.Errorf("first page should carry a header, got %q", chunk)
	}

	// Windows must be contiguous and cover the whole document, page by page.
	pos := 0
	for p := 1; p <= info.pages; p++ {
		_, pi := slicePage(content, p, limit)
		if pi.page != p {
			t.Fatalf("requested page %d, got %d", p, pi.page)
		}
		if pi.offset != pos {
			t.Fatalf("page %d offset = %d, want %d", p, pi.offset, pos)
		}
		if pi.length <= 0 {
			t.Fatalf("page %d has an empty window", p)
		}
		pos = pi.offset + pi.length
	}
	if pos != total {
		t.Fatalf("pages cover %d runes, want %d", pos, total)
	}
}

func TestSlicePageClampAndSingle(t *testing.T) {
	short := "just a short page"
	chunk, info := slicePage(short, 5, 24000)
	if info.pages != 1 || info.page != 1 {
		t.Fatalf("short doc: pages=%d page=%d, want 1/1", info.pages, info.page)
	}
	if chunk != short {
		t.Fatalf("short doc should be returned verbatim, got %q", chunk)
	}

	long := strings.Repeat("word ", 5000)
	_, info = slicePage(long, 999, minPageChars)
	if info.page != info.pages {
		t.Fatalf("out-of-range page should clamp to the last one: page=%d pages=%d", info.page, info.pages)
	}
}
