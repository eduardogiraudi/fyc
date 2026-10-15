package crawl

import (
	_ "embed"

	"github.com/mxschmitt/playwright-go"
)

// Stealth JS vendored from github.com/jonfriesen/playwright-go-stealth (MIT).
// The original package pins github.com/playwright-community/playwright-go
// v0.4201.1: keeping it would make it impossible to use the current branch of
// playwright-go, which has resumed the module path github.com/mxschmitt/playwright-go.
// The two scripts are embedded here and injected with the same semantics as
// stealth.InjectWithOptions(ChromeStealth: true).

//go:embed stealth.min.js
var stealthJS string

//go:embed chrome_stealth.js
var chromeStealthJS string

// injectStealth injects the base script and the Chrome-specific one into a page.
func injectStealth(page playwright.Page) error {
	if err := page.AddInitScript(playwright.Script{
		Content: playwright.String(stealthJS),
	}); err != nil {
		return err
	}
	return page.AddInitScript(playwright.Script{
		Content: playwright.String(chromeStealthJS),
	})
}
