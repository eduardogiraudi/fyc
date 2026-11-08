package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-shiori/dom"
	"golang.org/x/net/html"
)

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

var regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]{2})?$`)

func normalizeSearchOptions(opts PagedOptions) (PagedOptions, error) {
	if opts.SearchPage < 0 || opts.SearchPage > 10 {
		return opts, errors.New("search_page must be between 1 and 10")
	}
	opts.SearchPage = max(1, opts.SearchPage)
	opts.Region = strings.ToLower(strings.TrimSpace(opts.Region))
	opts.Language = strings.TrimSpace(opts.Language)
	if opts.Region != "" && !regionPattern.MatchString(opts.Region) {
		return opts, errors.New("region must be a country code or region-language code, e.g. it or it-it")
	}
	if opts.Language != "" && !reLocale.MatchString(opts.Language) {
		return opts, errors.New("language must be a language tag, e.g. it or it-IT")
	}
	return opts, nil
}

func searchEngines(engine string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "", "startpage":
		return []string{"startpage", "duckduckgo", "brave"}, nil
	case "duckduckgo":
		return []string{"duckduckgo"}, nil
	case "brave":
		return []string{"brave"}, nil
	default:
		return nil, errors.New("unknown search engine; use startpage, duckduckgo or brave")
	}
}

func searchURL(engine, query string, opts PagedOptions) string {
	values := url.Values{"q": {query}}
	language := opts.Language
	if language == "" {
		language = profileLocale()
	}
	primary := strings.ToLower(strings.SplitN(language, "-", 2)[0])
	country := strings.ToUpper(strings.SplitN(opts.Region, "-", 2)[0])
	page := max(1, opts.SearchPage)
	var endpoint string
	switch engine {
	case "duckduckgo":
		endpoint = "https://html.duckduckgo.com/html/"
		region := opts.Region
		if region != "" && !strings.Contains(region, "-") {
			region += "-" + primary
		}
		if region != "" {
			values.Set("kl", region)
		}
		// HTML search needs the server's POST cursor, not a guessed GET offset.
		values.Set("s", "0")
	case "brave":
		endpoint = "https://search.brave.com/search"
		values.Set("offset", strconv.Itoa(page-1))
		values.Set("search_lang", primary)
		if country != "" {
			values.Set("country", country)
		}
	case "startpage":
		endpoint = "https://www.startpage.com/do/dsearch"
		values.Set("page", strconv.Itoa(page))
		// Startpage's interface uses language names rather than ISO tags.
		names := map[string]string{"en": "english", "it": "italiano", "de": "deutsch", "fr": "francais", "es": "espanol", "nl": "nederlands", "pt": "portugues"}
		if name := names[primary]; name != "" {
			values.Set("language", name)
		}
		if country != "" {
			values.Set("country", country)
		}
	}
	return endpoint + "?" + values.Encode()
}

func serpEngine(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	switch {
	case (host == "duckduckgo.com" || host == "www.duckduckgo.com" || host == "html.duckduckgo.com") && (u.Query().Has("q") || strings.HasPrefix(u.Path, "/html")):
		return "duckduckgo"
	case (host == "startpage.com" || host == "www.startpage.com") && (strings.Contains(u.Path, "search") || u.Query().Has("query")):
		return "startpage"
	case host == "search.brave.com" && strings.HasPrefix(u.Path, "/search"):
		return "brave"
	case (host == "google.com" || host == "www.google.com") && u.Path == "/search":
		return "google"
	}
	return ""
}

func isSERP(raw string) bool { return serpEngine(raw) != "" }

func fetchSearch(ctx context.Context, query, engine string, opts PagedOptions, policy networkPolicy) (fetchedDocument, error) {
	engines, err := searchEngines(engine)
	if err != nil {
		return fetchedDocument{}, err
	}
	return trySearchEngines(ctx, engines, func(ctx context.Context, engine string) (fetchedDocument, error) {
		doc, err := fetchSearchPage(ctx, searchURL(engine, query, opts), engine, opts, policy)
		if err == nil {
			doc.engine, doc.searchPage, doc.region, doc.language = engine, opts.SearchPage, opts.Region, opts.Language
		}
		return doc, err
	})
}

func trySearchEngines(ctx context.Context, engines []string, fetch func(context.Context, string) (fetchedDocument, error)) (fetchedDocument, error) {
	var failures []error
	var last fetchedDocument
	for _, engine := range engines {
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
		doc, err := fetch(ctx, engine)
		last = doc
		if err == nil && len(doc.results) > 0 {
			return doc, nil
		}
		if err == nil {
			err = documentError(doc, errors.New("search page contains no recognized results"), false)
		}
		failures = append(failures, fmt.Errorf("%s: %w", engine, err))
	}
	if ctx.Err() != nil {
		return last, ctx.Err()
	}
	return last, errors.Join(failures...)
}

func fetchSearchPage(ctx context.Context, rawURL, engine string, opts PagedOptions, policy networkPolicy) (fetchedDocument, error) {
	var doc fetchedDocument
	var raw []byte
	var err error
	if engine == "duckduckgo" {
		doc, raw, err = fetchDuckDuckGoPage(ctx, rawURL, opts, policy)
	} else {
		doc, raw, err = fetchHTTP(ctx, rawURL, opts, policy)
	}
	if err == nil && htmlContentType(doc.contentType) {
		doc.results, err = extractSearchResults(ctx, string(raw), doc.url, engine)
		if err == nil {
			doc.content = renderSearchResults(doc.results)
			if node, parseErr := parseDocument(ctx, string(raw)); parseErr == nil {
				if title := firstNode(node, "title"); title != nil {
					doc.title = nodeText(title)
				}
			}
			return doc, nil
		}
	}
	if ctx.Err() != nil {
		return doc, ctx.Err()
	}
	if errors.Is(err, errUnsafeURL) {
		return doc, err
	}
	if engine == "duckduckgo" && opts.SearchPage > 1 {
		// A browser GET with a guessed offset silently repeats page 1.
		if err == nil {
			err = errors.New("search continuation yielded no recognized results")
		}
		return doc, documentError(doc, err, blockedHTML(raw))
	}
	if BrowserAvailable() {
		return fetchBrowser(ctx, rawURL, opts, policy)
	}
	if err == nil {
		err = errors.New("search returned unsupported media or no recognized results")
	}
	return doc, documentError(doc, err, blockedHTML(raw))
}

func nodeText(node *html.Node) string {
	return strings.Join(strings.Fields(dom.TextContent(node)), " ")
}

func shortText(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}

func extractSearchResults(ctx context.Context, raw, rawURL, engine string) ([]searchResult, error) {
	node, err := parseDocument(ctx, raw)
	if err != nil {
		return nil, err
	}
	u, err := validatedURL(rawURL)
	if err != nil {
		return nil, err
	}
	var containers, titleSelector, snippetSelector string
	switch engine {
	case "startpage":
		containers = ".w-gl__result, .result, .web-result, [data-testid=result]"
		titleSelector = "a.w-gl__result-title, .w-gl__result-title a, a.result-link, h2 a, h3 a"
		snippetSelector = ".w-gl__description, .result-description, .result-snippet, .snippet, p"
	case "duckduckgo":
		containers = ".result, article[data-testid=result], [data-testid=result]"
		titleSelector = "a.result__a, a[data-testid=result-title-a], h2 a, h3 a"
		snippetSelector = ".result__snippet, [data-testid=result-snippet], [data-result=snippet]"
	case "brave":
		containers = ".snippet, .web-result, [data-type=web]"
		titleSelector = "a.heading-serpresult, a.title, .title a, h2 a, h3 a"
		snippetSelector = ".snippet-description, .snippet-content, .description, p"
	case "google":
		containers = ".g, .MjjYud"
		titleSelector = "h3"
		snippetSelector = ".VwiC3b, .IsZvec"
	default:
		return nil, errors.New("unsupported search markup")
	}
	seen := map[string]bool{}
	results := []searchResult{}
	for _, container := range queryAll(node, containers) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		link := firstNode(container, titleSelector)
		if link == nil {
			continue
		}
		title := nodeText(link)
		for link != nil && link.Data != "a" {
			link = link.Parent
		}
		if link == nil || title == "" {
			continue
		}
		destination := searchDestination(dom.GetAttribute(link, "href"), u)
		if destination == "" || seen[destination] {
			continue
		}
		seen[destination] = true
		snippet := ""
		if text := firstNode(container, snippetSelector); text != nil {
			snippet = shortText(nodeText(text), 700)
		}
		results = append(results, searchResult{Title: shortText(title, 250), URL: destination, Snippet: snippet})
		if len(results) == 30 {
			break
		}
	}
	if len(results) == 0 {
		return nil, errors.New("search page contains no recognized results (blocked, empty or changed markup)")
	}
	return results, nil
}

func searchDestination(href string, base *url.URL) string {
	reference, err := url.Parse(strings.TrimSpace(href))
	if err != nil || href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u := base.ResolveReference(reference)
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		if target := u.Query().Get("uddg"); target != "" {
			u, err = url.Parse(target)
		}
	} else if u.Hostname() == base.Hostname() && u.Path == "/url" {
		target := u.Query().Get("q")
		if target == "" {
			target = u.Query().Get("url")
		}
		u, err = url.Parse(target)
	}
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, provider := range []string{"startpage.com", "duckduckgo.com", "search.brave.com", "google.com"} {
		if host == provider || strings.HasSuffix(host, "."+provider) {
			return ""
		}
	}
	if len(u.String()) > 4096 {
		return ""
	}
	return cleanLinkURL(u.String())
}

func renderSearchResults(results []searchResult) string {
	var out strings.Builder
	for _, result := range results {
		fmt.Fprintf(&out, "## %s\n\nURL: %s\n\n%s\n\n", result.Title, result.URL, result.Snippet)
	}
	return strings.TrimSpace(out.String())
}

func addSearchResults(payload map[string]any, results []searchResult, budget int) {
	if len(results) == 0 {
		return
	}
	shown := []searchResult{}
	used := 0
	for _, result := range results {
		size := utf8.RuneCountInString(marshalPayload(result))
		if used+size > budget/2 {
			break
		}
		shown = append(shown, result)
		used += size
	}
	payload["results"], payload["results_total"] = shown, len(results)
	if len(shown) < len(results) {
		payload["results_truncated"] = true
	}
}
