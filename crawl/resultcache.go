package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	cacheMaxEntries   = 32
	cacheTTL          = 10 * time.Minute
	negativeCacheTTL  = 30 * time.Second
	maxCachedDocBytes = 1 << 20
	crawlTimeout      = 60 * time.Second
)

type cacheEntry struct {
	doc    fetchedDocument
	err    error
	stored time.Time
}

type documentFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	complete bool
	doc      fetchedDocument
	err      error
}

var (
	cacheMu     sync.Mutex
	resultCache = map[string]cacheEntry{}
	flights     = map[string]*documentFlight{}
	fetchSlots  = make(chan struct{}, 4)
)

// Non-routing fragments and tracking keys do not select another document.
// Hash routes, trailing slashes, query ordering and functional keys are preserved.
func normalizeCacheURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.HasPrefix(u.Fragment, "/") && !strings.HasPrefix(u.Fragment, "!") {
		u.Fragment, u.RawFragment = "", ""
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	parts := strings.Split(u.RawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(key)
		if err != nil || !isTrackingParam(decoded) {
			kept = append(kept, part)
		}
	}
	u.RawQuery = strings.Join(kept, "&")
	return u.String()
}

func documentCacheKey(input, engine string, opts PagedOptions, policy networkPolicy) string {
	if isHTTPURL(input) {
		input, engine = normalizeCacheURL(input), ""
	} else {
		engine = strings.ToLower(strings.TrimSpace(engine))
		if engine == "" {
			engine = "startpage"
		}
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%t", engine, input,
		max(1, opts.SearchPage), opts.Region, opts.Language, profileLocale(), profileTimezone(), policy.allowPrivate)
}

func cachePut(key, content string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	putDocumentLocked(key, fetchedDocument{content: content}, nil)
}

func putDocumentLocked(key string, doc fetchedDocument, err error) {
	if len(doc.content) > maxCachedDocBytes {
		return
	}
	if err != nil {
		var failure *CrawlError
		if !errors.As(err, &failure) || (!failure.Blocked && failure.Status != 404 && failure.Status != 429 && failure.Status != 503) {
			return
		}
	}
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
	resultCache[key] = cacheEntry{doc: doc, err: err, stored: time.Now()}
}

func cachedDocument(ctx context.Context, input, engine string, opts PagedOptions, policy networkPolicy) (fetchedDocument, error) {
	return coalescedDocument(ctx, documentCacheKey(input, engine, opts, policy), func(fetchCtx context.Context) (fetchedDocument, error) {
		select {
		case fetchSlots <- struct{}{}:
			defer func() { <-fetchSlots }()
		case <-fetchCtx.Done():
			return fetchedDocument{}, fetchCtx.Err()
		}
		if isHTTPURL(input) {
			return fetchDocument(fetchCtx, input, opts, policy)
		}
		return fetchSearch(fetchCtx, input, engine, opts, policy)
	})
}

// A cancelled subscriber never cancels another turn's shared request. Once the
// last subscriber leaves, abort the fetch and do not cache its cancellation.
func coalescedDocument(ctx context.Context, key string, fetch func(context.Context) (fetchedDocument, error)) (fetchedDocument, error) {
	if err := ctx.Err(); err != nil {
		return fetchedDocument{}, err
	}
	cacheMu.Lock()
	if e, ok := resultCache[key]; ok {
		ttl := cacheTTL
		if e.err != nil {
			ttl = negativeCacheTTL
		}
		if time.Since(e.stored) < ttl {
			cacheMu.Unlock()
			return e.doc, e.err
		}
		delete(resultCache, key)
	}
	f := flights[key]
	if f == nil {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), crawlTimeout)
		f = &documentFlight{done: make(chan struct{}), cancel: cancel}
		flights[key] = f
		go func() {
			doc, err := fetch(fetchCtx)
			if fetchCtx.Err() != nil {
				err = fetchCtx.Err()
			}
			cacheMu.Lock()
			f.doc, f.err, f.complete = doc, err, true
			if flights[key] == f {
				delete(flights, key)
				putDocumentLocked(key, doc, err)
			}
			close(f.done)
			cacheMu.Unlock()
			cancel()
		}()
	}
	f.waiters++
	cacheMu.Unlock()
	defer func() {
		cacheMu.Lock()
		f.waiters--
		if f.waiters == 0 && !f.complete {
			if flights[key] == f {
				delete(flights, key)
			}
			f.cancel()
		}
		cacheMu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return fetchedDocument{}, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			return fetchedDocument{}, err
		}
		return f.doc, f.err
	}
}
