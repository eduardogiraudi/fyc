package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-shiori/dom"
)

type searchRequest struct {
	url    string
	method string
	body   string
	offset int
}

// DuckDuckGo HTML pagination uses a server-issued cursor and variable page
// sizes. Guessed GET offsets return the first page again without any error.
func fetchDuckDuckGoPage(ctx context.Context, rawURL string, opts PagedOptions, policy networkPolicy) (fetchedDocument, []byte, error) {
	request := searchRequest{url: rawURL, method: http.MethodGet}
	for page := 1; page <= max(1, opts.SearchPage); page++ {
		doc, raw, err := fetchHTTPRequest(ctx, request, opts, policy)
		if err != nil || page == max(1, opts.SearchPage) {
			return doc, raw, err
		}
		next, err := nextSearchRequest(ctx, string(raw), doc.url)
		if err != nil {
			return doc, raw, documentError(doc, fmt.Errorf("search_page %d unavailable: %w", opts.SearchPage, err), false)
		}
		if next.offset <= request.offset {
			return doc, raw, documentError(doc, errors.New("search continuation cursor did not advance"), false)
		}
		request = next
	}
	return fetchedDocument{}, nil, errors.New("invalid search page")
}

func nextSearchRequest(ctx context.Context, raw, rawURL string) (searchRequest, error) {
	node, err := parseDocument(ctx, raw)
	if err != nil {
		return searchRequest{}, err
	}
	base, err := validatedURL(rawURL)
	if err != nil {
		return searchRequest{}, err
	}
	for _, form := range queryAll(node, "form") {
		values := url.Values{}
		isNext := false
		for _, input := range queryAll(form, "input") {
			kind := strings.ToLower(dom.GetAttribute(input, "type"))
			if kind == "submit" && strings.HasPrefix(strings.ToLower(dom.GetAttribute(input, "value")), "next") {
				isNext = true
			}
			name := dom.GetAttribute(input, "name")
			if kind == "hidden" && name != "" {
				values.Add(name, dom.GetAttribute(input, "value"))
			}
		}
		offset, err := strconv.Atoi(values.Get("s"))
		if !isNext || err != nil || offset <= 0 {
			continue
		}
		ref, err := url.Parse(dom.GetAttribute(form, "action"))
		if err != nil {
			return searchRequest{}, err
		}
		action := base.ResolveReference(ref)
		if action.Scheme != base.Scheme || action.Host != base.Host || action.User != nil {
			return searchRequest{}, errors.New("search continuation changed origin")
		}
		method := strings.ToUpper(dom.GetAttribute(form, "method"))
		if method == "" {
			method = http.MethodGet
		}
		body := values.Encode()
		if len(body) > 8192 {
			return searchRequest{}, errors.New("search continuation exceeds size limit")
		}
		switch method {
		case http.MethodGet:
			query := action.Query()
			for key, value := range values {
				query[key] = value
			}
			action.RawQuery, body = query.Encode(), ""
		case http.MethodPost:
		default:
			return searchRequest{}, errors.New("unsupported search continuation method")
		}
		return searchRequest{url: action.String(), method: method, body: body, offset: offset}, nil
	}
	return searchRequest{}, errors.New("no next-results cursor")
}
