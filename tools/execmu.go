package tools

import (
	"context"
	"sync"
)

// executeMuReleaseKey carries the mutex-release hook attached by
// ExecuteToolWithSpinner.
type executeMuReleaseKey struct{}

type executeMuRelease func(wait func() error) error

// withExecuteMuRelease attaches a hook that temporarily releases the global
// tool-execution mutex while a tool waits for external input. The AG-UI server
// uses it for level-1 human-in-the-loop interactions: without
// it a parked interaction would serialize every other tool in the process.
func withExecuteMuRelease(ctx context.Context, mu *sync.Mutex) context.Context {
	return context.WithValue(ctx, executeMuReleaseKey{}, executeMuRelease(func(wait func() error) error {
		mu.Unlock()
		defer mu.Lock()
		return wait()
	}))
}

// ReleaseExecuteMu runs wait with the global tool-execution mutex released when
// ctx carries the hook injected by ExecuteToolWithSpinner; otherwise it just
// runs wait. It must be called by the goroutine currently holding the mutex
// (i.e. from inside a tool implementation).
func ReleaseExecuteMu(ctx context.Context, wait func() error) error {
	if h, ok := ctx.Value(executeMuReleaseKey{}).(executeMuRelease); ok {
		return h(wait)
	}
	return wait()
}
