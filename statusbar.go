package main

import (
	"context"
	"sync"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// statusHookRunner remembers the last useful status-bar hook value, keyed by
// provider. It is shared by the per-turn and the periodic refresh so both see
// the same "last useful value".
var statusHookRunner = &tools.StatusHookRunner{}

// statusHookCancel stops the background refresher; it is set by startStatusHook
// once the Tea program is running.
var (
	statusHookCancelMu sync.Mutex
	statusHookCancel   context.CancelFunc
)

// startStatusHook performs the initial status-bar hook refresh and launches the
// periodic refresher. It must be called once the Tea program is running (see
// chatModel.Init): sending to a Program before Run panics.
func startStatusHook(s *ChatSession) {
	if s == nil {
		return
	}
	refreshStatusHook(s.Sink, s.RestrictedRoot)

	ctx, cancel := context.WithCancel(context.Background())
	statusHookCancelMu.Lock()
	if statusHookCancel != nil {
		statusHookCancel()
	}
	statusHookCancel = cancel
	statusHookCancelMu.Unlock()
	go runStatusHookRefresher(ctx, s)
}

// stopStatusHook cancels the background refresher, if any.
func stopStatusHook() {
	statusHookCancelMu.Lock()
	if statusHookCancel != nil {
		statusHookCancel()
		statusHookCancel = nil
	}
	statusHookCancelMu.Unlock()
}

// refreshStatusHookOnTurn re-runs the status-bar hook after an assistant turn,
// unless the active hook opted out with RefreshOnTurn=false. When the hook is
// disabled this still runs (and clears any stale line); a disabled hook is
// cheap because Refresh returns immediately.
func refreshStatusHookOnTurn(sink types.OutputSink, root string) {
	if h := config.ActiveStatusHook(); h.Enabled && !h.RefreshOnTurn {
		return
	}
	refreshStatusHook(sink, root)
}

// refreshStatusHook runs the active status-bar hook in the background and
// pushes the result to the sink. It never blocks the caller: a slow or failing
// hook must not stall generation. Sinks that do not implement StatusLineSink
// (AG-UI, silent) are ignored.
func refreshStatusHook(sink types.OutputSink, root string) {
	sl, ok := sink.(types.StatusLineSink)
	if !ok {
		return
	}
	go func() {
		line, show := statusHookRunner.Refresh(root)
		if show {
			sl.WriteStatusLine(line)
		} else {
			sl.WriteStatusLine("")
		}
	}()
}

// runStatusHookRefresher re-runs the status-bar hook every IntervalSeconds
// while the active hook declares one. When no interval is configured it idles
// (and re-reads the configuration, so a hook enabled at runtime is picked up).
// It returns when ctx is cancelled.
func runStatusHookRefresher(ctx context.Context, s *ChatSession) {
	for {
		h := config.ActiveStatusHook()
		enabled := h.Enabled && h.IntervalSeconds > 0
		wait := 15 * time.Second
		if enabled {
			wait = time.Duration(h.IntervalSeconds) * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			if enabled && s != nil {
				refreshStatusHook(s.Sink, s.RestrictedRoot)
			}
		}
	}
}
