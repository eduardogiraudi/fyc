package crawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

const (
	maxRawDocBytes   = 8 << 20
	maxExtractBytes  = 4 << 20
	maxDocumentNodes = 100000
)

type fetchedDocument struct {
	content     string
	url         string
	title       string
	contentType string
	status      int
	engine      string
	searchPage  int
	region      string
	language    string
	results     []searchResult
}

func (d fetchedDocument) addMetadata(payload map[string]any) {
	if d.url != "" {
		payload["url"] = d.url
	}
	if d.title != "" {
		payload["title"] = shortText(d.title, 250)
	}
	if d.status != 0 {
		payload["status"] = d.status
	}
	if d.contentType != "" {
		payload["content_type"] = d.contentType
	}
	if d.engine != "" {
		payload["type"], payload["engine"], payload["search_page"] = "serp", d.engine, d.searchPage
		payload["region"], payload["language"] = d.region, d.language
	}
}

// CrawlError retains response metadata even when the document is blocked or invalid.
type CrawlError struct {
	URL         string
	Status      int
	ContentType string
	Blocked     bool
	Cause       error
}

func (e *CrawlError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("crawl %s (HTTP %d, %s): %v", e.URL, e.Status, e.ContentType, e.Cause)
	}
	return fmt.Sprintf("crawl %s: %v", e.URL, e.Cause)
}

func (e *CrawlError) Unwrap() error { return e.Cause }

func documentError(d fetchedDocument, cause error, blocked bool) error {
	return &CrawlError{URL: d.url, Status: d.status, ContentType: d.contentType, Blocked: blocked, Cause: cause}
}

// ErrorResult is the tool-facing error payload, without losing HTTP metadata.
func ErrorResult(err error) string {
	payload := map[string]any{"error": shortText(err.Error(), 4000)}
	var failure *CrawlError
	if errors.As(err, &failure) {
		payload["url"], payload["status"], payload["content_type"] = shortText(failure.URL, 4096), failure.Status, failure.ContentType
		if failure.Blocked {
			payload["blocked"] = true
		}
	}
	return marshalPayload(payload)
}

func fetchDocument(ctx context.Context, rawURL string, opts PagedOptions, policy networkPolicy) (fetchedDocument, error) {
	doc, raw, err := fetchHTTP(ctx, rawURL, opts, policy)
	if ctx.Err() != nil {
		return fetchedDocument{}, ctx.Err()
	}
	if errors.Is(err, errUnsafeURL) {
		return doc, err
	}
	if err == nil {
		if !htmlContentType(doc.contentType) {
			return decodeDocument(ctx, doc, raw)
		}
		if engine := serpEngine(doc.url); engine != "" {
			doc.results, err = extractSearchResults(ctx, string(raw), doc.url, engine)
			if err == nil {
				doc.content = renderSearchResults(doc.results)
				return doc, nil
			}
		} else {
			doc, err = decodeDocument(ctx, doc, raw)
			if err == nil && !needsRendering(raw, doc.content) {
				return doc, nil
			}
		}
	}
	// Only render when HTTP was blocked/failed or produced a JS shell. Do not
	// retry definitive HTTP failures, oversized documents or unsupported media.
	var failure *CrawlError
	if errors.As(err, &failure) && !failure.Blocked && (failure.Status >= 400 || errors.Is(err, errDocumentTooLarge)) {
		return doc, err
	}
	if !BrowserAvailable() {
		if err != nil {
			return doc, err
		}
		return doc, documentError(doc, errors.New("page requires JavaScript rendering; install a supported browser or set browser_paths"), false)
	}
	return fetchBrowser(ctx, rawURL, opts, policy)
}

var errDocumentTooLarge = errors.New("document exceeds crawler size limit")

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errDocumentTooLarge
	}
	return data, nil
}

func fetchHTTP(ctx context.Context, rawURL string, opts PagedOptions, policy networkPolicy) (fetchedDocument, []byte, error) {
	return fetchHTTPRequest(ctx, searchRequest{url: rawURL, method: http.MethodGet}, opts, policy)
}

func fetchHTTPRequest(ctx context.Context, request searchRequest, opts PagedOptions, policy networkPolicy) (fetchedDocument, []byte, error) {
	rawURL := request.url
	doc := fetchedDocument{url: rawURL}
	if err := policy.checkURL(ctx, rawURL); err != nil {
		return doc, nil, documentError(doc, err, false)
	}
	transport := policy.transport()
	defer transport.CloseIdleConnections()
	if len(request.body) > 8192 || (request.method != http.MethodGet && request.method != http.MethodPost) {
		return doc, nil, documentError(doc, errors.New("invalid search continuation request"), false)
	}
	req, err := http.NewRequestWithContext(ctx, request.method, rawURL, strings.NewReader(request.body))
	if err != nil {
		return doc, nil, documentError(doc, err, false)
	}
	locale := profileLocale()
	if opts.Language != "" {
		locale = opts.Language
	}
	profile := buildProfile("", runtime.GOOS, locale, profileTimezone())
	req.Header.Set("User-Agent", profile.UserAgent)
	req.Header.Set("Accept-Language", acceptLanguage(locale))
	req.Header.Set("Accept", "text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.9, application/json;q=0.8, text/plain;q=0.8, application/pdf;q=0.7, */*;q=0.5")
	if request.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := policy.client(transport).Do(req)
	if err != nil {
		return doc, nil, documentError(doc, err, false)
	}
	defer resp.Body.Close()
	doc.url, doc.status = resp.Request.URL.String(), resp.StatusCode
	doc.contentType, _, _ = mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.ContentLength > maxRawDocBytes {
		return doc, nil, documentError(doc, errDocumentTooLarge, false)
	}
	raw, err := readBounded(resp.Body, maxRawDocBytes)
	if err != nil {
		return doc, nil, documentError(doc, err, false)
	}
	if doc.contentType == "" {
		doc.contentType, _, _ = mime.ParseMediaType(http.DetectContentType(raw))
	}
	blocked := htmlContentType(doc.contentType) && blockedHTML(raw)
	if doc.status >= 400 {
		return doc, raw, documentError(doc, errors.New(http.StatusText(doc.status)), blocked || doc.status == 403 || doc.status == 429)
	}
	if blocked {
		return doc, raw, documentError(doc, errors.New("anti-bot challenge or CAPTCHA did not yield readable content"), true)
	}
	if htmlContentType(doc.contentType) {
		decoded, err := charset.NewReader(bytes.NewReader(raw), resp.Header.Get("Content-Type"))
		if err != nil {
			return doc, raw, documentError(doc, err, false)
		}
		raw, err = readBounded(decoded, maxRawDocBytes)
		if err != nil {
			return doc, nil, documentError(doc, err, false)
		}
	}
	return doc, raw, nil
}

func htmlContentType(kind string) bool {
	return kind == "text/html" || kind == "application/xhtml+xml"
}

func decodeDocument(ctx context.Context, doc fetchedDocument, raw []byte) (fetchedDocument, error) {
	if err := ctx.Err(); err != nil {
		return doc, err
	}
	var err error
	switch {
	case htmlContentType(doc.contentType):
		doc.content, doc.title, err = extractMarkdown(ctx, string(raw), doc.url)
	case doc.contentType == "application/pdf" || bytes.HasPrefix(raw, []byte("%PDF-")):
		doc.contentType = "application/pdf"
		doc.content, err = extractPDF(ctx, raw)
	case doc.contentType == "application/json" || strings.HasSuffix(doc.contentType, "+json"):
		var out bytes.Buffer
		if json.Indent(&out, raw, "", "  ") == nil {
			doc.content = out.String()
		} else {
			doc.content = string(raw)
		}
	case strings.HasPrefix(doc.contentType, "text/") || doc.contentType == "application/xml" || strings.HasSuffix(doc.contentType, "+xml") ||
		(doc.contentType == "application/octet-stream" && utf8.Valid(raw) && !bytes.ContainsRune(raw, '\x00')):
		if !utf8.Valid(raw) {
			decoded, decodeErr := charset.NewReader(bytes.NewReader(raw), doc.contentType)
			if decodeErr != nil {
				err = decodeErr
				break
			}
			raw, err = readBounded(decoded, maxRawDocBytes)
		}
		doc.content = string(raw)
	default:
		err = fmt.Errorf("unsupported Content-Type %q", doc.contentType)
	}
	if ctx.Err() != nil {
		return doc, ctx.Err()
	}
	if err != nil {
		return doc, documentError(doc, err, false)
	}
	if len(doc.content) > maxExtractBytes {
		return doc, documentError(doc, errDocumentTooLarge, false)
	}
	if strings.TrimSpace(doc.content) == "" {
		return doc, documentError(doc, errors.New("document contains no readable text"), false)
	}
	return doc, nil
}

func needsRendering(raw []byte, content string) bool {
	low := strings.ToLower(string(raw))
	if blockedHTML(raw) {
		return true
	}
	return len(strings.TrimSpace(content)) < 160 && (strings.Contains(low, "<script") || strings.Contains(low, "enable javascript"))
}
