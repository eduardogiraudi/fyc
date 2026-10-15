// Package logger records provider diagnostics grouped by conversation.
package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

type contextKey int

const (
	sessionKey contextKey = iota
	requestKey
)

type scope struct {
	session string
	role    string
	agent   string
}

// WithSession keeps the same directory across turns and process restarts.
func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey, scope{session: id, role: "main"})
}

// WithAgent derives an immutable scope; parallel agents never share mutable labels.
func WithAgent(ctx context.Context, role, id string) context.Context {
	s, _ := ctx.Value(sessionKey).(scope)
	s.role, s.agent = role, id
	return context.WithValue(ctx, sessionKey, s)
}

func getLogDir() string { return filepath.Join(store.CacheDir(), "logs") }

const (
	rootLockTimeout       = 2 * time.Second
	rootLockRetryInterval = 25 * time.Millisecond
)

// Diagnostics must not wait indefinitely for another process's root lock.
func lockRoot() (*store.FileLock, error) {
	deadline := time.Now().Add(rootLockTimeout)
	for {
		lock, err := store.TryLock(filepath.Join(getLogDir(), ".lock"))
		if !errors.Is(err, store.ErrLocked) {
			return lock, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, err
		}
		time.Sleep(min(rootLockRetryInterval, remaining))
	}
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// Metadata excludes endpoints and credentials: URLs can carry API keys.
type Metadata struct {
	Provider string `json:"provider"`
	Name     string `json:"provider_name,omitempty"`
	Model    string `json:"model"`
}

type Event struct {
	Schema    int       `json:"schema"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id"`
	RequestID string    `json:"request_id"`
	Role      string    `json:"role"`
	AgentID   string    `json:"agent_id,omitempty"`
	Type      string    `json:"type"`
	Metadata
	DurationMS       int64  `json:"duration_ms,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	CachedTokens     int    `json:"cached_tokens,omitempty"`
	ToolCalls        int    `json:"tool_calls,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"`
	Interrupted      bool   `json:"interrupted,omitempty"`
	ErrorKind        string `json:"error_kind,omitempty"`
	Attempt          int    `json:"attempt,omitempty"`
	HTTPStatus       int    `json:"http_status,omitempty"`
	DelayMS          int64  `json:"delay_ms,omitempty"`
}

type loggedProvider struct {
	provider types.Provider
	meta     Metadata
	secrets  func() []string
}

// Wrap observes normalized results, not raw HTTP payloads. Without a session
// scope it is a no-op, including direct provider calls from library tests.
func Wrap(p types.Provider, meta Metadata, secrets ...string) types.Provider {
	values := append([]string(nil), secrets...)
	return WrapWithSecrets(p, meta, func() []string { return values })
}

// WrapWithSecrets samples credentials before and after a call to redact tokens
// replaced by reactive authentication refreshes as well as the original ones.
func WrapWithSecrets(p types.Provider, meta Metadata, secrets func() []string) types.Provider {
	return &loggedProvider{provider: p, meta: meta, secrets: secrets}
}

func (p *loggedProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	s, _ := ctx.Value(sessionKey).(scope)
	if !validID(s.session) {
		return p.provider.StreamChat(ctx, messages, tools, sink)
	}
	r, err := begin(s, p.meta)
	if err != nil {
		store.Notice("logs: %v", err)
		return p.provider.StreamChat(ctx, messages, tools, sink)
	}
	defer r.release()
	var secrets []string
	if r.debug && p.secrets != nil {
		secrets = append(secrets, p.secrets()...)
	}
	ctx = context.WithValue(ctx, requestKey, r)
	result, callErr := p.provider.StreamChat(ctx, messages, tools, sink)
	if r.debug && p.secrets != nil {
		secrets = append(secrets, p.secrets()...)
	}
	if err := r.finish(ctx, result, callErr, secrets); err != nil {
		store.Notice("logs: %v", err)
	}
	return result, callErr
}

type request struct {
	event   Event
	start   time.Time
	lease   *store.FileLock
	debug   bool
	attempt atomic.Int64
}

func begin(s scope, meta Metadata) (*request, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	r := &request{
		start: time.Now(), debug: os.Getenv("FYC_LOG_DEBUG") == "1",
		event: Event{Schema: 1, SessionID: s.session, RequestID: hex.EncodeToString(b[:]), Role: s.role, AgentID: s.agent, Metadata: meta},
	}
	lock, err := lockRoot()
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	dir := r.dir()
	if err := store.EnsureDir(filepath.Join(dir, ".requests"), store.DirPerm); err != nil {
		return nil, err
	}
	r.lease, err = store.TryLock(filepath.Join(dir, ".requests", r.event.RequestID))
	if err != nil {
		return nil, err
	}
	if err := r.appendEvent(Event{Type: "request_started"}); err != nil {
		r.release()
		return nil, err
	}
	return r, nil
}

func (r *request) dir() string { return filepath.Join(getLogDir(), r.event.SessionID) }

func (r *request) release() {
	if r.lease == nil {
		return
	}
	path := r.lease.Path()
	_ = r.lease.Unlock()
	_ = os.Remove(path)
}

// HTTPAttempt records status/retry metadata only, never headers, URLs or bodies.
// Attempts are numbered per logical request, including authentication refreshes.
func HTTPAttempt(ctx context.Context, status int, delay time.Duration, networkError bool) {
	r, _ := ctx.Value(requestKey).(*request)
	if r == nil {
		return
	}
	e := Event{Type: "request_http", Attempt: int(r.attempt.Add(1)), HTTPStatus: status, DelayMS: delay.Milliseconds()}
	if networkError {
		e.ErrorKind = "network_error"
	}
	lock, err := lockRoot()
	if err == nil {
		err = r.appendEvent(e)
		_ = lock.Unlock()
	}
	if err != nil {
		store.Notice("logs: %v", err)
	}
}

func (r *request) finish(ctx context.Context, result *types.StreamResult, callErr error, secrets []string) error {
	lock, err := lockRoot()
	if err != nil {
		r.release()
		return err
	}
	defer lock.Unlock()
	defer r.release()
	e := Event{Type: "request_finished", DurationMS: time.Since(r.start).Milliseconds()}
	if result != nil {
		e.PromptTokens, e.CompletionTokens, e.CachedTokens = result.PromptTokens, result.CompletionTokens, result.CachedTokens
		e.ToolCalls, e.FinishReason, e.Interrupted = len(result.ToolCalls), result.FinishReason, result.Interrupted
	}
	if callErr != nil {
		e.Type, e.ErrorKind = "request_failed", "provider_error"
		// Provider errors may contain entire response bodies or authenticated URLs.
		// Persist a classification, never the original error string.
		switch {
		case errors.Is(callErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			e.ErrorKind = "deadline_exceeded"
		case errors.Is(callErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			e.ErrorKind = "canceled"
		}
	}
	if ctx.Err() != nil {
		e.Interrupted = true
	}
	err = r.appendEvent(e)
	if r.debug && result != nil {
		err = errors.Join(err, r.writeResponse(result, secrets))
	}
	// Release before pruning so completed conversations are eligible.
	r.release()
	r.lease = nil
	return errors.Join(err, cleanupOldLogsIfDue(getLogDir(), time.Now()))
}

func (r *request) appendEvent(e Event) error {
	e.Schema, e.Timestamp = 1, time.Now().UTC()
	e.SessionID, e.RequestID = r.event.SessionID, r.event.RequestID
	e.Role, e.AgentID, e.Metadata = r.event.Role, r.event.AgentID, r.event.Metadata
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return appendLine(filepath.Join(r.dir(), "events.jsonl"), append(data, '\n'))
}

// Repair only an unfinished last line after a crash; complete records stay intact.
func appendLine(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, store.FilePerm)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(store.FilePerm); err != nil {
		return err
	}
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	var buf [4096]byte
	pos := end
	for pos > 0 {
		start := max(int64(0), pos-int64(len(buf)))
		n, err := f.ReadAt(buf[:pos-start], start)
		if err != nil && err != io.EOF {
			return err
		}
		if i := strings.LastIndexByte(string(buf[:n]), '\n'); i >= 0 {
			end = start + int64(i) + 1
			break
		}
		pos, end = start, start
	}
	if err := f.Truncate(end); err != nil {
		return err
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Truncate(end)
		return err
	}
	return f.Sync()
}
