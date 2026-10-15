package crawl

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Browser profile — User-Agent, client hints and locale derived from the
// browser that is actually launched, so the headers do not contradict what
// the page can read from JavaScript (navigator.userAgent, userAgentData,
// navigator.language, Intl timezone).
// ---------------------------------------------------------------------------

const (
	defaultLocale   = "en-US"
	defaultTimezone = "UTC"
	// Only used when the real browser version cannot be parsed.
	fallbackChromeMajor = "131"
)

var (
	reMajorVersion = regexp.MustCompile(`(\d+)\.`)
	reLocale       = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	reTimezone     = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+\-]+)*$`)
)

type browserProfile struct {
	UserAgent string
	Locale    string
	Timezone  string
	// Headers holds the extra HTTP headers: Accept-Language and Sec-CH-UA*.
	Headers map[string]string
}

// profileLocale returns the browser locale: FYC_CRAWL_LOCALE if valid,
// otherwise en-US.
func profileLocale() string {
	if v := strings.TrimSpace(os.Getenv("FYC_CRAWL_LOCALE")); v != "" && reLocale.MatchString(v) {
		return v
	}
	return defaultLocale
}

// profileTimezone returns the IANA timezone: FYC_CRAWL_TIMEZONE if it looks
// valid, otherwise UTC.
func profileTimezone() string {
	if v := strings.TrimSpace(os.Getenv("FYC_CRAWL_TIMEZONE")); v != "" && reTimezone.MatchString(v) {
		if _, err := time.LoadLocation(v); err == nil {
			return v
		}
	}
	return defaultTimezone
}

// buildProfile derives a coherent profile from the version string reported by
// the launched browser (Browser.Version()), the host OS (runtime.GOOS) and the
// wanted locale / timezone.
func buildProfile(browserVersion, goos, locale, timezone string) browserProfile {
	major := fallbackChromeMajor
	if m := reMajorVersion.FindStringSubmatch(browserVersion); m != nil {
		major = m[1]
	}

	var uaPlatform, chPlatform string
	switch goos {
	case "darwin":
		uaPlatform, chPlatform = "Macintosh; Intel Mac OS X 10_15_7", "macOS"
	case "windows":
		uaPlatform, chPlatform = "Windows NT 10.0; Win64; x64", "Windows"
	default:
		uaPlatform, chPlatform = "X11; Linux x86_64", "Linux"
	}

	return browserProfile{
		// Chrome freezes everything after the major version in the UA.
		UserAgent: fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", uaPlatform, major),
		Locale:    locale,
		Timezone:  timezone,
		Headers: map[string]string{
			"Accept-Language":    acceptLanguage(locale),
			"Sec-Ch-Ua":          fmt.Sprintf(`"Google Chrome";v="%s", "Chromium";v="%s", "Not.A/Brand";v="24"`, major, major),
			"Sec-Ch-Ua-Mobile":   "?0",
			"Sec-Ch-Ua-Platform": `"` + chPlatform + `"`,
		},
	}
}

// acceptLanguage builds a browser-like Accept-Language list from a locale:
// "it-IT" → "it-IT,it;q=0.9,en-US;q=0.8,en;q=0.7", "en-US" → "en-US,en;q=0.9".
func acceptLanguage(locale string) string {
	primary := strings.ToLower(strings.SplitN(locale, "-", 2)[0])
	var tags []string
	seen := map[string]bool{}
	for _, t := range []string{locale, primary, "en-US", "en"} {
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	var b strings.Builder
	for i, t := range tags {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(t)
		if i > 0 {
			fmt.Fprintf(&b, ";q=%.1f", 1.0-0.1*float64(i))
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Tracker / ad blocking
// ---------------------------------------------------------------------------

// trackerHosts are advertising / analytics / session-replay hosts. Requests to
// them (and their subdomains) are aborted: pages load faster and the DOM has
// less noise. Cookie-consent providers are deliberately NOT listed.
var trackerHosts = []string{
	"doubleclick.net", "googlesyndication.com", "googleadservices.com",
	"googletagservices.com", "googletagmanager.com", "google-analytics.com",
	"adservice.google.com", "2mdn.net",
	"connect.facebook.net", "ads-twitter.com", "analytics.tiktok.com",
	"snap.licdn.com", "px.ads.linkedin.com", "ct.pinterest.com", "bat.bing.com",
	"hotjar.com", "hotjar.io", "clarity.ms", "fullstory.com", "mouseflow.com",
	"crazyegg.com", "segment.io", "mixpanel.com", "amplitude.com",
	"chartbeat.com", "chartbeat.net", "scorecardresearch.com", "quantserve.com",
	"quantcount.com", "nr-data.net", "permutive.com", "bounceexchange.com",
	"taboola.com", "outbrain.com", "criteo.com", "criteo.net", "adnxs.com",
	"adsrvr.org", "pubmatic.com", "rubiconproject.com", "openx.net",
	"casalemedia.com", "amazon-adsystem.com", "adform.net", "smartadserver.com",
	"teads.tv", "moatads.com", "doubleverify.com", "adsafeprotected.com",
	"yieldmo.com",
}

func isTrackerHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, t := range trackerHosts {
		if host == t || strings.HasSuffix(host, "."+t) {
			return true
		}
	}
	return false
}

// isTrackerRequest reports whether rawURL points at a tracker host. Requests
// to the site being crawled are never blocked, so crawling a tracker's own
// pages (e.g. a Taboola or Hotjar page) still works.
func isTrackerRequest(rawURL, targetHost string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if targetHost != "" {
		t := strings.ToLower(targetHost)
		if host == t || strings.HasSuffix(host, "."+t) || strings.HasSuffix(t, "."+host) {
			return false
		}
	}
	return isTrackerHost(host)
}
