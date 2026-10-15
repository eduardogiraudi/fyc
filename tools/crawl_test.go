package tools

import (
	"fuckyourcli/crawl"
	"testing"
)

func TestCrawlOutput(t *testing.T) {
	res1, _ := crawl.Crawl("New AI models", "startpage")
	t.Logf("Crawl results: %+v\n\n\n\n\n", res1)
	res2, _ := crawl.Crawl("https://github.com/torvalds/linux", "startpage")
	t.Logf("Crawl results: %+v", res2)
}
