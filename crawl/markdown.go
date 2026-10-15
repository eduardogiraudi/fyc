package crawl

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Markdown cleanup — shrinks the converted page so fewer tokens reach the
// model: tracking parameters, empty links, data: URIs and absurdly long URLs
// carry no information for it.
// ---------------------------------------------------------------------------

// maxLinkURLLen is the longest link destination kept verbatim. Longer URLs
// first lose their query string and fragment; if they are still longer they
// are cut (such a link is almost never useful to follow anyway).
const maxLinkURLLen = 300

// trackingParamPrefixes and trackingParams list query parameters that only
// carry campaign / click attribution and never change the target page.
var trackingParamPrefixes = []string{"utm_", "mtm_", "hsa_", "pk_"}

var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "gclsrc": true, "dclid": true,
	"msclkid": true, "yclid": true, "twclid": true, "ttclid": true,
	"li_fat_id": true, "igshid": true, "mc_cid": true, "mc_eid": true,
	"_hsenc": true, "_hsmi": true, "__hssc": true, "__hstc": true,
	"__hsfp": true, "mkt_tok": true, "ref_src": true, "ref_url": true,
	"_ga": true, "_gl": true, "vero_id": true, "wickedid": true,
	"oly_enc_id": true, "oly_anon_id": true, "s_kwcid": true,
	"srsltid": true,
}

const (
	// linkDest matches a markdown link destination, allowing one level of
	// balanced parentheses (e.g. Wikipedia URLs) but no whitespace.
	linkDest = `(?:[^()\s]|\([^()\s]*\))`
)

var (
	// ![alt](data:...) — inline base64 images are pure token waste.
	reDataImage = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?data:[^)]*\)`)
	// [text](data:...) — keep the text, drop the link.
	reDataLink = regexp.MustCompile(`\[([^\]]*)\]\(\s*<?data:[^)]*\)`)
	// [](url) / [   ](url) — links with no text (but not images: ![](url)).
	reEmptyLink = regexp.MustCompile(`(^|[^!])\[\s*\]\(` + linkDest + `*(?:\s+"[^"]*")?\)`)
	// ](destination "optional title") — the part of a link we rewrite.
	reLinkDest = regexp.MustCompile(`\]\((` + linkDest + `+)((?:\s+"[^"]*")?)\)`)
	// list items left with no content once their only link was removed.
	reEmptyBullet    = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+\.)[ \t]*$`)
	reTrailingSpaces = regexp.MustCompile(`(?m)[ \t]+$`)
)

// cleanMarkdown post-processes converted markdown.
func cleanMarkdown(md string) string {
	if idx := strings.Index(md, "<!--THE END-->"); idx >= 0 {
		md = md[:idx]
	}
	md = cleanLinks(md)
	md = reEmptyBullet.ReplaceAllString(md, "")
	md = reTrailingSpaces.ReplaceAllString(md, "")
	for strings.Contains(md, "\n\n\n") {
		md = strings.ReplaceAll(md, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(md)
}

// cleanLinks removes data: URIs and empty links, strips tracking parameters
// and shortens very long URLs.
func cleanLinks(md string) string {
	md = reDataImage.ReplaceAllString(md, "")
	md = reDataLink.ReplaceAllString(md, "$1")

	// The regexp consumes the character before "[", so adjacent empty links
	// need more than one pass. Removing an inner image can also expose a new
	// empty link: [![x](data:...)](url) → [](url) → "".
	for i := 0; i < 5; i++ {
		next := reEmptyLink.ReplaceAllString(md, "$1")
		if next == md {
			break
		}
		md = next
	}

	return reLinkDest.ReplaceAllStringFunc(md, func(m string) string {
		sub := reLinkDest.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		return "](" + cleanLinkURL(sub[1]) + sub[2] + ")"
	})
}

func isTrackingParam(key string) bool {
	k := strings.ToLower(key)
	if trackingParams[k] {
		return true
	}
	for _, p := range trackingParamPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// cleanLinkURL strips tracking parameters from a URL string without
// re-encoding it (query parameters are filtered textually so the remaining
// ones are left byte-for-byte as they were), then enforces maxLinkURLLen.
func cleanLinkURL(raw string) string {
	base, frag := raw, ""
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base, frag = base[:i], base[i:]
	}
	query := ""
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base, query = base[:i], base[i+1:]
	}

	if query != "" {
		kept := make([]string, 0, 4)
		for _, p := range strings.Split(query, "&") {
			if p == "" {
				continue
			}
			key := p
			if i := strings.IndexByte(p, '='); i >= 0 {
				key = p[:i]
			}
			if isTrackingParam(key) {
				continue
			}
			kept = append(kept, p)
		}
		query = strings.Join(kept, "&")
	}

	// Text fragments (#:~:text=...) are highlight hints, not navigation.
	if strings.HasPrefix(frag, "#:~:") {
		frag = ""
	}

	out := base
	if query != "" {
		out += "?" + query
	}
	out += frag

	if len(out) > maxLinkURLLen {
		out = base
		if len(out) > maxLinkURLLen {
			cut := maxLinkURLLen
			for cut > 0 && !utf8.RuneStart(out[cut]) {
				cut--
			}
			out = out[:cut]
		}
	}
	return out
}
