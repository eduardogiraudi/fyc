package crawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"runtime"
	"sync"

	"fuckyourcli/config"

	"github.com/mxschmitt/playwright-go"
)

type browserTask struct {
	done chan struct{}
	err  error
}

var (
	mu            sync.Mutex
	sharedBrowser playwright.Browser
	sharedPW      *playwright.Playwright
	sharedProxy   *browserProxy
	browserInit   *browserTask
)

func closeBrowser(browser playwright.Browser, pw *playwright.Playwright, proxy *browserProxy) {
	if browser != nil {
		_ = browser.Close()
	}
	if pw != nil {
		_ = pw.Stop()
	}
	if proxy != nil {
		proxy.close()
	}
}

// Shutdown invalidates an in-progress initialization too, so it cannot resurrect
// a browser after shutdown. Initialization errors and disconnections are retryable.
func Shutdown() {
	mu.Lock()
	browser, pw, proxy := sharedBrowser, sharedPW, sharedProxy
	sharedBrowser, sharedPW, sharedProxy, browserInit = nil, nil, nil, nil
	mu.Unlock()
	closeBrowser(browser, pw, proxy)
}

func initBrowser(ctx context.Context) error {
	return initBrowserWith(ctx, doInit)
}

func initBrowserWith(ctx context.Context, initialize func() (*playwright.Playwright, playwright.Browser, *browserProxy, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mu.Lock()
	if sharedBrowser != nil && sharedBrowser.IsConnected() {
		mu.Unlock()
		return ctx.Err()
	}
	task := browserInit
	if task == nil {
		task = &browserTask{done: make(chan struct{})}
		browserInit = task
		oldBrowser, oldPW, oldProxy := sharedBrowser, sharedPW, sharedProxy
		sharedBrowser, sharedPW, sharedProxy = nil, nil, nil
		go func() {
			closeBrowser(oldBrowser, oldPW, oldProxy)
			pw, browser, proxy, err := initialize()
			mu.Lock()
			if browserInit == task {
				browserInit = nil
				sharedPW, sharedBrowser, sharedProxy = pw, browser, proxy
			} else if err == nil {
				err = errors.New("browser initialization invalidated by shutdown")
			}
			task.err = err
			close(task.done)
			invalidated := sharedBrowser != browser
			mu.Unlock()
			if invalidated {
				closeBrowser(browser, pw, proxy)
			}
		}()
	}
	mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-task.done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return task.err
	}
}

func browserLaunchOptions(path, proxy string, sandbox bool) playwright.BrowserTypeLaunchOptions {
	return playwright.BrowserTypeLaunchOptions{
		ExecutablePath: playwright.String(path), Headless: playwright.Bool(true),
		ChromiumSandbox: playwright.Bool(sandbox), HandleSIGINT: playwright.Bool(false),
		Proxy: &playwright.Proxy{Server: proxy},
		Args: []string{
			"--disable-blink-features=AutomationControlled", "--disable-infobars", "--disable-logging",
			"--disable-dev-shm-usage", "--window-size=1920,1080", "--disable-quic",
			"--proxy-bypass-list=<-loopback>", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		},
	}
}

func doInit() (*playwright.Playwright, playwright.Browser, *browserProxy, error) {
	path, err := findBrowser()
	if err != nil {
		return nil, nil, nil, err
	}
	options := &playwright.RunOptions{SkipInstallBrowsers: true, Stdout: io.Discard, Stderr: io.Discard}
	pw, err := playwright.Run(options)
	if err != nil {
		if installErr := playwright.Install(options); installErr != nil {
			return nil, nil, nil, fmt.Errorf("driver install failed: %w", installErr)
		}
		pw, err = playwright.Run(options)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to start Playwright: %w", err)
		}
	}
	proxy, err := newBrowserProxy(context.Background(), networkPolicy{})
	if err != nil {
		_ = pw.Stop()
		return nil, nil, nil, err
	}
	sandbox := !config.Get().Crawl.DisableSandbox && !(runtime.GOOS == "linux" && os.Geteuid() == 0)
	browser, err := pw.Chromium.Launch(browserLaunchOptions(path, proxy.address(), sandbox))
	if err != nil {
		closeBrowser(nil, pw, proxy)
		return nil, nil, nil, fmt.Errorf("failed to launch sandboxed browser (crawl.disable_sandbox is an explicit opt-in for restricted containers): %w", err)
	}
	return pw, browser, proxy, nil
}

func newPage(ctx context.Context, proxy string, opts PagedOptions) (playwright.Page, error) {
	if err := initBrowser(ctx); err != nil {
		return nil, err
	}
	type outcome struct {
		page playwright.Page
		err  error
	}
	done := make(chan outcome)
	go func() {
		mu.Lock()
		browser := sharedBrowser
		if browser == nil || !browser.IsConnected() || ctx.Err() != nil {
			mu.Unlock()
			select {
			case done <- outcome{err: errors.New("browser disconnected before page creation")}:
			case <-ctx.Done():
			}
			return
		}
		locale := profileLocale()
		if opts.Language != "" {
			locale = opts.Language
		}
		// Leave UA and client hints native: branded browsers must not masquerade
		// as Chrome in HTTP while exposing Edge/Brave brands to JavaScript.
		page, err := browser.NewPage(playwright.BrowserNewPageOptions{
			Locale: playwright.String(locale), TimezoneId: playwright.String(profileTimezone()),
			Viewport: &playwright.Size{Width: 1920, Height: 1080},
			Proxy:    &playwright.Proxy{Server: proxy}, AcceptDownloads: playwright.Bool(false),
			ServiceWorkers: playwright.ServiceWorkerPolicyBlock,
		})
		mu.Unlock()
		if err == nil {
			err = injectStealth(page)
		}
		if err != nil && page != nil {
			_ = page.Context().Close()
			page = nil
		}
		select {
		case done <- outcome{page: page, err: err}:
		case <-ctx.Done():
			if page != nil {
				_ = page.Context().Close()
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		return result.page, result.err
	}
}

const realContentExpr = `() => {
	if (document.getElementById('anubis_challenge') || document.getElementById('anubis_version')) return false;
	if (/making sure you.re not a bot|anubis/i.test(document.title || '')) return false;
	return document.readyState !== 'loading';
}`

const renderStableExpr = `(minimum) => {
	let state = window.__fycRenderState;
	if (!state) {
		state = window.__fycRenderState = {started: Date.now(), changed: Date.now()};
		state.observer = new MutationObserver(() => { state.changed = Date.now(); });
		state.observer.observe(document.documentElement, {subtree: true, childList: true, characterData: true});
	}
	return Date.now() - state.started >= minimum && Date.now() - state.changed >= 350;
}`

func waitRendered(page playwright.Page, minimum, timeout float64) {
	handle, _ := page.WaitForFunction(renderStableExpr, minimum, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(timeout)})
	if handle != nil {
		_ = handle.Dispose()
	}
	_, _ = page.Evaluate(`() => { if (window.__fycRenderState) { window.__fycRenderState.observer.disconnect(); delete window.__fycRenderState; } }`)
}

func boundedPageHTML(page playwright.Page) (string, error) {
	value, err := page.Evaluate(`limit => {
		const html = document.documentElement ? document.documentElement.outerHTML : '';
		if (html.length > limit || new TextEncoder().encode(html).length > limit) return null;
		return html;
	}`, maxRawDocBytes)
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", errDocumentTooLarge
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("browser returned invalid DOM snapshot")
	}
	return text, nil
}

func fetchBrowser(ctx context.Context, rawURL string, opts PagedOptions, policy networkPolicy) (fetchedDocument, error) {
	doc := fetchedDocument{url: rawURL}
	if err := policy.checkURL(ctx, rawURL); err != nil {
		return doc, documentError(doc, err, false)
	}
	proxy, err := newBrowserProxy(ctx, policy)
	if err != nil {
		return doc, err
	}
	defer proxy.close()
	page, err := newPage(ctx, proxy.address(), opts)
	if err != nil {
		return doc, err
	}
	var closeOnce sync.Once
	closePage := func() { closeOnce.Do(func() { _ = page.Context().Close() }) }
	stopClose := context.AfterFunc(ctx, closePage)
	defer func() { stopClose(); closePage() }()
	parsed, _ := url.Parse(rawURL)
	targetHost := parsed.Hostname()
	if err := page.Context().Route("**/*", func(route playwright.Route) {
		req := route.Request()
		if _, err := validatedURL(req.URL()); err != nil || isTrackerRequest(req.URL(), targetHost) {
			_ = route.Abort()
			return
		}
		switch req.ResourceType() {
		case "image", "font", "media", "ping":
			_ = route.Abort()
		default:
			_ = route.Continue()
		}
	}); err != nil {
		return doc, err
	}
	// WebSockets are unnecessary for extraction and otherwise bypass URL routes.
	if err := page.Context().RouteWebSocket("**/*", func(socket playwright.WebSocketRoute) { socket.Close() }); err != nil {
		return doc, err
	}
	var responseMu sync.Mutex
	latest := doc
	page.OnResponse(func(response playwright.Response) {
		if response.Request().IsNavigationRequest() && response.Frame() == page.MainFrame() {
			responseMu.Lock()
			latest.status = response.Status()
			latest.contentType, _, _ = mime.ParseMediaType(response.Headers()["content-type"])
			responseMu.Unlock()
		}
	})
	response, err := page.Goto(rawURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded, Timeout: playwright.Float(30000),
	})
	if err != nil {
		if ctx.Err() != nil {
			return doc, ctx.Err()
		}
		return doc, documentError(doc, err, false)
	}
	if response == nil {
		return doc, documentError(doc, errors.New("navigation returned no HTTP response"), false)
	}
	responseMu.Lock()
	latest.status = response.Status()
	latest.contentType, _, _ = mime.ParseMediaType(response.Headers()["content-type"])
	responseMu.Unlock()
	handle, _ := page.WaitForFunction(realContentExpr, nil, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(20000)})
	if handle != nil {
		_ = handle.Dispose()
	}
	tryAcceptCookies(page)
	waitRendered(page, 1000, 2000)
	raw, err := boundedPageHTML(page)
	if err == nil && needsRendering([]byte(raw), visibleHTMLText(raw)) {
		waitRendered(page, 3000, 6000)
		raw, err = boundedPageHTML(page)
	}
	responseMu.Lock()
	snapshot := latest
	responseMu.Unlock()
	snapshot.url = page.URL()
	if ctx.Err() != nil {
		return snapshot, ctx.Err()
	}
	if err != nil {
		return snapshot, documentError(snapshot, err, false)
	}
	if blockedHTML([]byte(raw)) {
		return snapshot, documentError(snapshot, errors.New("anti-bot challenge or CAPTCHA remains after rendering"), true)
	}
	if snapshot.status >= 400 {
		return snapshot, documentError(snapshot, fmt.Errorf("HTTP status %d", snapshot.status), snapshot.status == 403 || snapshot.status == 429)
	}
	if snapshot.contentType == "" {
		snapshot.contentType = "text/html"
	}
	if !htmlContentType(snapshot.contentType) {
		return snapshot, documentError(snapshot, errors.New("non-HTML browser navigation; use the HTTP document response"), false)
	}
	if engine := serpEngine(snapshot.url); engine != "" {
		snapshot.results, err = extractSearchResults(ctx, raw, snapshot.url, engine)
		if err != nil {
			return snapshot, documentError(snapshot, err, false)
		}
		snapshot.title, _ = page.Title()
		snapshot.title = shortText(snapshot.title, 250)
		snapshot.content = renderSearchResults(snapshot.results)
		return snapshot, nil
	}
	return decodeDocument(ctx, snapshot, []byte(raw))
}

func visibleHTMLText(raw string) string {
	node, err := parseDocument(context.Background(), raw)
	if err != nil {
		return ""
	}
	for _, bad := range queryAll(node, "script, style, noscript, nav, aside, header, footer") {
		if bad.Parent != nil {
			bad.Parent.RemoveChild(bad)
		}
	}
	if main := firstNode(node, "main, [role=main]"); main != nil {
		return nodeText(main)
	}
	if body := firstNode(node, "body"); body != nil {
		return nodeText(body)
	}
	return ""
}
