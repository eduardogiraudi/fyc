package agui

import (
	"encoding/json"
	"io"
	"sync"

	"fuckyourcli/types"
)

// Sink implements types.OutputSink (and the optional types.ToolEventSink) by
// translating the agent's output calls into AG-UI events written as SSE frames.
//
// A Sink is used by a single run goroutine, but a mutex protects the writer
// because sub-agent progress reporting can call into it concurrently.
type Sink struct {
	mu    sync.Mutex
	w     io.Writer
	flush func()
	err   error

	threadID string
	runID    string

	// text message state
	textOpen  bool
	textMsgID string

	// reasoning state
	reasoningStarted bool
	reasoningOpen    bool
	reasoningMsgID   string

	// metadata collected during the run (token usage, provider info)
	metadata map[string]interface{}
}

// NewSink builds a Sink writing to w. flush, when non-nil, flushes the
// underlying HTTP response after every event.
func NewSink(w io.Writer, flush func(), threadID, runID string) *Sink {
	return &Sink{w: w, flush: flush, threadID: threadID, runID: runID}
}

// Err returns the first write error encountered, if any.
func (s *Sink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Metadata returns a copy of the metadata collected during the run.
func (s *Sink) Metadata() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.metadata) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(s.metadata))
	for k, v := range s.metadata {
		out[k] = v
	}
	return out
}

// Emit writes a single event. It is exported so the server can drive the
// lifecycle events (RUN_STARTED / RUN_FINISHED / RUN_ERROR).
func (s *Sink) Emit(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeLocked(ev)
}

// writeLocked serialises ev and writes one SSE frame. Caller holds the lock.
func (s *Sink) writeLocked(ev Event) {
	if s.err != nil {
		return
	}
	data, err := encodeEvent(ev)
	if err != nil {
		s.err = err
		return
	}
	if _, err := io.WriteString(s.w, "data: "); err != nil {
		s.err = err
		return
	}
	if _, err := s.w.Write(data); err != nil {
		s.err = err
		return
	}
	if _, err := io.WriteString(s.w, "\n\n"); err != nil {
		s.err = err
		return
	}
	if s.flush != nil {
		s.flush()
	}
}

func (s *Sink) closeTextLocked() {
	if s.textOpen {
		s.writeLocked(TextMessageEnd{Base: Base{Type: EvTextMessageEnd}, MessageID: s.textMsgID})
		s.textOpen = false
	}
}

func (s *Sink) closeReasoningLocked() {
	if s.reasoningOpen {
		s.writeLocked(ReasoningMessageEnd{Base: Base{Type: EvReasoningMessageEnd}, MessageID: s.reasoningMsgID})
		s.reasoningOpen = false
	}
	if s.reasoningStarted {
		s.writeLocked(ReasoningEnd{Base: Base{Type: EvReasoningEnd}, MessageID: s.reasoningMsgID})
		s.reasoningStarted = false
	}
}

// ── types.OutputSink ─────────────────────────────────────────────────────────

// WriteText emits TEXT_MESSAGE_START (lazily, on the first token) followed by
// TEXT_MESSAGE_CONTENT deltas.
func (s *Sink) WriteText(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.textOpen {
		s.textMsgID = NewID("msg")
		s.textOpen = true
		s.writeLocked(TextMessageStart{Base: Base{Type: EvTextMessageStart}, MessageID: s.textMsgID, Role: "assistant"})
	}
	s.writeLocked(TextMessageContent{Base: Base{Type: EvTextMessageContent}, MessageID: s.textMsgID, Delta: token})
}

// WriteReasoning emits the reasoning triade.
func (s *Sink) WriteReasoning(delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.reasoningStarted {
		s.reasoningStarted = true
		// REASONING_START itself carries the message id, so it is generated
		// before the block is opened.
		s.reasoningMsgID = NewID("reasoning")
		s.writeLocked(ReasoningStart{Base: Base{Type: EvReasoningStart}, MessageID: s.reasoningMsgID})
	}
	if !s.reasoningOpen {
		s.reasoningOpen = true
		s.writeLocked(ReasoningMessageStart{Base: Base{Type: EvReasoningMessageStart}, MessageID: s.reasoningMsgID, Role: "reasoning"})
	}
	s.writeLocked(ReasoningMessageContent{Base: Base{Type: EvReasoningMessageContent}, MessageID: s.reasoningMsgID, Delta: delta})
}

// WriteNewline closes the current text message (the CLI uses it as a message
// separator).
func (s *Sink) WriteNewline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeTextLocked()
}

// WriteInterrupt emits a fyc-specific CUSTOM event.
func (s *Sink) WriteInterrupt() {
	s.Emit(NewCustom("fyc.interrupt", nil))
}

// WriteDiffCraft reports replace_anchors statistics as a CUSTOM event.
func (s *Sink) WriteDiffCraft(add, del int) {
	s.Emit(NewCustom("fyc.replace_anchors", map[string]int{"add": add, "del": del}))
}

// FinishDiffCraft is a no-op: the statistics are already emitted.
func (s *Sink) FinishDiffCraft() {}

// WriteStats records provider/token statistics to attach to RUN_FINISHED.
func (s *Sink) WriteStats(providerInfo, tokenStats string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metadata == nil {
		s.metadata = map[string]interface{}{}
	}
	if providerInfo != "" {
		s.metadata["providerInfo"] = providerInfo
	}
	if tokenStats != "" {
		s.metadata["tokenStats"] = tokenStats
	}
}

// WriteToolOutput is the unstructured fallback. When the loop supports
// ToolEventSink (as it does for this sink) it is never called; a no-op keeps
// the contract intact without emitting duplicate TOOL_CALL_RESULT events.
func (s *Sink) WriteToolOutput(label, res string) {}

// StartSpinner and StopSpinner have no SSE representation.
func (s *Sink) StartSpinner(label string) {}
func (s *Sink) StopSpinner()              {}

// Reset closes any message left open by the previous turn.
func (s *Sink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeTextLocked()
	s.closeReasoningLocked()
}

// Finalize closes every open message; the provider calls it at the end of a
// StreamChat.
func (s *Sink) Finalize() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeTextLocked()
	s.closeReasoningLocked()
	if s.flush != nil {
		s.flush()
	}
}

// SelectOption always returns 0: interactive prompts are handled by the
// headless Interactor before reaching a sink.
func (s *Sink) SelectOption(title string, options []string) int { return 0 }

// ReadLine returns no input.
func (s *Sink) ReadLine() (string, bool) { return "", false }

// ── subagents (phase 2: no-op in the MVP) ────────────────────────────────────

func (s *Sink) StartSubagent(id, label string)          {}
func (s *Sink) UpdateSubagent(id, status string)        {}
func (s *Sink) FinishSubagent(id, label, result string) {}
func (s *Sink) StartSubagentBatch(count int)            {}
func (s *Sink) FinishSubagentBatch()                    {}
func (s *Sink) StartSubagentPanel(panel interface{})    {}

// ── types.ToolEventSink ──────────────────────────────────────────────────────

// ToolCallStarted emits TOOL_CALL_START, TOOL_CALL_ARGS and TOOL_CALL_END. The
// full argument object is available up-front, so the arguments are sent in a
// single delta.
func (s *Sink) ToolCallStarted(id, name string, args map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Close any open text/reasoning message before the tool call.
	s.closeTextLocked()
	s.closeReasoningLocked()
	s.writeLocked(ToolCallStart{Base: Base{Type: EvToolCallStart}, ToolCallID: id, ToolCallName: name})
	if len(args) > 0 {
		if raw, err := json.Marshal(args); err == nil {
			s.writeLocked(ToolCallArgs{Base: Base{Type: EvToolCallArgs}, ToolCallID: id, Delta: string(raw)})
		}
	}
	s.writeLocked(ToolCallEnd{Base: Base{Type: EvToolCallEnd}, ToolCallID: id})
}

// ToolCallFinished emits the TOOL_CALL_RESULT for an executed tool call.
func (s *Sink) ToolCallFinished(id, name, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeLocked(ToolCallResult{
		Base:       Base{Type: EvToolCallResult},
		MessageID:  NewID("tool"),
		ToolCallID: id,
		Content:    result,
		Role:       "tool",
	})
}

// compile-time assertion that Sink satisfies the agent contracts.
var (
	_ types.OutputSink    = (*Sink)(nil)
	_ types.ToolEventSink = (*Sink)(nil)
)
