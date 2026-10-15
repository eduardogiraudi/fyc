package providers

import (
	"context"
	"path/filepath"
	"sync"

	"fuckyourcli/codexversion"
	"fuckyourcli/store"
)

// Lazy initialization keeps other providers and startup free of version lookups.
var codexVersionResolver = sync.OnceValue(func() *codexversion.Resolver {
	return codexversion.NewResolver(filepath.Join(store.CacheDir(), "codex-version.json"))
})

var resolveCodexClientVersion = func(ctx context.Context) string {
	return codexVersionResolver().Resolve(ctx)
}
