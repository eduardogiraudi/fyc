package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCrawlPolicyMigrationAndRoundTrip(t *testing.T) {
	for _, block := range []string{"", `,"crawl":{"allow_private_networks":true,"disable_sandbox":true}`} {
		t.Run(block, func(t *testing.T) {
			dir := withConfigDir(t)
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":12,"model":"preserved"`+block+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := LoadE(); err != nil {
				t.Fatal(err)
			}
			want := block != ""
			if config.Crawl.AllowPrivateNetworks != want || config.Crawl.DisableSandbox != want || config.Model != "preserved" || config.Schema != 13 {
				t.Fatalf("migration changed policy: %+v", config.Crawl)
			}
			SetCrawlConfig(CrawlConfig{AllowPrivateNetworks: !want})
			if err := SaveE(); err != nil {
				t.Fatal(err)
			}
			if err := LoadE(); err != nil {
				t.Fatal(err)
			}
			if config.Crawl.AllowPrivateNetworks == want || config.Crawl.DisableSandbox {
				t.Fatal("crawl policy did not survive reload")
			}
		})
	}
	var current Config
	if err := json.Unmarshal([]byte(`{"schema":13}`), &current); err != nil || current.Crawl.AllowPrivateNetworks || current.Crawl.DisableSandbox {
		t.Fatal("network/sandbox exceptions enabled by default")
	}
}
