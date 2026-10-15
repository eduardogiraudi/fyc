// Package main — AG-UI level-1 human-in-the-loop interactor.
//
// It adapts the tool-layer Interactor contract (tools.InteractionRequest) to the
// agui.Broker wire payloads, so the exact same tool code can run in the TUI
// (tools.tuiInteractor), in the deterministic headless policy
// (tools.HeadlessInteractor) or against a live AG-UI client.
//
// Policy:
//
//   - no active run in the context (or interactions disabled) → deterministic
//     headless policy (`auto`/`deny`);
//   - otherwise the request is emitted as a CUSTOM "fyc.interaction_required"
//     event and the tool goroutine parks until the client answers, the
//     interaction times out or the run is canceled;
//   - timeouts and disconnections deny the request, so a run never hangs.
//
// With the level-2 interrupt lifecycle (--interrupts) the same interactor is
// used, but the request never parks: the broker suspends the run and the tool
// sees agui.ErrInterrupted, which the tool loop turns into the interrupt outcome.
package main

import (
	"context"
	"errors"
	"strings"

	"fuckyourcli/agui"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// hitlInteractor implements tools.Interactor on top of the AG-UI broker.
type hitlInteractor struct {
	broker  *agui.Broker
	policy  *tools.HeadlessInteractor
	enabled bool
}

func newHITLInteractor(broker *agui.Broker, policy string, enabled bool) *hitlInteractor {
	return &hitlInteractor{
		broker:  broker,
		policy:  tools.NewHeadlessInteractor(policy),
		enabled: enabled,
	}
}

// Interact implements tools.Interactor.
func (h *hitlInteractor) Interact(ctx context.Context, req tools.InteractionRequest) (tools.InteractionResponse, error) {
	if !h.enabled {
		return h.policy.Interact(ctx, req)
	}

	payload := agui.InteractionRequest{
		Kind:            string(req.Kind),
		Title:           req.Title,
		Options:         req.Options,
		AllowIndex:      req.AllowIndex,
		DenyIndex:       req.DenyIndex,
		SuggestionIndex: req.SuggestionIndex,
		SeeChanges:      req.SeeChangesLabel != "",
		Diff:            req.Diff,
		DiffStats:       req.DiffStats,
		DiffTitle:       req.DiffTitle,
		DiffFilename:    req.DiffFilename,
		SeeDetails:      req.SeeDetailsLabel != "",
		DetailsTitle:    req.DetailsTitle,
		// The details travel as plain strings: the wire payload is unchanged, and
		// the kinds we use for TUI colouring are flattened away (a client that
		// ignores them behaves exactly as before).
		Details:   types.DetailLinesText(req.Details),
		Expect:    req.Expect,
		Questions: payloadQuestions(req.Questions),
	}

	var answer agui.InteractionAnswer
	// The interaction parks the tool goroutine: release the global tool mutex
	// while waiting so other threads are not blocked by one pending approval.
	err := tools.ReleaseExecuteMu(ctx, func() error {
		var reqErr error
		answer, reqErr = h.broker.Request(ctx, payload, rememberKey(req))
		return reqErr
	})

	switch {
	case err == nil:
		return responseFromAnswer(req, answer), nil
	case errors.Is(err, agui.ErrNoRun):
		// Not inside a server run (or the server is shutting down): fall back to
		// the deterministic policy.
		return h.policy.Interact(ctx, req)
	case errors.Is(err, agui.ErrInterrupted):
		// Level 2: the run was suspended for a standard AG-UI interrupt. The tool
		// sees a refusal (which the loop discards) and the run ends with the
		// interrupt outcome; the call is replayed by the resumed run.
		return tools.InteractionResponse{}, err
	default:
		return denyResponse(req, err)
	}
}

// payloadQuestions converts questionary items to their wire representation.
func payloadQuestions(questions []tools.Question) []agui.InteractionQuestion {
	if len(questions) == 0 {
		return nil
	}
	out := make([]agui.InteractionQuestion, 0, len(questions))
	for _, q := range questions {
		out = append(out, agui.InteractionQuestion{
			Question:   q.Question,
			Options:    q.Options,
			AllowInput: q.AllowInput,
		})
	}
	return out
}

// responseFromAnswer maps the client answer back onto the tool contract.
func responseFromAnswer(req tools.InteractionRequest, answer agui.InteractionAnswer) tools.InteractionResponse {
	out := tools.InteractionResponse{
		Text:    answer.Text,
		Answers: answer.Answers,
	}
	if answer.Choice != nil {
		out.Choice = *answer.Choice
	}
	// A "remember" answer (explicit flag or the "Allow always" option) is
	// recorded per-thread by the broker: the tool layer must not persist it in
	// the shared configuration.
	out.Remember = answer.Remember || impliesRemember(req.Options, out.Choice)
	return out
}

// denyResponse deterministically refuses a request whose client answer never
// arrived. The shape mirrors the headless policy so the tool result the model
// sees is the same as in level 0.
func denyResponse(req tools.InteractionRequest, err error) (tools.InteractionResponse, error) {
	switch req.Kind {
	case tools.KindQuestion:
		// The tool reports the error verbatim to the model.
		return tools.InteractionResponse{}, err
	case tools.KindTypedConfirm:
		// An empty text never matches the expected word: the operation aborts.
		return tools.InteractionResponse{}, nil
	default:
		return tools.InteractionResponse{Choice: req.DenyIndex}, nil
	}
}

func rememberKey(req tools.InteractionRequest) string {
	if req.Kind != tools.KindApproval {
		return ""
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || strings.HasPrefix(title, "Remember choice?") {
		return ""
	}
	// File writes and edits target a path: key on the path itself, so that a
	// later update of the same file inherits the decision (the prompt title
	// differs between "Write file: X" and the styled "File: X" of an update).
	if target := rememberTarget(req, title); target != "" {
		return string(req.Kind) + "|file|" + target
	}
	return string(req.Kind) + "|" + title
}

// rememberTarget extracts the file a write/edit approval refers to, if any.
func rememberTarget(req tools.InteractionRequest, title string) string {
	if req.DiffFilename != "" {
		return req.DiffFilename
	}
	const prefix = "Write file: "
	if strings.HasPrefix(title, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(title, prefix))
	}
	return ""
}

// impliesRemember reports whether the selected option means "always": the AG-UI
// clients pick it from the same option list the TUI shows ("Allow always").
func impliesRemember(options []string, choice int) bool {
	return agui.OptionImpliesRemember(options, choice)
}
