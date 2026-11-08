package tools

// WebSearchBlockReason is kept for the runtime availability layer. HTTP search
// and static crawling are always available; only JS rendering needs a browser.
func WebSearchBlockReason() string {
	return ""
}
