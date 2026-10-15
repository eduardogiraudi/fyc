package tools

import "fuckyourcli/crawl"

// WebSearchBlockReason returns a human-readable reason when the web_search tool
// cannot run because no supported browser executable was found among the
// configured browser_paths and the built-in locations. Empty when a browser is
// available.
func WebSearchBlockReason() string {
	if crawl.BrowserAvailable() {
		return ""
	}
	return "no supported browser found (checked the configured browser_paths and the built-in locations)"
}
