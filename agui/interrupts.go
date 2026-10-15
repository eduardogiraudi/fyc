package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── level 2: standard AG-UI interrupts ───────────────────────────────────────
//
// Level 1 (see hitl.go) keeps the HTTP response open and asks the client to
// answer through a fyc-specific control plane. Level 2 uses the interrupt-aware
// run lifecycle of the protocol instead:
//
//	RUN_STARTED … (tool call announced) → RUN_FINISHED {outcome:{type:"interrupt"}}
//	client → POST / {threadId, resume:[{interruptId, status, payload}]}
//	RUN_STARTED (new run) → TOOL_CALL_RESULT → RUN_FINISHED {outcome:{type:"success"}}
//
// The tool layer is synchronous, so the run cannot be left suspended mid-stack:
// when an interaction is required the broker records the interrupt, cancels the
// run context with an *InterruptError and returns ErrInterrupted. The tool loop
// notices the interruption and stops before recording a tool result, so the
// pending tool call (announced in the interrupted run) is executed by the
// resumed run, which emits only its TOOL_CALL_RESULT — exactly the audit trail
// the protocol describes for tool-bound interrupts.
//
// The client answers with resume entries. The broker maps each answer back to
// its tool call and the resumed run replays it; a resume for an already applied
// interrupt is a no-op (idempotency rule of the protocol).

// Interaction kinds (mirrors tools.InteractionKind; the broker stays free of a
// dependency on the tools package).
const (
	KindApproval     = "approval"
	KindTypedConfirm = "typed_confirm"
	KindQuestion     = "question"
)

// Errors of the interrupt lifecycle.
var (
	// ErrInterrupted is returned to the tool layer when the run was suspended for
	// an interrupt: the caller must not treat it as a tool failure.
	ErrInterrupted = errors.New("ag-ui: run interrupted")
	// ErrInterruptUnknown means the resume references an interrupt the broker
	// does not know (or that belongs to another thread).
	ErrInterruptUnknown = errors.New("ag-ui: unknown interrupt")
	// ErrInterruptExpired means the resume arrived after the interrupt's
	// expiresAt.
	ErrInterruptExpired = errors.New("ag-ui: interrupt expired")
	// ErrInterruptConflict means the interrupt was already answered with a
	// different payload.
	ErrInterruptConflict = errors.New("ag-ui: interrupt already answered")
	// ErrInterruptStatus means the resume status is neither "resolved" nor
	// "cancelled".
	ErrInterruptStatus = errors.New("ag-ui: invalid resume status")
	// ErrInterruptPayload means the resume payload is not valid JSON.
	ErrInterruptPayload = errors.New("ag-ui: invalid resume payload")
	// ErrInterruptAbandoned means the client cancelled the interrupt; the tool
	// sees it as an error (questionary) or as a denial (approval/typed confirm).
	ErrInterruptAbandoned = errors.New("ag-ui: interrupt cancelled by the client")
)

// InterruptError is the run error that ends a run with an interrupt outcome.
// It is installed as the cancellation cause of the run context, so every tool
// in the stack can detect the suspension through RunInterrupted.
type InterruptError struct {
	ThreadID   string
	RunID      string
	Interrupts []Interrupt
	// Snapshot is the conversation state at the interrupt boundary. The server
	// emits it as MESSAGES_SNAPSHOT before RUN_FINISHED, as the protocol
	// requires; the runner fills it in when the run unwinds.
	Snapshot []Message
}

// Error implements error.
func (e *InterruptError) Error() string {
	reasons := make([]string, 0, len(e.Interrupts))
	for _, i := range e.Interrupts {
		reasons = append(reasons, i.Reason+":"+i.ID)
	}
	return fmt.Sprintf("ag-ui: run %s interrupted (%s)", e.RunID, strings.Join(reasons, ", "))
}

// Unwrap makes errors.Is(err, ErrInterrupted) work for the tool layer.
func (e *InterruptError) Unwrap() error { return ErrInterrupted }

// RunInterrupted returns the interrupt that suspended the run attached to ctx,
// or nil when the run was not suspended. The tool loop uses it to stop before
// recording a tool result that belongs to the next run.
func RunInterrupted(ctx context.Context) *InterruptError {
	cause := context.Cause(ctx)
	if cause == nil {
		return nil
	}
	var ie *InterruptError
	if errors.As(cause, &ie) {
		return ie
	}
	return nil
}

// ── tool call attribution ────────────────────────────────────────────────────

type toolCallKey struct{}

type toolCallInfo struct {
	ID   string
	Name string
}

// WithToolCall records the tool call a tool is currently executing, so that an
// interaction raised inside it is reported as an interrupt bound to that call
// (reason "tool_call" with a toolCallId, as the protocol prescribes).
func WithToolCall(ctx context.Context, id, name string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCallKey{}, toolCallInfo{ID: id, Name: name})
}

// ToolCallFromContext returns the tool call attached by WithToolCall.
func ToolCallFromContext(ctx context.Context) (id, name string) {
	tc, ok := ctx.Value(toolCallKey{}).(toolCallInfo)
	if !ok {
		return "", ""
	}
	return tc.ID, tc.Name
}

// ── broker state ─────────────────────────────────────────────────────────────

// parkedInterrupt is an interrupt a run raised and that has not been consumed by
// a resumed run yet.
type parkedInterrupt struct {
	interrupt   Interrupt
	threadID    string
	runID       string
	toolCallID  string
	ident       string
	request     InteractionRequest
	rememberKey string
	expiresAt   time.Time

	answered  bool
	consumed  bool
	cancelled bool
	answer    InteractionAnswer
	// sig is the signature of the resume that answered the interrupt; a repeated
	// resume with the same signature is a no-op.
	sig string
}

// interactionOutcome is the answer consumed by a replayed tool call.
type interactionOutcome struct {
	answer    InteractionAnswer
	cancelled bool
}

// SetInterruptMode enables the level-2 interrupt lifecycle on the broker. When
// disabled (the default) Request parks the tool goroutine and waits for the
// level-1 resolve endpoint instead.
func (b *Broker) SetInterruptMode(enabled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.interrupts = enabled
	b.ensureInterruptMapsLocked()
}

// InterruptMode reports whether the broker drives the standard interrupt
// lifecycle.
func (b *Broker) InterruptMode() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.interrupts
}

// ensureInterruptMapsLocked lazily creates the level-2 bookkeeping. Caller holds
// the mutex.
func (b *Broker) ensureInterruptMapsLocked() {
	if b.parked == nil {
		b.parked = map[string]*parkedInterrupt{}
	}
	if b.resolved == nil {
		b.resolved = map[string]string{}
	}
	if b.order == nil {
		b.order = map[string][]string{}
	}
}

// requestInterrupt is the level-2 counterpart of the parking logic in Request.
// It first looks for an answer already given by the client (a resumed run
// replays the pending tool calls), then for a per-thread "remember" grant, and
// otherwise records a new interrupt and suspends the run.
func (b *Broker) requestInterrupt(ctx context.Context, st runState, in InteractionRequest, rememberKey string) (InteractionAnswer, error) {
	if out, ok := b.takeInterrupt(st.threadID, ctx, in); ok {
		if out.cancelled {
			return cancelAnswer(in)
		}
		if rememberKey != "" && out.answer.Remember {
			b.rememberAnswer(st.threadID, rememberKey)
		}
		return out.answer, nil
	}

	if rememberKey != "" && b.isRemembered(st.threadID, rememberKey) {
		allow := in.AllowIndex
		return InteractionAnswer{Choice: &allow, Remember: true}, nil
	}

	if st.suspend == nil {
		// Defensive: without a cancellable run context the run cannot be
		// suspended, so fall back to the deterministic headless policy.
		return InteractionAnswer{}, ErrNoRun
	}

	intr, err := b.parkInterrupt(ctx, st, in, rememberKey)
	if err != nil {
		return InteractionAnswer{}, err
	}
	// Accumulate the interrupt on the run state: a run that raises more than one
	// (a batch of tool calls) ends with all of them in the outcome. The cancel is
	// idempotent, so the run is suspended by the first one.
	ie := st.interrupts
	if ie == nil {
		ie = &InterruptError{ThreadID: st.threadID, RunID: st.runID}
	}
	ie.Interrupts = append(ie.Interrupts, intr)
	st.suspend(ie)
	return InteractionAnswer{}, ErrInterrupted
}

// parkInterrupt records the interrupt for the thread.
func (b *Broker) parkInterrupt(ctx context.Context, st runState, in InteractionRequest, rememberKey string) (Interrupt, error) {
	id := NewID("int")
	toolCallID, _ := ToolCallFromContext(ctx)

	// The fyc payload mirrors the level-1 interaction payload byte for byte (the
	// diff, the options, the indices, the questions, …), so a client that already
	// renders level 1 can render interrupts with no extra mapping.
	in.InteractionID = id
	in.TimeoutMS = b.timeout.Milliseconds()

	intr := Interrupt{
		ID:             id,
		Reason:         interruptReason(in.Kind, toolCallID),
		Message:        interruptMessage(in),
		ToolCallID:     toolCallID,
		ResponseSchema: InterruptResponseSchema(in.Kind),
		Metadata:       map[string]interface{}{"fyc": in},
	}

	p := &parkedInterrupt{
		threadID:    st.threadID,
		runID:       st.runID,
		toolCallID:  toolCallID,
		ident:       interactionIdentity(in),
		request:     in,
		rememberKey: rememberKey,
	}
	if b.timeout > 0 {
		p.expiresAt = time.Now().Add(b.timeout)
		intr.ExpiresAt = p.expiresAt.UTC().Format(time.RFC3339)
	}
	p.interrupt = intr

	b.mu.Lock()
	b.ensureInterruptMapsLocked()
	if b.closed {
		b.mu.Unlock()
		return Interrupt{}, ErrNoRun
	}
	b.parked[id] = p
	b.order[st.threadID] = append(b.order[st.threadID], id)
	b.mu.Unlock()
	return intr, nil
}

// takeInterrupt consumes the answer the client gave for the request in a
// previous run. The originating tool call is matched first (a resumed run
// re-executes exactly the same call), then the request identity (providers that
// do not populate tool-call ids).
func (b *Broker) takeInterrupt(threadID string, ctx context.Context, in InteractionRequest) (interactionOutcome, bool) {
	toolCallID, _ := ToolCallFromContext(ctx)
	ident := interactionIdentity(in)

	b.mu.Lock()
	defer b.mu.Unlock()

	// A tool call can raise more than one decision (the git tier, then a typed
	// confirmation), so the match requires the request identity too — while the
	// tool-call-only fallback is not enough: replaying the same call must find the
	// very same answer again and again. An exact match (call + identity) is
	// therefore left reusable, the identity-only one (providers that do not carry
	// tool-call ids) is single use, so an answer can never leak to another call.
	byCall, byIdent := -1, -1
	for i, id := range b.order[threadID] {
		p := b.parked[id]
		if p == nil || !p.answered || p.consumed {
			continue
		}
		if toolCallID != "" && p.toolCallID == toolCallID && p.ident == ident {
			byCall = i
			break
		}
		if byIdent < 0 && p.ident == ident && (p.toolCallID == "" || toolCallID == "") {
			byIdent = i
		}
	}
	switch {
	case byCall >= 0:
		p := b.parked[b.order[threadID][byCall]]
		return interactionOutcome{answer: p.answer, cancelled: p.cancelled}, true
	case byIdent >= 0:
		p := b.parked[b.order[threadID][byIdent]]
		p.consumed = true
		return interactionOutcome{answer: p.answer, cancelled: p.cancelled}, true
	}
	return interactionOutcome{}, false
}

// OpenInterrupts returns the interrupts of a thread that are still waiting for
// an answer, in creation order. Expired interrupts are dropped (they can only be
// resumed with an error anyway).
func (b *Broker) OpenInterrupts(threadID string) []Interrupt {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	var out []Interrupt
	for _, id := range b.order[threadID] {
		p := b.parked[id]
		if p == nil || p.answered {
			continue
		}
		if !p.expiresAt.IsZero() && now.After(p.expiresAt) {
			// Expired and unanswered: it can never be resumed successfully, so
			// it must not block new input on the thread.
			p.cancelled = true
			continue
		}
		out = append(out, p.interrupt)
	}
	return out
}

// Resume applies the client's answers to the interrupts of a run. It is called
// by the server before the resumed run starts, so the replayed tool calls find
// their answer ready.
//
// Contract rules enforced here (see the AG-UI interrupts specification):
// an entry must reference a known interrupt of the same thread (rule 2), a
// repeated resume with the same (interruptId, status, payload) is a no-op
// (rule 5) while a conflicting one is an error, and a resume past expiresAt is
// an error (rule 7).
func (b *Broker) Resume(threadID string, entries []ResumeEntry) error {
	if len(entries) == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureInterruptMapsLocked()

	for _, e := range entries {
		if strings.TrimSpace(e.InterruptID) == "" {
			return fmt.Errorf("%w: missing interruptId", ErrInterruptUnknown)
		}
		switch e.Status {
		case ResumeStatusResolved, ResumeStatusCancelled:
		default:
			return fmt.Errorf("%w: %q (want %q or %q)", ErrInterruptStatus, e.Status, ResumeStatusResolved, ResumeStatusCancelled)
		}

		sig := resumeSignature(e)
		if prev, ok := b.resolved[e.InterruptID]; ok {
			if prev == sig {
				continue // idempotent replay
			}
			return fmt.Errorf("%w: %s", ErrInterruptConflict, e.InterruptID)
		}

		p := b.parked[e.InterruptID]
		if p == nil || p.threadID != threadID {
			return fmt.Errorf("%w: %s", ErrInterruptUnknown, e.InterruptID)
		}
		if !p.expiresAt.IsZero() && time.Now().After(p.expiresAt) {
			return fmt.Errorf("%w: %s", ErrInterruptExpired, e.InterruptID)
		}
		if p.answered {
			if p.sig == sig {
				b.resolved[e.InterruptID] = sig
				continue
			}
			return fmt.Errorf("%w: %s", ErrInterruptConflict, e.InterruptID)
		}

		answer, cancelled, err := resumeAnswer(p, e)
		if err != nil {
			return err
		}
		p.answer, p.cancelled, p.answered, p.sig = answer, cancelled, true, sig
		if !cancelled && answer.Remember && p.rememberKey != "" {
			// "remember" applies to the whole thread: record the grant now, so a
			// following call on the same target is not asked again (the grant is
			// in memory only, never written to the shared configuration).
			if b.remember[threadID] == nil {
				b.remember[threadID] = map[string]bool{}
			}
			b.remember[threadID][p.rememberKey] = true
		}
		b.resolved[e.InterruptID] = sig
	}
	return nil
}

// resumeSignature identifies a resume entry for the idempotency rule.
func resumeSignature(e ResumeEntry) string {
	return e.Status + "|" + string(e.Payload)
}

// resumeAnswer maps a resume entry onto the tool contract.
func resumeAnswer(p *parkedInterrupt, e ResumeEntry) (InteractionAnswer, bool, error) {
	req := p.request
	if e.Status == ResumeStatusCancelled {
		// The client abandoned: the answer is a denial (or an error for a
		// questionary, mapped by cancelAnswer).
		return InteractionAnswer{}, true, nil
	}

	var payload struct {
		Approved *bool    `json:"approved"`
		Choice   *int     `json:"choice"`
		Remember bool     `json:"remember"`
		Text     string   `json:"text"`
		Answers  []string `json:"answers"`
	}
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return InteractionAnswer{}, false, fmt.Errorf("%w: %v", ErrInterruptPayload, err)
		}
	}

	switch req.Kind {
	case KindQuestion:
		return InteractionAnswer{Answers: payload.Answers}, false, nil

	case KindTypedConfirm:
		// An empty (or wrong) text never matches the expected word, so the tool
		// aborts the operation exactly like a TUI mismatch.
		return InteractionAnswer{Text: payload.Text}, false, nil

	default: // approval
		// Keep the free-text suggestion whatever the selection is: a client may
		// answer with the "Deny with suggestion" option index (the very index
		// published in metadata.fyc.suggestionIndex) plus its text, and the text
		// must survive. Level 1 keeps choice and text independent too.
		ans := InteractionAnswer{Remember: payload.Remember, Text: payload.Text}
		switch {
		case payload.Choice != nil:
			ans.Choice = payload.Choice
		case payload.Approved != nil && *payload.Approved:
			allow := req.AllowIndex
			ans.Choice = &allow
		case strings.TrimSpace(payload.Text) != "":
			// Denial with a suggestion: the option list keeps the suggestion
			// entry at SuggestionIndex in both live modes.
			suggestion := req.SuggestionIndex
			ans.Choice = &suggestion
		default:
			deny := req.DenyIndex
			ans.Choice = &deny
		}
		// "Allow always" picked by index means the same as remember=true.
		if ans.Choice != nil && OptionImpliesRemember(req.Options, *ans.Choice) {
			ans.Remember = true
		}
		return ans, false, nil
	}
}

// cancelAnswer maps an abandoned interrupt onto the tool contract: approvals and
// typed confirmations deny, a questionary reports the error to the model.
func cancelAnswer(in InteractionRequest) (InteractionAnswer, error) {
	switch in.Kind {
	case KindTypedConfirm:
		return InteractionAnswer{}, nil
	case KindQuestion:
		return InteractionAnswer{}, ErrInterruptAbandoned
	default:
		deny := in.DenyIndex
		return InteractionAnswer{Choice: &deny}, nil
	}
}

// OptionImpliesRemember reports whether the selected option means "always": the
// AG-UI clients pick it from the same option list the TUI shows ("Allow
// always").
func OptionImpliesRemember(options []string, choice int) bool {
	if choice < 0 || choice >= len(options) {
		return false
	}
	return strings.Contains(strings.ToLower(options[choice]), "always")
}

// interruptReason picks the reason from the protocol taxonomy.
func interruptReason(kind, toolCallID string) string {
	switch kind {
	case KindQuestion:
		return ReasonInputRequired
	case KindTypedConfirm:
		return ReasonConfirmation
	default:
		if toolCallID != "" {
			return ReasonToolCall
		}
		return ReasonConfirmation
	}
}

// interruptMessage builds the human-readable fallback content: universal
// clients render exactly this string.
func interruptMessage(in InteractionRequest) string {
	if msg := strings.TrimSpace(in.Title); msg != "" {
		return msg
	}
	if in.Kind == KindQuestion {
		parts := make([]string, 0, len(in.Questions))
		for _, q := range in.Questions {
			parts = append(parts, q.Question)
		}
		return strings.Join(parts, "\n")
	}
	if in.Expect != "" {
		return "Type " + strconv.Quote(in.Expect) + " to confirm"
	}
	return "Approval required"
}

// interactionIdentity fingerprints an interaction request, for the fallback
// matching used when the provider does not populate tool-call ids.
func interactionIdentity(in InteractionRequest) string {
	parts := []string{in.Kind, in.Title, in.DiffFilename, in.Expect}
	for _, q := range in.Questions {
		parts = append(parts, q.Question)
	}
	return strings.Join(parts, "\x1f")
}

// InterruptResponseSchema returns the JSON schema of the resume payload expected
// for an interaction kind. It is the capability signal the client validates
// against; the fyc-specific option indices travel in metadata.fyc.
func InterruptResponseSchema(kind string) map[string]interface{} {
	switch kind {
	case KindQuestion:
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"answers": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "one answer per question, in the order of metadata.fyc.questions",
				},
			},
			"required": []string{"answers"},
		}
	case KindTypedConfirm:
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"text": map[string]interface{}{
					"type":        "string",
					"description": "the exact word the agent asked to type (metadata.fyc.expect)",
				},
			},
			"required": []string{"text"},
		}
	default:
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"approved": map[string]interface{}{
					"type":        "boolean",
					"description": "true to allow the action, false to deny it",
				},
				"choice": map[string]interface{}{
					"type":        "integer",
					"description": "index into metadata.fyc.options; overrides approved",
				},
				"text": map[string]interface{}{
					"type":        "string",
					"description": "free text, e.g. the denial suggestion",
				},
				"remember": map[string]interface{}{
					"type":        "boolean",
					"description": "apply this answer to every following request with the same target on this thread",
				},
			},
			"required": []string{"approved"},
		}
	}
}
