package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"fuckyourcli/types"
)

const maxStreamLineBytes = 8 * 1024 * 1024

// The initial budget bounds connection setup and retries; body bytes renew it.
// HTTP/2 pings do not count as activity on an individual response stream.
type streamRequest struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	mu       sync.Mutex
	timer    *time.Timer
	idle     time.Duration
	deadline time.Time
	closed   bool
}

func newStreamRequest(parent context.Context, idle time.Duration) *streamRequest {
	ctx, cancel := context.WithCancelCause(parent)
	r := &streamRequest{ctx: ctx, cancel: cancel, idle: idle, deadline: time.Now().Add(idle)}
	r.timer = time.AfterFunc(idle, r.expire)
	return r
}

func (r *streamRequest) expire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return
	}
	// A callback already queued before a Reset must not cancel a renewed stream.
	if remaining := time.Until(r.deadline); remaining > 0 {
		r.timer.Reset(remaining)
		return
	}
	r.cancel(fmt.Errorf("provider stream idle for %s: %w", r.idle, context.DeadlineExceeded))
}

func (r *streamRequest) touch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed && r.ctx.Err() == nil {
		r.deadline = time.Now().Add(r.idle)
		r.timer.Reset(r.idle)
	}
}

func (r *streamRequest) close() {
	r.mu.Lock()
	r.closed = true
	r.timer.Stop()
	r.mu.Unlock()
	r.cancel(context.Canceled)
}

func (r *streamRequest) body(body io.ReadCloser) io.ReadCloser {
	r.touch()
	return &activeStreamBody{ReadCloser: body, request: r}
}

type activeStreamBody struct {
	io.ReadCloser
	request *streamRequest
}

func (b *activeStreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.request.touch()
	}
	if err != nil && b.request.ctx.Err() != nil {
		err = context.Cause(b.request.ctx)
	}
	return n, err
}

type streamLine struct {
	text string
	done bool
	err  error
}

// stop closes the body to unblock Read and joins the scanner even on early exits.
func readStreamLines(ctx context.Context, body io.ReadCloser) (<-chan streamLine, func()) {
	readerCtx, cancel := context.WithCancel(ctx)
	lines := make(chan streamLine, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 1024), maxStreamLineBytes)
		for scanner.Scan() {
			select {
			case lines <- streamLine{text: scanner.Text()}:
			case <-readerCtx.Done():
				return
			}
		}
		if readerCtx.Err() != nil {
			return
		}
		select {
		case lines <- streamLine{done: true, err: scanner.Err()}:
		case <-readerCtx.Done():
		}
	}()
	return lines, func() {
		cancel()
		body.Close()
		<-done
	}
}

func validateStreamToolCalls(result *types.StreamResult) error {
	for i := range result.ToolCalls {
		tc := &result.ToolCalls[i]
		if tc.Function.ArgumentsRaw == "" {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.ArgumentsRaw), &args); err != nil {
			return fmt.Errorf("provider stream contains incomplete or invalid arguments for tool %s", tc.Function.Name)
		}
		// Some compatible models encode parameterless calls as JSON null.
		if args == nil {
			tc.Function.Arguments = make(map[string]any)
		}
	}
	return nil
}
