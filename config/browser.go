package config

// The web crawler (crawl package) launches a real browser whose executable is
// normally auto-detected. Non-canonical installations (custom prefixes,
// Flatpak, app bundles) need an explicit path: BrowserPaths lists them and they
// are probed before the built-in candidates. They live in Config.BrowserPaths.

// SetBrowserPaths replaces the configured browser executables. Blanks are
// dropped so a stray entry is never probed.
func SetBrowserPaths(paths []string) {
	config.BrowserPaths = cleanStrings(paths)
}

// BrowserPaths returns the configured browser executables, in priority order.
func BrowserPaths() []string {
	return append([]string(nil), config.BrowserPaths...)
}
