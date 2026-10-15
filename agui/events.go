// Package agui implements a minimal AG-UI backend for fuckyourcli.
//
// AG-UI is an event-based protocol (HTTP + Server-Sent Events) that standardises
// how an agent talks to a user-facing application. This package only covers the
// transport and the event encoding; the agent loop itself lives in package main
// (see the global aguiRunner) and is injected through the Runner interface.
//
// The wire format is a hand-written SSE encoder: the repository already streams
// SSE by hand for five providers, so no extra dependency is introduced.
package agui

import "encoding/json"

// AG-UI event type names.
const (
	EvRunStarted  = "RUN_STARTED"
	EvRunFinished = "RUN_FINISHED"
	EvRunError    = "RUN_ERROR"
	EvCustom      = "CUSTOM"

	// state (emitted before a RUN_FINISHED that carries an interrupt)
	EvMessagesSnapshot = "MESSAGES_SNAPSHOT"

	EvTextMessageStart   = "TEXT_MESSAGE_START"
	EvTextMessageContent = "TEXT_MESSAGE_CONTENT"
	EvTextMessageEnd     = "TEXT_MESSAGE_END"

	EvToolCallStart  = "TOOL_CALL_START"
	EvToolCallArgs   = "TOOL_CALL_ARGS"
	EvToolCallEnd    = "TOOL_CALL_END"
	EvToolCallResult = "TOOL_CALL_RESULT"

	EvReasoningStart          = "REASONING_START"
	EvReasoningMessageStart   = "REASONING_MESSAGE_START"
	EvReasoningMessageContent = "REASONING_MESSAGE_CONTENT"
	EvReasoningMessageEnd     = "REASONING_MESSAGE_END"
	EvReasoningEnd            = "REASONING_END"
)

// Base carries the properties shared by every AG-UI event. It is embedded so
// that json.Marshal flattens its fields into the event object.
type Base struct {
	Type          string                 `json:"type"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	SubagentRunID string                 `json:"subagentRunId,omitempty"`
}

// Event is implemented by every serialisable AG-UI event.
type Event interface {
	// event is a marker method that keeps the interface closed.
	event()
}

func (Base) event() {}

// ── lifecycle ────────────────────────────────────────────────────────────────

// RunStarted marks the beginning of a run.
type RunStarted struct {
	Base
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
}

// NewRunStarted builds a RUN_STARTED event.
func NewRunStarted(threadID, runID string) RunStarted {
	return RunStarted{Base: Base{Type: EvRunStarted}, ThreadID: threadID, RunID: runID}
}

// RunFinished marks the successful end of a run.
//
// Outcome is the optional discriminated union introduced by the interrupt-aware
// lifecycle level 2 of the interaction design): {type:"success"} for
// a normal completion or {type:"interrupt", interrupts:[…]} when the run paused
// for human input.
type RunFinished struct {
	Base
	ThreadID string                 `json:"threadId"`
	RunID    string                 `json:"runId"`
	Result   map[string]interface{} `json:"result,omitempty"`
	Outcome  *RunFinishedOutcome    `json:"outcome,omitempty"`
}

// NewRunFinished builds a successful RUN_FINISHED event, attaching the optional
// metadata collected during the run (token usage, provider info, …).
func NewRunFinished(threadID, runID string, metadata map[string]interface{}) RunFinished {
	return RunFinished{
		Base:     Base{Type: EvRunFinished, Metadata: metadata},
		ThreadID: threadID,
		RunID:    runID,
		Outcome:  &RunFinishedOutcome{Type: OutcomeSuccess},
	}
}

// NewRunFinishedInterrupt builds the RUN_FINISHED event that ends a run paused on
// one or more interrupts. The client answers by starting a new run on the same
// thread carrying RunAgentInput.resume.
func NewRunFinishedInterrupt(threadID, runID string, metadata map[string]interface{}, interrupts []Interrupt) RunFinished {
	return RunFinished{
		Base:     Base{Type: EvRunFinished, Metadata: metadata},
		ThreadID: threadID,
		RunID:    runID,
		Outcome:  &RunFinishedOutcome{Type: OutcomeInterrupt, Interrupts: interrupts},
	}
}

// RunError reports a failed run.
type RunError struct {
	Base
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// NewRunError builds a RUN_ERROR event.
func NewRunError(message, code string) RunError {
	return RunError{Base: Base{Type: EvRunError}, Message: message, Code: code}
}

// ── interrupts (level 2 human-in-the-loop) ───────────────────────────────────

// Run outcome discriminator values.
const (
	// OutcomeSuccess marks a run that completed normally.
	OutcomeSuccess = "success"
	// OutcomeInterrupt marks a run paused on one or more interrupts.
	OutcomeInterrupt = "interrupt"
)

// RunFinishedOutcome is the discriminated union carried by RUN_FINISHED.
type RunFinishedOutcome struct {
	Type       string      `json:"type"`
	Interrupts []Interrupt `json:"interrupts,omitempty"`
}

// Interrupt reasons from the AG-UI taxonomy. Any other namespaced string is
// valid; fyc uses the three core values below.
const (
	// ReasonToolCall marks an interrupt bound to a specific tool call (the
	// approval prompts raised by the tools).
	ReasonToolCall = "tool_call"
	// ReasonInputRequired marks an interrupt asking for structured input (the
	// questionary round).
	ReasonInputRequired = "input_required"
	// ReasonConfirmation marks a free-standing yes/no decision (typed
	// confirmations and approvals not bound to a tool call).
	ReasonConfirmation = "confirmation"
)

// Interrupt is one pending human-in-the-loop pause. The fyc-specific payload
// (options, diff, questions, indices, …) travels in Metadata under the "fyc"
// key using exactly the same shape as the level-1 interaction payload.
type Interrupt struct {
	ID             string                 `json:"id"`
	Reason         string                 `json:"reason"`
	Message        string                 `json:"message,omitempty"`
	ToolCallID     string                 `json:"toolCallId,omitempty"`
	ResponseSchema map[string]interface{} `json:"responseSchema,omitempty"`
	ExpiresAt      string                 `json:"expiresAt,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	SubagentRunID  string                 `json:"subagentRunId,omitempty"`
}

// Custom carries an application-specific event (fyc extensions live here).
type Custom struct {
	Base
	Name  string      `json:"name"`
	Value interface{} `json:"value,omitempty"`
}

// NewCustom builds a CUSTOM event.
func NewCustom(name string, value interface{}) Custom {
	return Custom{Base: Base{Type: EvCustom}, Name: name, Value: value}
}

// ── text messages ────────────────────────────────────────────────────────────

// TextMessageStart opens an assistant text message.
type TextMessageStart struct {
	Base
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
}

// TextMessageContent appends a delta to an assistant text message.
type TextMessageContent struct {
	Base
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
}

// TextMessageEnd closes an assistant text message.
type TextMessageEnd struct {
	Base
	MessageID string `json:"messageId"`
}

// ── tool calls ───────────────────────────────────────────────────────────────

// ToolCallStart announces a tool call.
type ToolCallStart struct {
	Base
	ToolCallID   string `json:"toolCallId"`
	ToolCallName string `json:"toolCallName"`
}

// ToolCallArgs streams (chunked) tool-call arguments as a JSON string.
type ToolCallArgs struct {
	Base
	ToolCallID string `json:"toolCallId"`
	Delta      string `json:"delta"`
}

// ToolCallEnd closes the argument stream of a tool call.
type ToolCallEnd struct {
	Base
	ToolCallID string `json:"toolCallId"`
}

// ToolCallResult carries the textual result of a tool call.
type ToolCallResult struct {
	Base
	MessageID  string `json:"messageId"`
	ToolCallID string `json:"toolCallId"`
	Content    string `json:"content"`
	Role       string `json:"role"`
}

// ── reasoning ────────────────────────────────────────────────────────────────

// ReasoningStart opens a reasoning (thinking) block.
//
// Even though the event itself creates no message, the protocol makes
// `messageId` a required string on ReasoningStartEventSchema (and on
// ReasoningEndEventSchema), so the id of the reasoning message the block
// carries must be generated before the block is opened.
type ReasoningStart struct {
	Base
	MessageID string `json:"messageId"`
}

// ReasoningMessageStart opens a reasoning message.
type ReasoningMessageStart struct {
	Base
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
}

// ReasoningMessageContent appends a delta to a reasoning message.
type ReasoningMessageContent struct {
	Base
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
}

// ReasoningMessageEnd closes a reasoning message.
type ReasoningMessageEnd struct {
	Base
	MessageID string `json:"messageId"`
}

// ReasoningEnd closes a reasoning block. Like REASONING_START, the protocol
// requires the messageId of the reasoning message the block carried.
type ReasoningEnd struct {
	Base
	MessageID string `json:"messageId"`
}

// ── state ────────────────────────────────────────────────────────────────────

// Message is one entry of a MESSAGES_SNAPSHOT event.
type Message struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	// Content is always serialised, even when empty: the AG-UI message schemas
	// require `content` on user/system/tool/reasoning messages, so omitting the
	// key makes the whole MESSAGES_SNAPSHOT fail validation on the client.
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolCalls  []InputToolCall `json:"toolCalls,omitempty"`
}

// MessagesSnapshot carries the full conversation state. The protocol requires it
// (before RUN_FINISHED) at an interrupt boundary, so that a client can rebuild
// the context of a resumed run either by replaying messages or by relying on the
// agent's own checkpoint.
type MessagesSnapshot struct {
	Base
	Messages []Message `json:"messages"`
}

// NewMessagesSnapshot builds a MESSAGES_SNAPSHOT event.
func NewMessagesSnapshot(messages []Message) MessagesSnapshot {
	return MessagesSnapshot{Base: Base{Type: EvMessagesSnapshot}, Messages: messages}
}

// encodeEvent marshals an event to its JSON representation.
func encodeEvent(ev Event) ([]byte, error) {
	return json.Marshal(ev)
}
