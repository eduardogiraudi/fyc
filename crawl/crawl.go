package crawl

import (
	"errors"
	"fmt"
	nurl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	"golang.org/x/net/html"

	"github.com/go-shiori/dom"
	readability "github.com/go-shiori/go-readability"
	"github.com/mxschmitt/playwright-go"

	"fuckyourcli/config"
)

// ---------------------------------------------------------------------------
// Browser singleton — lazy-initialized on first Crawl call, reused forever.
// ---------------------------------------------------------------------------

var (
	mu              sync.Mutex
	sharedBrowser   playwright.Browser
	sharedPW        *playwright.Playwright
	browserInitErr  error
	browserInitDone bool
	sharedProfile   browserProfile // UA / client hints / locale derived from the real browser
)

// suppressStderr redirects os.Stderr to os.DevNull to prevent
// Playwright or Chromium from writing ANSI sequences to the terminal
// while Bubble Tea is in raw mode.
func suppressStderr() func() {
	realStderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return func() {}
	}
	os.Stderr = devNull
	return func() {
		os.Stderr = realStderr
		devNull.Close()
	}
}

func initBrowserOnce() error {
	mu.Lock()
	defer mu.Unlock()

	if browserInitDone {
		return browserInitErr
	}

	pw, browser, err := doInit()
	if err != nil {
		browserInitErr = err
		return err
	}

	sharedPW = pw
	sharedBrowser = browser
	sharedProfile = buildProfile(browser.Version(), runtime.GOOS, profileLocale(), profileTimezone())
	browserInitDone = true
	browserInitErr = nil
	return nil
}

// doInit contains the actual browser initialization logic, extracted so
// initBrowserOnce can retry on failure.
func doInit() (*playwright.Playwright, playwright.Browser, error) {
	defer suppressStderr()()
	// 1. Force skipping browser download (downloads only the driver ~20MB)
	os.Setenv("PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD", "1")

	// 2. Automatic installation of only the driver on first run
	if err := playwright.Install(&playwright.RunOptions{
		SkipInstallBrowsers: true,
	}); err != nil {
		return nil, nil, fmt.Errorf("driver install failed: %w", err)
	}

	// 3. Start Playwright
	pw, err := playwright.Run()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start Playwright: %w", err)
	}

	browserPath, err := findBrowser()
	if err != nil {
		pw.Stop()
		return nil, nil, fmt.Errorf("no supported browser found: %w", err)
	}

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		ExecutablePath: playwright.String(browserPath),
		Headless:       playwright.Bool(true),
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--disable-infobars",
			"--disable-logging",
			"--disable-dev-shm-usage",
			"--no-sandbox",
			"--disable-setuid-sandbox",
			"--disable-web-security",
			"--disable-features=IsolateOrigins,site-per-process",
			"--window-size=1920,1080",
		},
	})
	if err != nil {
		pw.Stop()
		return nil, nil, fmt.Errorf("failed to launch browser: %w (%s)", err, browserPath)
	}

	return pw, browser, nil
}

// ---------------------------------------------------------------------------
// Shutdown — releases shared browser resources.
// Can be called by main on process exit.
// After Shutdown, a subsequent call to Crawl will re-initialize the browser.
// ---------------------------------------------------------------------------

func Shutdown() {
	mu.Lock()
	defer mu.Unlock()
	if sharedBrowser != nil {
		sharedBrowser.Close()
		sharedBrowser = nil
	}
	if sharedPW != nil {
		sharedPW.Stop()
		sharedPW = nil
	}
	browserInitDone = false
	browserInitErr = nil
}

// newPage creates a new page on the shared browser with stealth already injected.
func newPage() (playwright.Page, error) {
	if err := initBrowserOnce(); err != nil {
		return nil, err
	}

	profile := sharedProfile
	page, err := sharedBrowser.NewPage(playwright.BrowserNewPageOptions{
		UserAgent:  playwright.String(profile.UserAgent),
		Locale:     playwright.String(profile.Locale),
		TimezoneId: playwright.String(profile.Timezone),
		Viewport: &playwright.Size{
			Width:  1920,
			Height: 1080,
		},
		ExtraHttpHeaders: profile.Headers,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create page: %w", err)
	}

	// Inject stealth on every new page
	if err := injectStealth(page); err != nil {
		page.Close()
		return nil, fmt.Errorf("stealth injection failed: %w", err)
	}

	return page, nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Page size defaults for the paged crawl output. A single tool result is
// capped so a long page cannot blow up the model context (KV cache): the model
// reads the document one window at a time through the `page` parameter.
const (
	defaultPageChars = 24000
	minPageChars     = 2000
	maxPageChars     = 200000
)

// Result cache — keeps the last few cleaned documents so paging through a long
// page does not re-fetch the network (and re-slice a possibly changed page) on
// every call. Bounded by entry count, per-document size and TTL.
const (
	cacheMaxEntries   = 32
	cacheTTL          = 10 * time.Minute
	maxCachedDocBytes = 1 << 20
)

type cacheEntry struct {
	content string
	stored  time.Time
}

var (
	cacheMu     sync.Mutex
	resultCache = map[string]cacheEntry{}
)

func cacheGet(key string) (string, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	e, ok := resultCache[key]
	if !ok {
		return "", false
	}
	if time.Since(e.stored) > cacheTTL {
		delete(resultCache, key)
		return "", false
	}
	return e.content, true
}

func cachePut(key, content string) {
	// Very large documents are not cached: a paged read then re-fetches them
	// rather than holding tens of megabytes in memory.
	if len(content) > maxCachedDocBytes {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _, exists := resultCache[key]; !exists && len(resultCache) >= cacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for k, e := range resultCache {
			if oldestKey == "" || e.stored.Before(oldest) {
				oldestKey, oldest = k, e.stored
			}
		}
		delete(resultCache, oldestKey)
	}
	resultCache[key] = cacheEntry{content: content, stored: time.Now()}
}

// Crawl crawls a URL or runs a web search and returns the first page of the
// result as JSON. It is a convenience wrapper around CrawlPaged for callers
// that never page.
func Crawl(input, engine string) (string, error) {
	return CrawlPaged(input, engine, 1, 0)
}

// PagedOptions selects what CrawlPagedOpts returns.
type PagedOptions struct {
	// Page is the 1-based window to return; 0 means "not specified"
	// (page 1, or matches only when Find is set).
	Page int
	// MaxChars caps the window size (0 → FYC_CRAWL_MAX_CHARS or default).
	MaxChars int
	// Find, if set, searches the whole document (case-insensitive) and
	// reports the pages and snippets where the term occurs. Without an
	// explicit Page only the matches are returned, no content.
	Find string
}

// CrawlPaged crawls a URL or runs a web search and returns a single window of
// the cleaned result. It is kept for existing callers; see CrawlPagedOpts.
// Pages below 1 are treated as 1; pages past the end return a
// *PageOutOfRangeError.
func CrawlPaged(input, engine string, page, maxChars int) (string, error) {
	if page < 1 {
		page = 1
	}
	return CrawlPagedOpts(input, engine, PagedOptions{Page: page, MaxChars: maxChars})
}

// CrawlPagedOpts crawls a URL or runs a web search and returns one window of
// the cleaned result as JSON, so a long document never floods the model
// context. The payload always carries `pages`, `total_chars` and `truncated`;
// plus, depending on the request:
//   - `page`, `content` and (when more pages follow) `next_page`;
//   - `outline` (headings with their page) on page 1 of a multi-page document;
//   - `find` (matching pages and snippets) when opts.Find is set.
//
// A page outside 1..pages is an error, never a silent fallback.
func CrawlPagedOpts(input, engine string, opts PagedOptions) (string, error) {
	resultType := "serp"
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		resultType = "crawl"
	}

	content, err := crawlCached(input, engine)
	if err != nil {
		return "", err
	}

	doc := newPagedDoc(content, normalizeMaxChars(opts.MaxChars))
	payload := map[string]any{
		"type":        resultType,
		"pages":       doc.pages(),
		"total_chars": doc.total(),
		"truncated":   doc.pages() > 1,
	}

	if q := strings.TrimSpace(opts.Find); q != "" {
		payload["find"] = doc.find(q)
		if opts.Page == 0 {
			// Search only: the model picks a page from the matches.
			return marshalPayload(payload), nil
		}
	}

	page := opts.Page
	if page == 0 {
		page = 1
	}
	if page < 1 || page > doc.pages() {
		return "", &PageOutOfRangeError{Page: page, Pages: doc.pages()}
	}

	chunk, info := doc.window(page)
	payload["page"] = info.page
	payload["content"] = chunk
	if info.page < info.pages {
		payload["next_page"] = info.page + 1
	}
	if info.page == 1 && info.pages > 1 {
		if outline, cut := doc.outline(); len(outline) > 0 {
			payload["outline"] = outline
			if cut {
				payload["outline_truncated"] = true
			}
		}
	}
	return marshalPayload(payload), nil
}

// crawlCached returns the full cleaned document for input/engine, reusing the
// cached copy when available so paged reads are cheap and consistent.
func crawlCached(input, engine string) (string, error) {
	key := strings.ToLower(engine) + "\x00" + input
	if content, ok := cacheGet(key); ok {
		return content, nil
	}
	content, err := crawlRaw(input, engine)
	if err != nil {
		return "", err
	}
	cachePut(key, content)
	return content, nil
}

// normalizeMaxChars resolves the per-page character budget: an explicit value
// wins, then the FYC_CRAWL_MAX_CHARS environment variable, then the default.
// The result is always clamped to [minPageChars, maxPageChars].
func normalizeMaxChars(v int) int {
	if v <= 0 {
		if env := strings.TrimSpace(os.Getenv("FYC_CRAWL_MAX_CHARS")); env != "" {
			if n, err := strconv.Atoi(env); err == nil {
				v = n
			}
		}
	}
	if v <= 0 {
		v = defaultPageChars
	}
	if v < minPageChars {
		return minPageChars
	}
	if v > maxPageChars {
		return maxPageChars
	}
	return v
}

// crawlRaw performs the actual browser crawl/search and returns the cleaned
// markdown. The `page` windowing is applied by the callers.
func crawlRaw(input, engine string) (string, error) {
	defer suppressStderr()()
	page, err := newPage()
	if err != nil {
		return "", err
	}
	defer page.Close()

	var result string
	targetHost := ""
	if u, perr := nurl.Parse(input); perr == nil && (u.Scheme == "http" || u.Scheme == "https") {
		targetHost = u.Hostname()
	}
	page.Route("**/*", func(route playwright.Route) {
		req := route.Request()
		switch req.ResourceType() {
		case "image", "stylesheet", "font", "media", "ping", "other":
			route.Abort()
			return
		}
		if isTrackerRequest(req.URL(), targetHost) {
			route.Abort()
			return
		}
		route.Continue()
	})
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		result, err = crawlPage(page, input)
	} else {
		switch strings.ToLower(engine) {
		case "duckduckgo":
			result, err = searchDuckDuckGo(page, input)
		case "startpage":
			result, err = searchStartpage(page, input)
			if err == nil && strings.TrimSpace(result) == "" {
				result, err = searchDuckDuckGo(page, input)
			}
		case "brave":
			result, err = searchBrave(page, input)
		default:
			result, err = searchStartpage(page, input)
			if err != nil || strings.TrimSpace(result) == "" {
				result, err = searchDuckDuckGo(page, input)
			}
			if err != nil || strings.TrimSpace(result) == "" {
				result, err = searchBrave(page, input)
			}
		}
	}
	if err != nil {
		return "", err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Browser discovery
// ---------------------------------------------------------------------------
// DetectBrowser returns the browser executable that would be launched (the
// FYC_BROWSER_PATH override, then the configured browser_paths, then the
// built-in candidates) without launching it.
func DetectBrowser() (string, error) {
	return findBrowser()
}

// BrowserAvailable reports whether a usable browser executable is present among
// the configured and built-in paths. It never launches the browser.
func BrowserAvailable() bool {
	_, err := findBrowser()
	return err == nil
}

// browserExecutable reports whether path points to a usable browser file.
func browserExecutable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func findBrowser() (string, error) {
	// Explicit override (useful for tests or non-standard installations).
	if p := strings.TrimSpace(os.Getenv("FYC_BROWSER_PATH")); p != "" {
		if browserExecutable(p) {
			return p, nil
		}
	}

	// User-configured paths (config.json: browser_paths) win over the built-in
	// candidates below, so a non-canonical installation is picked up.
	for _, p := range config.Get().BrowserPaths {
		if browserExecutable(p) {
			return p, nil
		}
	}

	type browserCandidate struct {
		path string
	}

	var candidates []browserCandidate

	switch runtime.GOOS {
	case "linux":
		candidates = []browserCandidate{
			{"/snap/bin/chromium"},
			{"/usr/bin/google-chrome"},
			{"/usr/bin/google-chrome-stable"},
			{"/usr/bin/chromium-browser"},
			{"/usr/bin/chromium"},
			{"/usr/bin/brave-browser"},
			{"/usr/bin/brave"},
			{"/usr/bin/microsoft-edge"},
			{"/usr/bin/microsoft-edge-stable"},
			{"/usr/bin/opera"},
			{"/usr/bin/vivaldi"},
		}
	case "darwin":
		candidates = []browserCandidate{
			{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
			{"/Applications/Chromium.app/Contents/MacOS/Chromium"},
			{"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"},
			{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"},
			{"/Applications/Opera.app/Contents/MacOS/Opera"},
			{"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi"},
			{"/Applications/Arc.app/Contents/MacOS/Arc"},
		}
	case "windows":
		// On Windows, browsers are not in the PATH: they must be searched for in
		// standard installation paths under the program files environment variables.
		bases := []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("ProgramW6432"),
			os.Getenv("LOCALAPPDATA"),
		}
		winRel := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Chromium\Application\chrome.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`Vivaldi\Application\vivaldi.exe`,
			`Opera\launcher.exe`,
			`Opera GX\launcher.exe`,
		}
		for _, base := range bases {
			if strings.TrimSpace(base) == "" {
				continue
			}
			for _, rel := range winRel {
				candidates = append(candidates, browserCandidate{filepath.Join(base, rel)})
			}
		}
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	pathCandidates := []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium-browser",
		"chromium",
		"brave-browser",
		"microsoft-edge",
		"opera",
		"vivaldi",
	}
	if runtime.GOOS == "windows" {
		// Names without extension: exec.LookPath adds PATHEXT (.exe).
		pathCandidates = append(pathCandidates,
			"chrome",
			"msedge",
			"brave",
			"vivaldi",
			"opera",
		)
	}

	for _, name := range pathCandidates {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, browserCandidate{p})
		}
	}

	visited := make(map[string]bool)
	for _, c := range candidates {
		if visited[c.path] {
			continue
		}
		visited[c.path] = true
		info, err := os.Stat(c.path)
		if err == nil && !info.IsDir() {
			return c.path, nil
		}
	}

	return "", errors.New("no supported browser found. Install Chrome, Chromium, Brave, Edge, Opera, Vivaldi or Arc")
}

// ---------------------------------------------------------------------------
// Page crawling & search helpers
// ---------------------------------------------------------------------------
// realContentExpr is a JS predicate that returns true when an auto-solving
// anti-bot challenge (currently Anubis, used by Startpage and Brave) has
// been passed. On the Anubis interstitial, the page contains a
// <script id="anubis_challenge"> tag and the proof-of-work is calculated on the
// client side before redirecting to the actual content. Reading the DOM before
// that moment returns the "Verifying your request..." placeholder instead of
// the results.
const realContentExpr = `() => {
	try {
		if (document.getElementById('anubis_challenge')) return false;
		if (document.getElementById('anubis_version')) return false;
		const t = document.title || '';
		if (/making sure you.re not a bot|anubis/i.test(t)) return false;
		const b = document.body ? document.body.innerText : '';
		if (/verifying your request/i.test(b)) return false;
		return document.readyState !== 'loading';
	} catch (e) {
		return false;
	}
}`

// settleBotChallenge waits for the end of an auto-solving anti-bot challenge.
// It's a no-op on pages that don't have one: the predicate is already true as soon
// as the initial DOMContentLoaded fires. In case of timeout it is
// non-fatal: extraction is attempted anyway (Crawl falls back to another
// engine if the result is empty).
func settleBotChallenge(page playwright.Page) {
	_, err := page.WaitForFunction(realContentExpr, nil, playwright.PageWaitForFunctionOptions{
		Timeout: playwright.Float(20000),
	})
	_ = err
}

func crawlPage(page playwright.Page, urlStr string) (string, error) {
	_, err := page.Goto(urlStr, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	})
	if err != nil {
		return "", fmt.Errorf("navigation error: %w", err)
	}

	// Wait for any auto-solving anti-bot challenges (Anubis) to be
	// passed before reading the DOM.
	settleBotChallenge(page)

	tryAcceptCookies(page)

	rawHTML, err := page.Content()
	if err != nil {
		return "", fmt.Errorf("failed to get page HTML: %w", err)
	}

	var cleanedHTML string
	if isSERP(urlStr) {
		cleanedHTML, err = cleanSERPHTML(rawHTML, urlStr)
		if err != nil {
			return "", fmt.Errorf("SERP cleanup error: %w", err)
		}
	} else {
		cleanedHTML, err = extractPageHTML(rawHTML, urlStr)
		if err != nil {
			return "", fmt.Errorf("page extraction error: %w", err)
		}
	}

	markdown, err := htmltomarkdown.ConvertString(cleanedHTML)
	if err != nil {
		return "", fmt.Errorf("markdown conversion error: %w", err)
	}

	markdown = cleanMarkdown(markdown)
	return markdown, nil
}

// consentClickJS looks for a cookie-consent "accept" button and clicks it, all
// in a single page round-trip. It first tries selectors of well-known consent
// managers; failing that, it accepts only buttons whose whole text is an
// "accept"-style phrase AND that sit inside something that looks like a
// consent dialog, so "Accept invitation" or a newsletter "OK" are left alone.
// It returns the matched selector / label, or "" when nothing was clicked.
const consentClickJS = `() => {
	const visible = (el) => {
		if (!el || !el.getClientRects || el.getClientRects().length === 0) return false;
		const cs = getComputedStyle(el);
		return cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0';
	};
	const click = (el, how) => { try { el.click(); return how; } catch (e) { return ''; } };

	const known = [
		'#onetrust-accept-btn-handler',
		'#CybotCookiebotDialogBodyLevelButtonLevelOptinAllowAll',
		'#CybotCookiebotDialogBodyButtonAccept',
		'#didomi-notice-agree-button',
		'.iubenda-cs-accept-btn',
		'#truste-consent-button',
		'.fc-cta-consent',
		'button.sp_choice_type_11',
		'#L2AGLb',
		'.cmplz-accept',
		'.cc-allow',
		'[data-testid="cookie-policy-accept"]',
		'[data-testid="uc-accept-all-button"]',
		'button[data-cookiebanner="accept_button"]'
	];
	for (const sel of known) {
		const el = document.querySelector(sel);
		if (visible(el)) return click(el, sel);
	}

	const containerRe = /cookie|consent|gdpr|privacy|cmp|onetrust|didomi|iubenda|cookiebot|trustarc|quantcast|usercentrics|sp_message|cc-window|cc-banner/i;
	const acceptRe = /^(accept( all)?( (the )?cookies)?|accept (and|&) close|i accept|i agree|agree( (and|&) close)?|allow all( cookies)?|allow cookies|got it|ok|okay|accetta( tutti)?( i cookie| e chiudi)?|accetto|accetta e continua|consenti( tutti)?( i cookie)?|acconsento|va bene|ho capito|tout accepter|accepter( et fermer)?|accepter tout|alle akzeptieren|akzeptieren|zustimmen|einverstanden|aceptar( todo| todas)?|aceptar y cerrar|aceitar( tudo| todos)?|accepteren|alles accepteren)$/i;

	const inConsent = (el) => {
		for (let n = el, i = 0; n && n.nodeType === 1 && i < 10; n = n.parentElement, i++) {
			const sig = (n.id || '') + ' ' + (typeof n.className === 'string' ? n.className : '') + ' ' + (n.getAttribute('aria-label') || '');
			if (containerRe.test(sig)) return true;
			if (n.getAttribute('role') === 'dialog' || n.getAttribute('aria-modal') === 'true') {
				if (containerRe.test((n.innerText || n.textContent || '').slice(0, 600))) return true;
			}
		}
		return false;
	};

	const candidates = document.querySelectorAll('button, [role="button"], a[href], input[type="button"], input[type="submit"]');
	for (const el of candidates) {
		if (!visible(el)) continue;
		const raw = el.innerText || el.textContent || el.value || el.getAttribute('aria-label') || '';
		const text = raw.trim().toLowerCase().replace(/\s+/g, ' ').replace(/[.!>»›→]+$/, '').trim();
		if (!text || text.length > 40 || !acceptRe.test(text)) continue;
		if (!inConsent(el)) continue;
		return click(el, text);
	}
	return '';
}`

// tryAcceptCookies dismisses a cookie-consent banner with one Evaluate call.
// Consent managers rendered inside cross-origin iframes are not reached.
func tryAcceptCookies(page playwright.Page) {
	defer func() { recover() }()
	res, err := page.Evaluate(consentClickJS)
	if s, ok := res.(string); (ok && s != "") || err != nil {
		// A click can trigger a reload (consent walls) or an execution
		// context error; give the page a moment to settle either way.
		page.WaitForTimeout(400)
	}
}

func absoluteLinks(doc *html.Node, baseURL *nurl.URL) {
	for _, n := range dom.GetElementsByTagName(doc, "a") {
		href := dom.GetAttribute(n, "href")
		if href == "" || strings.HasPrefix(href, "#") ||
			strings.HasPrefix(href, "mailto:") ||
			strings.HasPrefix(href, "javascript:") {
			continue
		}
		u, err := nurl.Parse(href)
		if err != nil {
			continue
		}
		if !u.IsAbs() {
			absURL := baseURL.ResolveReference(u)
			dom.SetAttribute(n, "href", absURL.String())
		}
	}
}

func extractPageHTML(rawHTML string, urlStr string) (string, error) {
	parsedURL, err := nurl.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	doc, err := dom.FastParse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("failed to parse raw HTML: %w", err)
	}
	absoluteLinks(doc, parsedURL)
	var buf strings.Builder
	if err := html.Render(&buf, doc); err != nil {
		return "", fmt.Errorf("failed to render HTML: %w", err)
	}
	preProcessedHTML := buf.String()

	article, err := readability.FromReader(strings.NewReader(preProcessedHTML), parsedURL)
	if err != nil {
		return "", fmt.Errorf("readability extraction: %w", err)
	}
	if article.Content == "" {
		return "", fmt.Errorf("extraction returned no content")
	}

	return article.Content, nil
}

func cleanSERPHTML(rawHTML string, urlStr string) (string, error) {
	doc, err := dom.FastParse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("HTML parsing: %w", err)
	}

	killTags := []string{
		"script", "style", "noscript", "iframe", "link", "meta", "svg",
		"img", "input", "textarea", "button", "select", "option", "label",
		"header", "footer", "nav", "aside", "form", "template", "canvas",
	}
	for _, tag := range killTags {
		nodes := dom.GetElementsByTagName(doc, tag)
		dom.RemoveNodes(nodes, func(n *html.Node) bool { return true })
	}

	serpHost := ""
	if parsed, err := nurl.Parse(urlStr); err == nil {
		serpHost = strings.ToLower(parsed.Hostname())
	}
	for _, n := range dom.GetElementsByTagName(doc, "a") {
		href := strings.TrimSpace(dom.GetAttribute(n, "href"))
		hrefLower := strings.ToLower(href)
		linkText := strings.TrimSpace(dom.TextContent(n))
		switch {
		case strings.Contains(linkText, "Anonymous View"):
			dom.DetachChild(n)
		case strings.HasPrefix(hrefLower, "https://eu3-browse.startpage.com") || strings.Contains(hrefLower, "browse.startpage.com"):
			dom.DetachChild(n)
		case strings.Contains(linkText, "›") || strings.Contains(linkText, "://"):
			dom.DetachChild(n)
		case href == "" || href == "#":
			dom.DetachChild(n)
		case strings.HasPrefix(hrefLower, "javascript:"):
			dom.DetachChild(n)
		case strings.HasPrefix(href, "/") || strings.HasPrefix(href, "?"):
			dom.DetachChild(n)
		case serpHost != "" && strings.Contains(hrefLower, serpHost):
			dom.DetachChild(n)
		}
	}

	killExact := map[string]bool{"footer": true, "header": true, "nav": true, "sidebar": true, "menu": true}
	for {
		removed := 0
	nodeLoop:
		for _, n := range dom.GetElementsByTagName(doc, "*") {
			if n.Type == html.ElementNode && n.Parent != nil {
				for _, a := range n.Attr {
					key := strings.ToLower(a.Key)
					if key != "class" && key != "id" {
						continue
					}
					for _, v := range strings.Fields(strings.ToLower(a.Val)) {
						if killExact[v] {
							dom.DetachChild(n)
							removed++
							continue nodeLoop
						}
					}
				}
			}
		}
		if removed == 0 {
			break
		}
	}

	for {
		removed := 0
		for _, n := range dom.GetElementsByTagName(doc, "*") {
			if n.Type == html.ElementNode && n.Parent != nil {
				text := strings.TrimSpace(dom.TextContent(n))
				if text == "" {
					dom.DetachChild(n)
					removed++
				}
			}
		}
		if removed == 0 {
			break
		}
	}

	body := dom.GetElementsByTagName(doc, "body")
	if len(body) > 0 {
		return dom.InnerHTML(body[0]), nil
	}
	return dom.InnerHTML(doc), nil
}

func isSERP(urlStr string) bool {
	parsed, err := nurl.Parse(urlStr)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case strings.Contains(host, "google."):
		return true
	case strings.Contains(host, "duckduckgo."):
		return true
	case strings.Contains(host, "search.brave."):
		return true
	case strings.Contains(host, "startpage."):
		return true
	}
	return false
}

func searchDuckDuckGo(page playwright.Page, query string) (string, error) {
	searchURL := "https://duckduckgo.com/?q=" + nurl.QueryEscape(query)
	return crawlPage(page, searchURL)
}

func searchBrave(page playwright.Page, query string) (string, error) {
	searchURL := "https://search.brave.com/search?q=" + nurl.QueryEscape(query)
	return crawlPage(page, searchURL)
}

func searchStartpage(page playwright.Page, query string) (string, error) {
	searchURL := "https://www.startpage.com/do/dsearch?q=" + nurl.QueryEscape(query)
	return crawlPage(page, searchURL)
}
