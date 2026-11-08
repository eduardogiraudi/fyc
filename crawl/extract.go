package crawl

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/andybalholm/cascadia"
	"github.com/go-shiori/dom"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func queryAll(node *html.Node, selector string) []*html.Node {
	matcher, err := cascadia.Compile(selector)
	if err != nil {
		return nil
	}
	return cascadia.QueryAll(node, matcher)
}

func firstNode(node *html.Node, selector string) *html.Node {
	matches := queryAll(node, selector)
	if len(matches) == 0 {
		return nil
	}
	return matches[0]
}

func parseDocument(ctx context.Context, raw string) (*html.Node, error) {
	if len(raw) > maxRawDocBytes {
		return nil, errDocumentTooLarge
	}
	node, err := html.Parse(contextReader{ctx, strings.NewReader(raw)})
	if err != nil {
		return nil, err
	}
	stack := []*html.Node{node}
	for count := 0; len(stack) > 0; count++ {
		if count >= maxDocumentNodes {
			return nil, errDocumentTooLarge
		}
		if count%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			stack = append(stack, child)
		}
	}
	return node, nil
}

func toMarkdown(ctx context.Context, cleaned string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(table.WithHeaderPromotion(true)),
	))
	md, err := conv.ConvertString(cleaned)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	if len(md) > maxExtractBytes {
		return "", errDocumentTooLarge
	}
	return cleanMarkdown(md), nil
}

func extractPageHTML(rawHTML, urlStr string) (string, error) {
	cleaned, _, err := extractHTML(context.Background(), rawHTML, urlStr)
	return cleaned, err
}

func extractMarkdown(ctx context.Context, raw, rawURL string) (string, string, error) {
	cleaned, title, err := extractHTML(ctx, raw, rawURL)
	if err != nil {
		return "", title, err
	}
	md, err := toMarkdown(ctx, cleaned)
	return md, title, err
}

// Prefer a semantic main container for docs; use Readability for articles only
// when it retains enough of the cleaned content. Never apply SERP link pruning.
func extractHTML(ctx context.Context, raw, rawURL string) (string, string, error) {
	u, err := validatedURL(rawURL)
	if err != nil {
		return "", "", err
	}
	node, err := parseDocument(ctx, raw)
	if err != nil {
		return "", "", err
	}
	title := ""
	if head := firstNode(node, "title"); head != nil {
		title = shortText(strings.Join(strings.Fields(dom.TextContent(head)), " "), 250)
	}
	baseURL := u
	if base := firstNode(node, "base[href]"); base != nil {
		if ref, err := url.Parse(dom.GetAttribute(base, "href")); err == nil {
			candidate := u.ResolveReference(ref)
			if candidate.Scheme == "http" || candidate.Scheme == "https" {
				baseURL = candidate
			}
		}
	}
	absoluteLinks(node, baseURL)
	for _, bad := range queryAll(node, "script, style, noscript, iframe, template, canvas, svg, [hidden], [aria-hidden=true]") {
		if bad.Parent != nil {
			bad.Parent.RemoveChild(bad)
		}
	}
	main := firstNode(node, "main, [role=main]")
	root := main
	if root == nil {
		root = firstNode(node, "body")
		if root == nil {
			root = node
		}
		for _, chrome := range queryAll(root, "nav, aside, header, footer") {
			// Headers embedded in an article can contain its title and byline.
			inArticle := false
			for parent := chrome.Parent; parent != nil; parent = parent.Parent {
				if parent.Data == "article" {
					inArticle = true
					break
				}
			}
			if !inArticle && chrome.Parent != nil {
				chrome.Parent.RemoveChild(chrome)
			}
		}
	}
	fallback := dom.InnerHTML(root)
	text := strings.TrimSpace(dom.TextContent(root))
	if text == "" {
		return "", title, errors.New("extraction returned no readable content")
	}
	if main != nil {
		return fallback, title, ctx.Err()
	}
	article, articleErr := readability.FromReader(contextReader{ctx, strings.NewReader(raw)}, baseURL)
	if ctx.Err() != nil {
		return "", title, ctx.Err()
	}
	if articleErr == nil && article.Content != "" {
		if title == "" {
			title = shortText(article.Title, 250)
		}
		articleNode, parseErr := parseDocument(ctx, article.Content)
		if parseErr == nil {
			articleText := strings.TrimSpace(dom.TextContent(articleNode))
			preservesStructure := len(queryAll(articleNode, "table")) >= len(queryAll(root, "table")) &&
				len(queryAll(articleNode, "pre")) >= len(queryAll(root, "pre"))
			if preservesStructure && utf8.RuneCountInString(articleText)*100 >= utf8.RuneCountInString(text)*65 {
				absoluteLinks(articleNode, baseURL)
				return dom.InnerHTML(articleNode), title, nil
			}
		}
	}
	return fallback, title, nil
}

// Challenges have specific DOM/title signals, not just the word "captcha" in
// an article. Visible interstitial text is inspected only on short documents.
func blockedHTML(raw []byte) bool {
	if len(raw) == 0 || len(raw) > maxRawDocBytes {
		return false
	}
	node, err := parseDocument(context.Background(), string(raw))
	if err != nil {
		return false
	}
	title := ""
	if n := firstNode(node, "title"); n != nil {
		title = strings.ToLower(strings.TrimSpace(dom.TextContent(n)))
	}
	for _, phrase := range []string{"just a moment", "making sure you're not a bot", "access denied", "attention required", "robot or human", "captcha verification"} {
		if strings.HasPrefix(title, phrase) {
			return true
		}
	}
	if firstNode(node, "#anubis_challenge, #anubis_version, #challenge-running, #challenge-form, form[action*='/captcha/'], form[action*='anomaly']") != nil {
		return true
	}
	for _, n := range queryAll(node, "script, style, noscript") {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
	body := firstNode(node, "body")
	if body == nil {
		return false
	}
	text := strings.ToLower(strings.Join(strings.Fields(dom.TextContent(body)), " "))
	if len(text) > 2000 {
		return false
	}
	for _, phrase := range []string{"verifying your request", "verify you are human", "verify that you are human", "checking your browser", "unusual traffic from your computer network", "complete the captcha", "request has been blocked", "access to this page has been denied"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
