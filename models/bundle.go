package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fuckyourcli/store"
)

// catalogFileName is the catalog file inside the config directory. There is a
// single source: the catalog is never embedded in the binary, so `models update`
// simply overwrites this file (no precedence rule, no binary bloat).
const catalogFileName = "models.json"

// Path is the location of the catalog file the app reads and `models update`
// writes.
func Path() string {
	return filepath.Join(store.ConfigDir(), catalogFileName)
}

// Load reads the catalog from Path. A missing or unreadable file yields an empty
// catalog: the package is best-effort and never fatal.
func Load() Catalog {
	return parseBytes(readFile(Path()))
}

// The catalog is several megabytes of JSON, so the per-request lookups go
// through a cache keyed by the file's path, size and mtime: a stat is cheap, a
// re-parse is not. The cached value is shared and must never be mutated.
var (
	cacheMu   sync.Mutex
	cacheKey  string
	cacheMod  time.Time
	cacheSize int64
	cacheCat  Catalog
	cacheOK   bool
)

// Cached returns the catalog at Path, re-reading it only when the file changed
// on disk. The result is shared: callers must treat it as read-only.
func Cached() Catalog {
	path := Path()
	fi, err := os.Stat(path)
	if err != nil {
		Invalidate()
		return Catalog{}
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheOK && cacheKey == path && cacheSize == fi.Size() && cacheMod.Equal(fi.ModTime()) {
		return cacheCat
	}
	c := parseBytes(readFile(path))
	cacheKey, cacheMod, cacheSize, cacheCat, cacheOK = path, fi.ModTime(), fi.Size(), c, true
	return c
}

// Invalidate drops the cached catalog. Update calls it after writing the file;
// tests use it to point the cache at a different config directory.
func Invalidate() {
	cacheMu.Lock()
	cacheOK = false
	cacheCat = nil
	cacheMu.Unlock()
}

func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

func parseBytes(data []byte) Catalog {
	if len(data) == 0 {
		return Catalog{}
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}
	}
	return c
}

// providerIDs returns the provider ids in a stable (sorted) order so lookups
// over ambiguous model ids are deterministic.
func (c Catalog) providerIDs() []string {
	ids := make([]string, 0, len(c))
	for id := range c {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// LookupModel finds a model by id, searching every provider. An exact match
// wins; otherwise a model whose id (or canonical id) ends with "/"+id is used.
func (c Catalog) LookupModel(modelID string) (providerID string, m Model, ok bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", Model{}, false
	}
	for _, pid := range c.providerIDs() {
		for _, mm := range c[pid].Models {
			if mm.ID == modelID || (mm.CanonicalID != "" && mm.CanonicalID == modelID) {
				return pid, mm, true
			}
		}
	}
	suffix := "/" + modelID
	for _, pid := range c.providerIDs() {
		for _, mm := range c[pid].Models {
			if strings.HasSuffix(mm.ID, suffix) ||
				(mm.CanonicalID != "" && strings.HasSuffix(mm.CanonicalID, suffix)) {
				return pid, mm, true
			}
		}
	}
	if alias := cloudModelID(modelID); alias != modelID {
		return c.LookupModel(alias)
	}
	return "", Model{}, false
}

// Cloud is a deployment suffix, not a different model or a quantization tag.
func cloudModelID(modelID string) string {
	if strings.HasSuffix(modelID, "-cloud") {
		return strings.TrimSuffix(modelID, "-cloud")
	}
	return strings.TrimSuffix(modelID, ":cloud")
}

// LookupInfo resolves the catalog entry for a model id using the cached
// catalog.
func LookupInfo(modelID string) (providerID string, m Model, ok bool) {
	return Cached().LookupModel(modelID)
}

// ContextWindow returns the documented prompt context window of a model, or 0
// when the catalog does not know it. A model the catalog does not describe has
// no window here: callers must treat zero as "unknown", never as "unlimited".
func (c Catalog) ContextWindow(modelID string) int {
	if _, m, ok := c.LookupModel(modelID); ok {
		return m.Limit.Context
	}
	return 0
}

// ContextWindow resolves a model's context window against the cached catalog.
func ContextWindow(modelID string) int {
	return Cached().ContextWindow(modelID)
}

// ContextWindowIn returns the documented context window of modelID inside the
// catalog provider providerID only, or 0 when that provider is not in the
// catalog or does not list the model. Unlike ContextWindow it never falls back
// to another provider: the same model is often served with a smaller window by
// a host than by its maker, so the caller decides what the fallback is.
func (c Catalog) ContextWindowIn(providerID, modelID string) int {
	modelID = strings.TrimSpace(modelID)
	p, ok := c[providerID]
	if !ok || modelID == "" {
		return 0
	}
	ids := []string{modelID}
	if alias := cloudModelID(modelID); alias != modelID {
		ids = append(ids, alias)
	}
	for _, id := range ids {
		if m, ok := p.Models[id]; ok {
			return m.Limit.Context
		}
		suffix := "/" + id
		// Sorted keys keep the pick deterministic when several entries end
		// with the same suffix.
		keys := make([]string, 0, len(p.Models))
		for k := range p.Models {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			m := p.Models[k]
			if m.ID == id || m.CanonicalID == id || strings.HasSuffix(m.ID, suffix) ||
				(m.CanonicalID != "" && strings.HasSuffix(m.CanonicalID, suffix)) {
				return m.Limit.Context
			}
		}
	}
	return 0
}

// ReasoningInfo renders a short human string for the /reasoning UI, or "" when
// the model is unknown. It is informational only and never affects the request.
func ReasoningInfo(modelID string) string {
	pid, m, ok := LookupInfo(modelID)
	if !ok {
		return ""
	}
	if !m.Reasoning {
		return "catalog (" + pid + "): no reasoning"
	}
	var parts []string
	for _, opt := range m.ReasoningOptions {
		switch opt.Type {
		case "effort":
			parts = append(parts, "effort="+strings.Join(opt.Values, "|"))
		case "budget_tokens":
			parts = append(parts, "budget_tokens")
		case "toggle":
			parts = append(parts, "toggle")
		default:
			if opt.Type != "" {
				parts = append(parts, opt.Type)
			}
		}
	}
	detail := "reasoning"
	if len(parts) > 0 {
		detail += " (" + strings.Join(parts, ", ") + ")"
	}
	return "catalog (" + pid + "): " + detail
}
