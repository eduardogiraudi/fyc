package crawl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for test condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func isolateTestCache(t *testing.T) {
	t.Helper()
	clear := func() {
		cacheMu.Lock()
		delete(resultCache, t.Name())
		cacheMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestCacheNormalizationDoesNotChangeResources(t *testing.T) {
	if got := normalizeCacheURL("https://EXAMPLE.com/p?b=2&utm_source=x&a=1#fragment"); got != "https://example.com/p?b=2&a=1" {
		t.Fatal(got)
	}
	if normalizeCacheURL("https://example.com/#/docs") == normalizeCacheURL("https://example.com/#/products") {
		t.Fatal("SPA hash routes share a cache entry")
	}
	if normalizeCacheURL("https://example.com/p/") == normalizeCacheURL("https://example.com/p") {
		t.Fatal("distinct trailing-slash resources merged")
	}
	if normalizeCacheURL("https://example.com/p?id=1") == normalizeCacheURL("https://example.com/p?id=2") {
		t.Fatal("functional query keys removed")
	}
	if documentCacheKey("query", "startpage", PagedOptions{SearchPage: 1}, networkPolicy{}) == documentCacheKey("query", "startpage", PagedOptions{SearchPage: 2}, networkPolicy{}) {
		t.Fatal("search pages share a cache entry")
	}
}

func TestSingleFlightKeepsIndependentCancellationAndContextValues(t *testing.T) {
	isolateTestCache(t)
	type contextKey struct{}
	leader, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "turn"))
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fetch := func(ctx context.Context) (fetchedDocument, error) {
		calls.Add(1)
		if ctx.Value(contextKey{}) != "turn" {
			t.Error("shared fetch dropped context values")
		}
		close(started)
		select {
		case <-release:
			return fetchedDocument{content: "shared content", title: "Title"}, nil
		case <-ctx.Done():
			return fetchedDocument{}, ctx.Err()
		}
	}
	leaderResult := make(chan error, 1)
	go func() {
		_, err := coalescedDocument(leader, t.Name(), fetch)
		leaderResult <- err
	}()
	<-started
	followerResult := make(chan error, 1)
	go func() {
		doc, err := coalescedDocument(context.Background(), t.Name(), fetch)
		if err == nil && (doc.content != "shared content" || doc.title != "Title") {
			err = errors.New("shared metadata lost")
		}
		followerResult <- err
	}()
	await(t, func() bool {
		cacheMu.Lock()
		defer cacheMu.Unlock()
		return flights[t.Name()] != nil && flights[t.Name()].waiters == 2
	})
	cancel()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-followerResult; err != nil {
		t.Fatal("leader cancelled follower:", err)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate fetches", calls.Load())
	}
}

func TestLastSubscriberCancelsFetchWithoutNegativeCaching(t *testing.T) {
	isolateTestCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := coalescedDocument(ctx, t.Name(), func(ctx context.Context) (fetchedDocument, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return fetchedDocument{}, ctx.Err()
		})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-stopped
	cacheMu.Lock()
	_, active := flights[t.Name()]
	_, cached := resultCache[t.Name()]
	cacheMu.Unlock()
	if active || cached {
		t.Fatal("cancelled fetch still active/cached")
	}
	doc, err := coalescedDocument(context.Background(), t.Name(), func(context.Context) (fetchedDocument, error) {
		return fetchedDocument{content: "fresh"}, nil
	})
	if err != nil || doc.content != "fresh" {
		t.Fatal(doc, err)
	}
}

func TestNegativeCacheTTLAndLargeDocuments(t *testing.T) {
	isolateTestCache(t)
	var calls atomic.Int32
	fetch := func(context.Context) (fetchedDocument, error) {
		calls.Add(1)
		return fetchedDocument{}, &CrawlError{Status: 429, Blocked: true, Cause: errors.New("rate limited")}
	}
	for i := 0; i < 2; i++ {
		if _, err := coalescedDocument(context.Background(), t.Name(), fetch); err == nil {
			t.Fatal("error was discarded")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("blocked site was fetched repeatedly")
	}
	cacheMu.Lock()
	entry := resultCache[t.Name()]
	entry.stored = time.Now().Add(-negativeCacheTTL - time.Second)
	resultCache[t.Name()] = entry
	cacheMu.Unlock()
	_, _ = coalescedDocument(context.Background(), t.Name(), fetch)
	if calls.Load() != 2 {
		t.Fatal("negative entry never expired")
	}
	cachePut(t.Name()+"large", strings.Repeat("x", maxCachedDocBytes+1))
	cacheMu.Lock()
	_, exists := resultCache[t.Name()+"large"]
	cacheMu.Unlock()
	if exists {
		t.Fatal("oversized document was cached")
	}
}

func TestConcurrentFetchLimit(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if old >= current || peak.CompareAndSwap(old, current) {
				break
			}
		}
		select {
		case <-release:
		case <-req.Context().Done():
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("content"))
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := cachedDocument(ctx, server.URL+"/"+string(rune('a'+i)), "", PagedOptions{}, networkPolicy{allowPrivate: true})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	await(t, func() bool { return active.Load() == 4 })
	close(release)
	wg.Wait()
	if peak.Load() > 4 {
		t.Fatal("unbounded browser/HTTP concurrency", peak.Load())
	}
}

func TestSystemLocaleAndTimezone(t *testing.T) {
	t.Setenv("FYC_CRAWL_LOCALE", "")
	t.Setenv("FYC_CRAWL_TIMEZONE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("TZ", "Europe/Rome")
	if profileLocale() != "it-IT" || profileTimezone() != "Europe/Rome" {
		t.Fatal(profileLocale(), profileTimezone())
	}
	t.Setenv("LC_ALL", "C.UTF-8")
	if profileLocale() != defaultLocale {
		t.Fatal("LC_ALL precedence lost")
	}
}
