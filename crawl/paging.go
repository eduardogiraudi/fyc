package crawl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Paged documents: windows, heading outline, in-document search.
// ---------------------------------------------------------------------------

const (
	maxOutlineEntries = 80  // headings listed on page 1
	maxOutlineTitle   = 100 // runes per heading title
	maxFindMatches    = 20  // snippets returned by a find
	maxFindPages      = 50  // distinct pages listed by a find
	findContext       = 70  // runes of context on each side of a match
)

// pageInfo describes one window of a paged document.
type pageInfo struct {
	total  int // total characters (runes) in the full document
	page   int // 1-based index actually returned
	pages  int // total number of pages
	offset int // first character index of this window
	length int // characters in this window
}

// PageOutOfRangeError is returned when the requested page does not exist.
// Failing loudly (instead of silently serving the last page) stops a model
// from looping on "give me the next page" past the end of a document.
type PageOutOfRangeError struct {
	Page  int
	Pages int
}

func (e *PageOutOfRangeError) Error() string {
	return fmt.Sprintf("page %d does not exist: this document has %d page(s), valid pages are 1-%d",
		e.Page, e.Pages, e.Pages)
}

// pagedDoc is a document split into page windows. It keeps the rune slice so
// offsets used by the outline and find features are in the same unit as the
// page boundaries.
type pagedDoc struct {
	runes  []rune
	starts []int
	ends   []int
}

// newPagedDoc splits content into windows of at most limit characters
// (runes), preferring to break on a newline in the last quarter of the window
// so a page does not cut a sentence in half.
func newPagedDoc(content string, limit int) *pagedDoc {
	runes := []rune(content)
	total := len(runes)

	starts := []int{0}
	var ends []int
	pos := 0
	for pos < total {
		end := pos + limit
		if end >= total {
			ends = append(ends, total)
			break
		}
		brk := end
		for i := end; i > pos+limit*3/4; i-- {
			if runes[i-1] == '\n' {
				brk = i
				break
			}
		}
		ends = append(ends, brk)
		pos = brk
		starts = append(starts, pos)
	}
	if len(ends) == 0 {
		ends = append(ends, 0)
	}
	return &pagedDoc{runes: runes, starts: starts, ends: ends}
}

func (d *pagedDoc) pages() int { return len(d.ends) }
func (d *pagedDoc) total() int { return len(d.runes) }

// pageAt returns the 1-based page that contains the rune offset.
func (d *pagedDoc) pageAt(offset int) int {
	i := sort.Search(len(d.ends), func(i int) bool { return d.ends[i] > offset })
	if i >= len(d.ends) {
		i = len(d.ends) - 1
	}
	return i + 1
}

// window returns page (which must be within 1..pages) decorated with a header
// telling the reader where it is and how to get the rest.
func (d *pagedDoc) window(page int) (string, pageInfo) {
	start, end := d.starts[page-1], d.ends[page-1]
	chunk := strings.TrimSpace(string(d.runes[start:end]))
	info := pageInfo{total: len(d.runes), page: page, pages: d.pages(), offset: start, length: end - start}
	if info.pages > 1 {
		chunk = fmt.Sprintf(
			"[page %d/%d of %d characters — pass the `page` parameter to web_search to read other pages, or `find` to locate a term]\n\n%s",
			info.page, info.pages, info.total, chunk)
	}
	return chunk, info
}

// slicePage is the original single-call helper, kept for existing callers and
// tests. Out-of-range pages are clamped; CrawlPagedOpts reports them instead.
func slicePage(content string, page, limit int) (string, pageInfo) {
	d := newPagedDoc(content, limit)
	if page > d.pages() {
		page = d.pages()
	}
	if page < 1 {
		page = 1
	}
	return d.window(page)
}

// ---------------------------------------------------------------------------
// Outline
// ---------------------------------------------------------------------------

type outlineEntry struct {
	Level int    `json:"level"`
	Title string `json:"title"`
	Page  int    `json:"page"`
}

var (
	reHeading = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	reMDLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// outline lists the markdown headings with the page each one starts on.
// Headings inside fenced code blocks are ignored. When there are more than
// maxOutlineEntries the deepest levels are dropped first, so the result stays
// a usable table of contents; truncated reports that anything was omitted.
func (d *pagedDoc) outline() (entries []outlineEntry, truncated bool) {
	var all []outlineEntry
	inFence := false
	n := len(d.runes)
	for ls := 0; ls < n; {
		le := ls
		for le < n && d.runes[le] != '\n' {
			le++
		}
		if le-ls <= 300 {
			line := string(d.runes[ls:le])
			trimmed := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
				inFence = !inFence
			case !inFence:
				if m := reHeading.FindStringSubmatch(line); m != nil {
					title := reMDLink.ReplaceAllString(m[2], "$1")
					title = strings.TrimSpace(strings.Trim(title, "*_ \t"))
					if title != "" {
						if r := []rune(title); len(r) > maxOutlineTitle {
							title = string(r[:maxOutlineTitle]) + "…"
						}
						all = append(all, outlineEntry{Level: len(m[1]), Title: title, Page: d.pageAt(ls)})
					}
				}
			}
		}
		ls = le + 1
	}

	for maxLvl := 6; len(all) > maxOutlineEntries && maxLvl > 1; maxLvl-- {
		kept := make([]outlineEntry, 0, len(all))
		for _, e := range all {
			if e.Level < maxLvl {
				kept = append(kept, e)
			}
		}
		all = kept
		truncated = true
	}
	if len(all) > maxOutlineEntries {
		all = all[:maxOutlineEntries]
		truncated = true
	}
	return all, truncated
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

type findMatch struct {
	Page    int    `json:"page"`
	Snippet string `json:"snippet"`
}

type findResult struct {
	Query   string      `json:"query"`
	Total   int         `json:"total_matches"`
	Pages   []int       `json:"pages"`
	Matches []findMatch `json:"matches"`
}

// find searches the whole document case-insensitively and returns every page
// containing the term plus a few snippets, so the model can jump straight to
// the relevant window instead of paging through the document.
func (d *pagedDoc) find(query string) findResult {
	res := findResult{Query: query, Pages: []int{}, Matches: []findMatch{}}
	q := strings.TrimSpace(query)
	if q == "" {
		return res
	}

	// unicode.ToLower maps one rune to one rune, so rune offsets in the
	// lowered text are the same as in d.runes.
	low := strings.Map(unicode.ToLower, string(d.runes))
	needle := strings.Map(unicode.ToLower, q)
	needleRunes := utf8.RuneCountInString(needle)

	seen := map[int]bool{}
	runePos, prevByte, from := 0, 0, 0
	for {
		idx := strings.Index(low[from:], needle)
		if idx < 0 {
			break
		}
		abs := from + idx
		runePos += utf8.RuneCountInString(low[prevByte:abs])
		prevByte = abs
		from = abs + len(needle)

		res.Total++
		pg := d.pageAt(runePos)
		if !seen[pg] {
			seen[pg] = true
			if len(res.Pages) < maxFindPages {
				res.Pages = append(res.Pages, pg)
			}
		}
		if len(res.Matches) < maxFindMatches {
			s := runePos - findContext
			if s < 0 {
				s = 0
			}
			e := runePos + needleRunes + findContext
			if e > len(d.runes) {
				e = len(d.runes)
			}
			snippet := strings.Join(strings.Fields(string(d.runes[s:e])), " ")
			if s > 0 {
				snippet = "…" + snippet
			}
			if e < len(d.runes) {
				snippet += "…"
			}
			res.Matches = append(res.Matches, findMatch{Page: pg, Snippet: snippet})
		}
	}
	return res
}

// marshalPayload encodes v as compact JSON without escaping <, > and & (the
// default escaping turns every "<" of the page text into the six characters
// \u003c, which wastes tokens).
func marshalPayload(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}"
	}
	return strings.TrimRight(buf.String(), "\n")
}
