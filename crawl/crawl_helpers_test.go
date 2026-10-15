package crawl

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanLinks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"utm stripped", `[a](https://x.com/p?utm_source=n&id=3&fbclid=abc)`, `[a](https://x.com/p?id=3)`},
		{"only tracking", `[a](https://x.com/p?utm_source=n&gclid=1)`, `[a](https://x.com/p)`},
		{"keeps fragment", `[a](https://x.com/p?utm_x=1#sec)`, `[a](https://x.com/p#sec)`},
		{"text fragment dropped", `[a](https://x.com/p#:~:text=hello)`, `[a](https://x.com/p)`},
		{"empty link", "see [](https://x.com) this", "see  this"},
		{"empty link spaces", "see [  ](https://x.com) this", "see  this"},
		{"image kept", `![logo](https://x.com/l.png)`, `![logo](https://x.com/l.png)`},
		{"empty image kept", `![](https://x.com/l.png)`, `![](https://x.com/l.png)`},
		{"data image", "a ![x](data:image/png;base64,AAAA) b", "a  b"},
		{"data image in link", "a [![x](data:image/png;base64,AAAA)](https://x.com) b", "a  b"},
		{"data link keeps text", "[click](data:text/html;base64,AAAA)", "click"},
		{"adjacent empty", "[](a)[](b)ok", "ok"},
		{"parens url", `[w](https://en.wikipedia.org/wiki/Go_(game)?utm_a=1)`, `[w](https://en.wikipedia.org/wiki/Go_(game))`},
		{"title kept", `[a](https://x.com/p?utm_a=1 "T")`, `[a](https://x.com/p "T")`},
		{"param order kept", `[a](https://x.com/?b=2&a=1&utm_z=3)`, `[a](https://x.com/?b=2&a=1)`},
	}
	for _, c := range cases {
		if got := cleanLinks(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestCleanLinkURLLong(t *testing.T) {
	long := "https://x.com/a?" + strings.Repeat("q=1&", 200)
	if got := cleanLinkURL(long); got != "https://x.com/a" {
		t.Errorf("long query should be dropped, got %q", got)
	}
	veryLong := "https://x.com/" + strings.Repeat("é", 400)
	got := cleanLinkURL(veryLong)
	if len(got) > maxLinkURLLen || !strings.HasPrefix(veryLong, got) {
		t.Errorf("bad truncation len=%d", len(got))
	}
}

func TestCleanMarkdown(t *testing.T) {
	in := "# T\n\n- [](https://x.com)\n- item [a](https://x.com?utm_a=1)\n\n\n\nend  \n<!--THE END-->tail"
	want := "# T\n\n- item [a](https://x.com)\n\nend"
	if got := cleanMarkdown(in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestPagedDocBasics(t *testing.T) {
	// empty doc
	d := newPagedDoc("", 2000)
	if d.pages() != 1 || d.total() != 0 {
		t.Fatalf("empty: pages=%d total=%d", d.pages(), d.total())
	}
	// multibyte, no newline: must not split a rune and must cover everything
	s := strings.Repeat("è", 5000)
	d = newPagedDoc(s, 2000)
	if d.pages() != 3 {
		t.Fatalf("pages=%d", d.pages())
	}
	sum := 0
	for i := range d.starts {
		sum += d.ends[i] - d.starts[i]
	}
	if sum != 5000 {
		t.Errorf("coverage %d", sum)
	}
	// newline preference
	line := strings.Repeat("a", 99) + "\n"
	d = newPagedDoc(strings.Repeat(line, 50), 2000)
	for i := 0; i < d.pages()-1; i++ {
		if d.runes[d.ends[i]-1] != '\n' {
			t.Errorf("page %d does not end on newline", i+1)
		}
	}
}

func TestSlicePageCompat(t *testing.T) {
	c, info := slicePage(strings.Repeat("x\n", 5000), 99, 2000)
	if info.page != info.pages || !strings.HasPrefix(c, "[page") {
		t.Errorf("clamp failed: %+v", info)
	}
}

func TestOutline(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Title\n")
	b.WriteString(strings.Repeat("para para para\n", 400)) // ~6000 chars
	b.WriteString("## Second [link](http://x)\n")
	b.WriteString("```\n# not a heading\n```\n")
	b.WriteString(strings.Repeat("para para para\n", 400))
	b.WriteString("### Third **bold**\n")
	d := newPagedDoc(b.String(), 2000)
	out, trunc := d.outline()
	if trunc || len(out) != 3 {
		t.Fatalf("outline=%+v trunc=%v", out, trunc)
	}
	if out[0].Title != "Title" || out[0].Page != 1 || out[0].Level != 1 {
		t.Errorf("first=%+v", out[0])
	}
	if out[1].Title != "Second link" || out[1].Level != 2 || out[1].Page < 3 {
		t.Errorf("second=%+v", out[1])
	}
	if out[2].Title != "Third **bold" && out[2].Title != "Third **bold**" && out[2].Level != 3 {
		t.Errorf("third=%+v", out[2])
	}
	if out[2].Page != d.pages() {
		t.Errorf("third page %d, pages %d", out[2].Page, d.pages())
	}
}

func TestOutlineCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("## H2\n### H3\n#### H4\n")
	}
	out, trunc := newPagedDoc(b.String(), 2000).outline()
	if !trunc || len(out) > maxOutlineEntries {
		t.Fatalf("len=%d trunc=%v", len(out), trunc)
	}
	for _, e := range out {
		if e.Level > 2 {
			t.Fatalf("deep level kept: %+v", e)
		}
	}
}

func TestFind(t *testing.T) {
	filler := strings.Repeat("lorem ipsum dolor\n", 300) // ~5400 chars
	doc := filler + "Perché la Città è bella. CITTÀ ancora.\n" + filler + "fine città\n"
	d := newPagedDoc(doc, 2000)
	r := d.find("  città ")
	if r.Total != 3 {
		t.Fatalf("total=%d res=%+v", r.Total, r)
	}
	if len(r.Pages) < 2 {
		t.Errorf("pages=%v", r.Pages)
	}
	for _, m := range r.Matches {
		if !strings.Contains(strings.ToLower(m.Snippet), "città") {
			t.Errorf("snippet lacks term: %q", m.Snippet)
		}
		page, _ := d.window(m.Page)
		if !strings.Contains(strings.ToLower(page), "città") {
			t.Errorf("match page %d does not contain the term", m.Page)
		}
	}
	if r := d.find("zzzz"); r.Total != 0 || len(r.Pages) != 0 {
		t.Errorf("unexpected %+v", r)
	}
	if r := d.find("   "); r.Total != 0 {
		t.Errorf("blank query matched")
	}
}

func TestFindMatchCap(t *testing.T) {
	d := newPagedDoc(strings.Repeat("needle ", 500), 2000)
	r := d.find("needle")
	if r.Total != 500 || len(r.Matches) != maxFindMatches {
		t.Errorf("total=%d shown=%d", r.Total, len(r.Matches))
	}
}

func TestPageOutOfRangeError(t *testing.T) {
	var err error = &PageOutOfRangeError{Page: 9, Pages: 3}
	var pe *PageOutOfRangeError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "9") || !strings.Contains(err.Error(), "3") {
		t.Errorf("bad error: %v", err)
	}
}

func TestMarshalPayloadNoHTMLEscape(t *testing.T) {
	got := marshalPayload(map[string]any{"c": "a<b>&c"})
	if got != `{"c":"a<b>&c"}` {
		t.Errorf("got %s", got)
	}
}

func TestBuildProfile(t *testing.T) {
	p := buildProfile("125.0.6422.60", "linux", "en-US", "UTC")
	if !strings.Contains(p.UserAgent, "Chrome/125.0.0.0") || !strings.Contains(p.UserAgent, "X11; Linux") {
		t.Errorf("ua=%s", p.UserAgent)
	}
	if !strings.Contains(p.Headers["Sec-Ch-Ua"], `v="125"`) || p.Headers["Sec-Ch-Ua-Platform"] != `"Linux"` {
		t.Errorf("headers=%v", p.Headers)
	}
	if p.Headers["Accept-Language"] != "en-US,en;q=0.9" {
		t.Errorf("al=%s", p.Headers["Accept-Language"])
	}
	if p := buildProfile("Chromium 138.0.7204.49", "windows", "en-US", "UTC"); !strings.Contains(p.UserAgent, "Windows NT") ||
		!strings.Contains(p.UserAgent, "Chrome/138.") || p.Headers["Accept-Language"] != "en-US,en;q=0.9" || p.Headers["Sec-Ch-Ua-Platform"] != `"Windows"` {
		t.Errorf("win profile: %+v", p)
	}
	if p := buildProfile("", "darwin", "de", "Europe/Berlin"); !strings.Contains(p.UserAgent, "Macintosh") ||
		!strings.Contains(p.UserAgent, "Chrome/"+fallbackChromeMajor) || p.Headers["Accept-Language"] != "de,en-US;q=0.9,en;q=0.8" {
		t.Errorf("fallback profile: %+v / %s", p, p.Headers["Accept-Language"])
	}
}

func TestLocaleTimezoneEnv(t *testing.T) {
	t.Setenv("FYC_CRAWL_LOCALE", "fr-FR")
	t.Setenv("FYC_CRAWL_TIMEZONE", "Europe/Paris")
	if profileLocale() != "fr-FR" || profileTimezone() != "Europe/Paris" {
		t.Error("env not honoured")
	}
	t.Setenv("FYC_CRAWL_LOCALE", "not a locale!")
	t.Setenv("FYC_CRAWL_TIMEZONE", "../etc")
	if profileLocale() != defaultLocale || profileTimezone() != defaultTimezone {
		t.Error("invalid env not rejected")
	}
}

func TestTrackerRequests(t *testing.T) {
	cases := []struct {
		url, target string
		want        bool
	}{
		{"https://www.google-analytics.com/g/collect", "example.com", true},
		{"https://stats.g.doubleclick.net/x", "example.com", true},
		{"https://example.com/app.js", "example.com", false},
		{"https://cdn.example.com/app.js", "example.com", false},
		{"https://notdoubleclick.net/x", "example.com", false},
		{"https://static.hotjar.com/c.js", "hotjar.com", false}, // crawling the tracker's own site
		{"https://www.hotjar.com/", "", true},
		{"https://onetrust.com/x", "example.com", false}, // consent managers are not blocked
		{"::bad::", "x", false},
	}
	for _, c := range cases {
		if got := isTrackerRequest(c.url, c.target); got != c.want {
			t.Errorf("%s (target %q): got %v want %v", c.url, c.target, got, c.want)
		}
	}
}
