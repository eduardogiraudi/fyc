package crawl

import (
	"context"
	"errors"
	"fmt"
	nurl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"fuckyourcli/config"

	"github.com/go-shiori/dom"
	"github.com/mxschmitt/playwright-go"
	"golang.org/x/net/html"
)

const (
	defaultPageChars = 24000
	minPageChars     = 2000
	maxPageChars     = 200000
)

// PagedOptions separates document windows from search-engine pagination.
type PagedOptions struct {
	Page       int
	MaxChars   int
	Find       string
	SearchPage int
	Region     string
	Language   string
}

// Crawl returns the first document window. Context-aware callers should use
// CrawlPagedOptsContext so cancellation reaches HTTP requests and browser pages.
func Crawl(input, engine string) (string, error) {
	return CrawlPaged(input, engine, 1, 0)
}

func CrawlPaged(input, engine string, page, maxChars int) (string, error) {
	if page < 1 {
		page = 1
	}
	return CrawlPagedOpts(input, engine, PagedOptions{Page: page, MaxChars: maxChars})
}

func CrawlPagedOpts(input, engine string, opts PagedOptions) (string, error) {
	return CrawlPagedOptsContext(context.Background(), input, engine, opts)
}

// CrawlPagedOptsContext preserves document metadata across cached windows and find.
func CrawlPagedOptsContext(ctx context.Context, input, engine string, opts PagedOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	input = strings.TrimSpace(input)
	if len(input) > 4096 {
		return "", errors.New("URL or search query exceeds 4096-byte limit")
	}
	if input == "" {
		return "", errors.New("input must be a URL or non-empty search query")
	}
	if opts.Page < 0 {
		return "", &PageOutOfRangeError{Page: opts.Page, Pages: 1}
	}
	var err error
	opts, err = normalizeSearchOptions(opts)
	if err != nil {
		return "", err
	}
	policy := networkPolicy{allowPrivate: config.Get().Crawl.AllowPrivateNetworks}
	result, err := cachedDocument(ctx, input, engine, opts, policy)
	if err != nil {
		return "", err
	}
	doc := newPagedDoc(result.content, normalizeMaxChars(opts.MaxChars))
	payload := map[string]any{
		"type": "crawl", "pages": doc.pages(), "total_chars": doc.total(),
		"truncated": doc.pages() > 1,
	}
	result.addMetadata(payload)
	if q := strings.TrimSpace(opts.Find); q != "" {
		payload["find"] = doc.find(q)
		if opts.Page == 0 {
			return marshalPayload(payload), ctx.Err()
		}
	}
	page := opts.Page
	if page == 0 {
		page = 1
	}
	if page > doc.pages() {
		return "", &PageOutOfRangeError{Page: page, Pages: doc.pages()}
	}
	chunk, info := doc.window(page)
	payload["page"], payload["content"] = info.page, chunk
	if info.page == 1 {
		addSearchResults(payload, result.results, normalizeMaxChars(opts.MaxChars))
	}
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
	return marshalPayload(payload), ctx.Err()
}

func normalizeMaxChars(v int) int {
	if v <= 0 {
		v, _ = strconv.Atoi(strings.TrimSpace(os.Getenv("FYC_CRAWL_MAX_CHARS")))
	}
	if v <= 0 {
		v = defaultPageChars
	}
	return max(minPageChars, min(maxPageChars, v))
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
			{"/snap/bin/chromium"},
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

	// Playwright-managed browsers are a last resort, never preferred over a
	// user override or a supported system installation.
	cacheRoots := []string{os.Getenv("PLAYWRIGHT_BROWSERS_PATH")}
	if root, err := os.UserCacheDir(); err == nil {
		cacheRoots = append(cacheRoots, filepath.Join(root, "ms-playwright"))
	}
	for _, root := range cacheRoots {
		if root == "" || root == "0" {
			continue
		}
		for _, pattern := range []string{
			"chromium-*/chrome-linux/chrome", "chromium-*/chrome-linux64/chrome",
			"chromium-*/chrome-win/chrome.exe", "chromium-*/chrome-mac/Chromium.app/Contents/MacOS/Chromium",
		} {
			matches, _ := filepath.Glob(filepath.Join(root, pattern))
			for i := len(matches) - 1; i >= 0; i-- {
				candidates = append(candidates, browserCandidate{matches[i]})
			}
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
