package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"fuckyourcli/store"
)

// DataURL is the models.dev catalog endpoint. It is a variable so tests can
// point it at a local server.
var DataURL = "https://models.dev/api.json"

// maxCatalogBytes caps the download so a hostile endpoint cannot exhaust memory.
const maxCatalogBytes = 64 << 20

// Update downloads the latest catalog from models.dev and overwrites it at
// Path. It returns the number of providers and models written.
func Update(ctx context.Context) (providers, modelCount int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DataURL, nil)
	if err != nil {
		return 0, 0, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("download catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("catalog download failed (%d)", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if err != nil {
		return 0, 0, fmt.Errorf("read catalog: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return 0, 0, fmt.Errorf("catalog is not valid JSON: %w", err)
	}
	if len(c) == 0 {
		return 0, 0, fmt.Errorf("catalog is empty")
	}
	for _, p := range c {
		providers++
		modelCount += len(p.Models)
	}
	if err := store.EnsureDir(store.ConfigDir(), store.DirPerm); err != nil {
		return 0, 0, fmt.Errorf("store catalog dir: %w", err)
	}
	if err := store.WriteFile(Path(), data, store.FilePerm); err != nil {
		return 0, 0, fmt.Errorf("store catalog: %w", err)
	}
	Invalidate() // drop the cached copy so the new file is picked up
	return providers, modelCount, nil
}
