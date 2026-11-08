package crawl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fuckyourcli/config"
)

func TestHTTPDocumentsAndMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/markdown") {
			t.Error("Markdown content negotiation missing")
		}
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/docs/final", http.StatusFound)
		case "/docs/final":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<title>Redirected docs</title><main><h1>Docs</h1><p><a href="child">Child</a></p><table><tr><th>Option</th><th>Value</th></tr><tr><td>timeout</td><td>30</td></tr></table></main>`)
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"answer":42}`)
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "  raw := value\n\tindented\n")
		case "/markdown":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = io.WriteString(w, "# Native Markdown\n\n| A | B |\n|---|---|\n| 1 | 2 |")
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(testPDF("Hello PDF"))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{137, 80, 78, 71})
		case "/charset":
			w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
			_, _ = w.Write([]byte("<title>Caff\xe8</title><main>Caff\xe8 italiano</main>"))
		}
	}))
	defer server.Close()
	for path, expected := range map[string]string{"/redirect": "timeout", "/json": `"answer": 42`, "/plain": "\tindented", "/markdown": "# Native Markdown", "/pdf": "Hello PDF", "/charset": "Caffè"} {
		doc, err := fetchDocument(context.Background(), server.URL+path, PagedOptions{}, networkPolicy{allowPrivate: true})
		if err != nil || doc.status != 200 || !strings.Contains(doc.content, expected) {
			t.Fatalf("%s: %+v %v", path, doc, err)
		}
		if path == "/redirect" && (doc.url != server.URL+"/docs/final" || doc.title != "Redirected docs" || !strings.Contains(doc.content, server.URL+"/docs/child")) {
			t.Fatalf("redirect metadata/base URL lost: %+v", doc)
		}
	}
	if _, err := fetchDocument(context.Background(), server.URL+"/image", PagedOptions{}, networkPolicy{allowPrivate: true}); err == nil || !strings.Contains(err.Error(), "unsupported Content-Type") {
		t.Fatalf("unsupported media did not fail explicitly: %v", err)
	}
}

func TestHTTPErrorPayloadAndBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/big" {
			w.Header().Set("Content-Length", fmt.Sprint(maxRawDocBytes+1))
			_, _ = io.WriteString(w, "big")
			return
		}
		if r.URL.Path == "/blocked" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, fixture(t, "captcha"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<h1>Not found</h1>")
	}))
	defer server.Close()
	for _, path := range []string{"/blocked", "/missing", "/big"} {
		_, _, err := fetchHTTP(context.Background(), server.URL+path, PagedOptions{}, networkPolicy{allowPrivate: true})
		var failure *CrawlError
		if !errors.As(err, &failure) || failure.URL != server.URL+path || failure.Status == 0 {
			t.Fatalf("error metadata lost: %v", err)
		}
		payload := decode(t, ErrorResult(err))
		if payload["status"] != float64(failure.Status) || payload["content_type"] != "text/html" {
			t.Fatal(payload)
		}
		if path == "/big" && !errors.Is(err, errDocumentTooLarge) {
			t.Fatalf("size limit missing: %v", err)
		}
		if path == "/blocked" && !failure.Blocked {
			t.Fatal("challenge was treated as content")
		}
	}
	if _, err := readBounded(strings.NewReader("12345"), 4); !errors.Is(err, errDocumentTooLarge) {
		t.Fatal(err)
	}
	if data, err := readBounded(strings.NewReader("1234"), 4); err != nil || string(data) != "1234" {
		t.Fatal(string(data), err)
	}
}

func TestHTTPCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := fetchHTTP(ctx, server.URL, PagedOptions{}, networkPolicy{allowPrivate: true})
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP fetch ignored cancellation")
	}
	<-finished
}

func TestCachedWindowsRetainMetadataAndPolicy(t *testing.T) {
	previous := config.Get().Crawl
	config.SetCrawlConfig(config.CrawlConfig{AllowPrivateNetworks: true})
	t.Cleanup(func() { config.SetCrawlConfig(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<title>Large docs</title><main><h1>Heading</h1><p>"+strings.Repeat("text text ", 800)+"unique-needle</p></main>")
	}))
	defer server.Close()
	first, err := CrawlPagedOptsContext(context.Background(), server.URL, "startpage", PagedOptions{MaxChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	second, err := CrawlPagedOptsContext(context.Background(), server.URL+"#fragment", "brave", PagedOptions{MaxChars: 2000, Page: 2})
	if err != nil {
		t.Fatal("cached window was fetched again:", err)
	}
	for _, payload := range []map[string]any{decode(t, first), decode(t, second)} {
		if payload["title"] != "Large docs" || payload["url"] != server.URL || payload["status"] != float64(200) {
			t.Fatal("metadata lost:", payload)
		}
	}
	config.SetCrawlConfig(config.CrawlConfig{})
	if _, err := CrawlPagedOptsContext(context.Background(), server.URL, "", PagedOptions{}); !errors.Is(err, errUnsafeURL) {
		t.Fatal("private cache leaked across policy change:", err)
	}
}

func testPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return out.Bytes()
}

func TestPDFErrorsAndCancellation(t *testing.T) {
	if _, err := extractPDF(context.Background(), []byte("not a PDF")); err == nil {
		t.Fatal("invalid PDF accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractPDF(ctx, testPDF("text")); !errors.Is(err, context.Canceled) {
		t.Fatal("PDF cancellation lost:", err)
	}
	if _, err := extractPDF(context.Background(), testPDF("")); err == nil || !strings.Contains(err.Error(), "OCR") {
		t.Fatal("scanned PDF did not fail explicitly:", err)
	}
}
