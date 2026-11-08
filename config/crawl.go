package config

// CrawlConfig keeps network and process-isolation exceptions explicit.
type CrawlConfig struct {
	AllowPrivateNetworks bool `json:"allow_private_networks,omitempty"`
	DisableSandbox       bool `json:"disable_sandbox,omitempty"`
}

func SetCrawlConfig(c CrawlConfig) {
	config.Crawl = c
}
