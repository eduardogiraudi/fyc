package agui

import (
	"context"
	"errors"
	"sync"
	"time"

	"fuckyourcli/types"
)

// ── L1 human-in-the-loop ─────────────────────────────────────────────────────
//
// Level 1 of the interaction works like
// this: the tool goroutine parks, the server emits a CUSTOM
// "fyc.interaction_required" event on the run's SSE stream, and the client
// answers out of band with
//
//	POST /runs/{runId}/interactions/{interactionId}
//
// The parked goroutine then resumes with the client's answer. A timeout or a
// client disconnection resolves the interaction as "denied", so a run can never
// stay parked forever.
//
// The broker stays agnostic of package tools: the HTTP payload mirrors
// tools.InteractionRequest/Response field by field, and package main adapts
// between the two.

// Custom event names emitted by the broker.
const (
	// CustomInteractionRequired carries an InteractionRequest payload.
	CustomInteractionRequired = "fyc.interaction_required"
	// CustomInteractionResolved reports an interaction that ended without an
	// answer (timeout or disconnection).
	CustomInteractionResolved = "fyc.interaction_resolved"
)

// InteractionQuestion is one questionary item carried by the payload.
type InteractionQuestion struct {
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	AllowInput bool     `json:"allowInput,omitempty"`
}

// InteractionRequest is the payload of a fyc.interaction_required event.
type InteractionRequest struct {
	// InteractionID identifies this interaction; it also appears in the resolve
	// endpoint path.
	InteractionID string `json:"interactionId"`
	RunID         string `json:"runId"`
	ThreadID      string `json:"threadId"`
	// Kind is "approval", "typed_confirm" or "question".
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Options []string `json:"options,omitempty"`
	// AllowIndex/DenyIndex/SuggestionIndex index into Options.
	AllowIndex      int `json:"allowIndex"`
	DenyIndex       int `json:"denyIndex"`
	SuggestionIndex int `json:"suggestionIndex"`
	// SeeChanges is true when the request carries a diff the client may show in
	// place of the interactive diff viewer.
	SeeChanges   bool             `json:"seeChanges,omitempty"`
	Diff         []types.DiffLine `json:"diff,omitempty"`
	DiffStats    string           `json:"diffStats,omitempty"`
	DiffTitle    string           `json:"diffTitle,omitempty"`
	DiffFilename string           `json:"diffFilename,omitempty"`
	// SeeDetails marks a purely additive payload: Title stays complete (it is
	// never truncated), and DetailsTitle/Details carry optional extra context —
	// the full command, the sandbox envelope, the exact git invocation. A client
	// that ignores these fields is unaffected.
	SeeDetails   bool                  `json:"seeDetails,omitempty"`
	DetailsTitle string                `json:"detailsTitle,omitempty"`
	Details      []string              `json:"details,omitempty"`
	Expect       string                `json:"expect,omitempty"`
	Questions    []InteractionQuestion `json:"questions,omitempty"`
	// TimeoutMS is the server-side wait budget for this interaction.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
}

// InteractionAnswer is the client reply to an interaction.
type InteractionAnswer struct {
	// Choice is the index into Options (approval); nil when the answer only
	// carries text or answers.
	Choice   *int     `json:"choice,omitempty"`
	Text     string   `json:"text,omitempty"`
	Answers  []string `json:"answers,omitempty"`
	Remember bool     `json:"remember,omitempty"`
}

// Interaction errors.
var (
	// ErrNoRun means the context carries no active server run: the caller must
	// fall back to the deterministic headless policy.
	ErrNoRun = errors.New("ag-ui: no active run in context")
	// ErrInteractionTimeout means the client did not answer in time.
	ErrInteractionTimeout = errors.New("ag-ui: interaction timed out")
	// ErrInteractionCanceled means the run was canceled (client disconnected).
	ErrInteractionCanceled = errors.New("ag-ui: interaction canceled")
	// ErrInteractionUnknown means the interaction id is unknown, already
	// answered, or belongs to another run.
	ErrInteractionUnknown = errors.New("ag-ui: unknown interaction")
	// ErrBrokerClosed means the server is shutting down.
	ErrBrokerClosed = errors.New("ag-ui: interaction broker closed")
)

// runState is attached to the run context by the HTTP handler; it gives the
// broker the run's SSE sink and identifiers without package main having to pass
// them through the tool layer.
type runState struct {
	sink     *Sink
	threadID string
	runID    string
	// suspend cancels the run context with an *InterruptError: level-2
	// interactions use it to end the run with the interrupt outcome.
	suspend context.CancelCauseFunc
	// interrupts accumulates the interrupts this run raised; it is the value
	// installed as the cancellation cause, so the run can end with all of them.
	interrupts *InterruptError
}

type runStateKey struct{}

// WithRun attaches the run's sink and identifiers to ctx. It is called by the
// HTTP handler before the runner starts, so every tool executed by that run can
// reach its own SSE stream.
// WithRun attaches the run's sink and identifiers to ctx and makes the context
// cancellable with a cause: the returned context is the run context, and a
// level-2 interrupt cancels it with an *InterruptError so the whole tool stack
// can observe the suspension.
func WithRun(ctx context.Context, sink *Sink, threadID, runID string) context.Context {
	runCtx, suspend := context.WithCancelCause(ctx)
	return context.WithValue(runCtx, runStateKey{}, runState{
		sink:       sink,
		threadID:   threadID,
		runID:      runID,
		suspend:    suspend,
		interrupts: &InterruptError{ThreadID: threadID, RunID: runID},
	})
}

// runStateFrom returns the run state attached to ctx, if any.
func runStateFrom(ctx context.Context) (runState, bool) {
	st, ok := ctx.Value(runStateKey{}).(runState)
	return st, ok
}

// pendingInteraction is one parked tool goroutine.
type pendingInteraction struct {
	id       string
	runID    string
	ch       chan InteractionAnswer
	done     chan struct{}
	answered bool
}

// Broker parks tool goroutines waiting for a client answer and delivers the
// answers posted to the resolve endpoint.
type Broker struct {
	timeout time.Duration

	mu       sync.Mutex
	pending  map[string]*pendingInteraction
	remember map[string]map[string]bool
	closed   bool

	// level-2 interrupt bookkeeping (see interrupts.go)
	interrupts bool
	parked     map[string]*parkedInterrupt
	resolved   map[string]string
	order      map[string][]string
}

// NewBroker builds a broker waiting at most timeout for each answer (a
// non-positive timeout disables the deadline, leaving only cancellation).
func NewBroker(timeout time.Duration) *Broker {
	return &Broker{
		timeout:  timeout,
		pending:  map[string]*pendingInteraction{},
		remember: map[string]map[string]bool{},
		parked:   map[string]*parkedInterrupt{},
		resolved: map[string]string{},
		order:    map[string][]string{},
	}
}

// Timeout returns the configured answer timeout.
func (b *Broker) Timeout() time.Duration { return b.timeout }

// Request parks the calling goroutine until the client answers, the timeout
// expires or ctx is canceled.
//
// rememberKey, when non-empty, enables the per-thread "remember" memory: a
// previous answer with Remember set auto-approves (choice = in.AllowIndex)
// every following request with the same key on the same thread. Nothing is ever
// written to the shared configuration file.
//
// It returns ErrNoRun when ctx carries no run state, so the caller can fall
// back to the headless policy.
//
// With the interrupt mode enabled (level 2, see interrupts.go) it never parks:
// it records a standard AG-UI interrupt, suspends the run and returns
// ErrInterrupted.
func (b *Broker) Request(ctx context.Context, in InteractionRequest, rememberKey string) (InteractionAnswer, error) {
	st, ok := runStateFrom(ctx)
	if !ok || st.sink == nil {
		return InteractionAnswer{}, ErrNoRun
	}

	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return InteractionAnswer{}, ErrNoRun
	}

	in.RunID = st.runID
	in.ThreadID = st.threadID

	if b.InterruptMode() {
		// Level 2: suspend the run instead of parking this goroutine.
		return b.requestInterrupt(ctx, st, in, rememberKey)
	}

	in.InteractionID = NewID("int")
	in.TimeoutMS = b.timeout.Milliseconds()

	if rememberKey != "" && b.isRemembered(st.threadID, rememberKey) {
		allow := in.AllowIndex
		return InteractionAnswer{Choice: &allow, Remember: true}, nil
	}

	p := &pendingInteraction{
		id:    in.InteractionID,
		runID: st.runID,
		ch:    make(chan InteractionAnswer, 1),
		done:  make(chan struct{}),
	}
	b.mu.Lock()
	b.pending[p.id] = p
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, p.id)
		b.mu.Unlock()
	}()

	st.sink.Emit(NewCustom(CustomInteractionRequired, in))

	var timeoutCh <-chan time.Time
	if b.timeout > 0 {
		timer := time.NewTimer(b.timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	var (
		ans InteractionAnswer
		err error
	)
	select {
	case ans = <-p.ch:
	case <-timeoutCh:
		err = ErrInteractionTimeout
	case <-p.done:
		err = ErrBrokerClosed
	case <-ctx.Done():
		err = ErrInteractionCanceled
	}
	if err != nil {
		st.sink.Emit(NewCustom(CustomInteractionResolved, map[string]interface{}{
			"interactionId": in.InteractionID,
			"status":        interactionStatus(err),
		}))
		return InteractionAnswer{}, err
	}

	if rememberKey != "" && ans.Remember {
		b.rememberAnswer(st.threadID, rememberKey)
	}
	return ans, nil
}

// Resolve delivers the client answer to the parked goroutine. runID must match
// the run that opened the interaction.
func (b *Broker) Resolve(runID, interactionID string, ans InteractionAnswer) error {
	b.mu.Lock()
	p, ok := b.pending[interactionID]
	if ok && (p.answered || (runID != "" && p.runID != runID)) {
		ok = false
	}
	if ok {
		p.answered = true
	}
	b.mu.Unlock()
	if !ok {
		return ErrInteractionUnknown
	}
	p.ch <- ans
	return nil
}

// Pending returns the number of interactions currently parked (used by tests
// and by the shutdown log).
func (b *Broker) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// Close unblocks every parked interaction as denied and disables the broker
// (subsequent requests fall back to the headless policy). It is called on
// server shutdown.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	pending := make([]*pendingInteraction, 0, len(b.pending))
	for _, p := range b.pending {
		pending = append(pending, p)
	}
	b.mu.Unlock()

	for _, p := range pending {
		close(p.done)
	}
}

// Forget drops everything the broker knows about a thread: the per-thread
// "remember" memory and every level-2 interrupt record (used when a thread is
// dropped from the session map).
func (b *Broker) Forget(threadID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.remember, threadID)
	for _, id := range b.order[threadID] {
		delete(b.parked, id)
		delete(b.resolved, id)
	}
	delete(b.order, threadID)
}

func (b *Broker) isRemembered(threadID, key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remember[threadID][key]
}

func (b *Broker) rememberAnswer(threadID, key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remember[threadID] == nil {
		b.remember[threadID] = map[string]bool{}
	}
	b.remember[threadID][key] = true
}

// interactionStatus maps a broker error to the status string sent to the client.
func interactionStatus(err error) string {
	switch {
	case errors.Is(err, ErrInteractionTimeout):
		return "timeout"
	case errors.Is(err, ErrInteractionCanceled):
		return "canceled"
	case errors.Is(err, ErrBrokerClosed):
		return "server_shutdown"
	default:
		return "denied"
	}
}
