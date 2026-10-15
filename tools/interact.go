// Package tools — interaction abstraction.
//
// Every interactive prompt issued by a tool (approval, typed confirmation,
// questionary) goes through a single Interactor so the exact same tool code can
// run both in the interactive TUI and in the headless AG-UI server.
//
// The default interactor reproduces the historical TUI behaviour byte for byte;
// the AG-UI server installs a deterministic headless policy (see
// NewHeadlessInteractor) so a non-TTY process never blocks on stdin.
package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"fuckyourcli/types"
	"fuckyourcli/ui"
)

// InteractionKind discriminates the three families of interactive requests.
type InteractionKind string

const (
	// KindApproval is a yes/no style verdict, optionally with "remember me"
	// options and/or a diff payload shown in the viewer.
	KindApproval InteractionKind = "approval"
	// KindTypedConfirm requires the user to type an exact word.
	KindTypedConfirm InteractionKind = "typed_confirm"
	// KindQuestion is a questionary round: the answers are returned to the
	// model as the tool result.
	KindQuestion InteractionKind = "question"
)

// Question is a single questionary item.
type Question struct {
	Question   string
	Options    []string
	AllowInput bool
}

// InteractionRequest describes one interactive request.
//
// For KindApproval, Options is the complete list of options as rendered to the
// user (including the trailing "Deny with suggestion" entry when present).
// SuggestionIndex points at that trailing entry (-1 when absent). SeeChangesLabel
// and SeeDetailsLabel, when non-empty, add extra options (inserted right before
// the suggestion entry) that open the diff viewer or the details viewer and
// then re-show the prompt; those options never appear in the returned Choice.
// Details carries the viewer payload as typed lines (types.DetailLine): the
// kind is what lets the TUI colour the envelope and shell-highlight the command
// section, while the AG-UI adapter flattens it back to plain strings.
// ShortTitle, when non-empty, is what the inline TUI selector renders instead of
// Title: it is a compact preview for a Title that would overflow the terminal
// (a multi-line command). Title itself stays authoritative — it is what the
// viewers and the AG-UI wire carry.
type InteractionRequest struct {
	Kind            InteractionKind
	Title           string
	ShortTitle      string
	StyledTitle     bool
	Options         []string
	AllowIndex      int
	DenyIndex       int
	SuggestionIndex int
	SeeChangesLabel string
	Diff            []types.DiffLine
	DiffStats       string
	DiffTitle       string
	DiffFilename    string
	SeeDetailsLabel string
	DetailsTitle    string
	Details         []types.DetailLine
	Expect          string
	Questions       []Question
}

// InteractionResponse is the outcome of an InteractionRequest.
type InteractionResponse struct {
	Choice   int      // approval: selected option index
	Text     string   // typed_confirm / free-text input
	Answers  []string // question: one answer per question
	Remember bool     // reserved for per-thread "allow always" (phase 2)
}

// Interactor resolves interactive requests.
type Interactor interface {
	Interact(ctx context.Context, req InteractionRequest) (InteractionResponse, error)
}

var (
	interactorMu sync.RWMutex
	interactor   Interactor = tuiInteractor{}
)

// SetInteractor installs the process-wide interactor. Passing nil restores the
// default TUI behaviour.
func SetInteractor(i Interactor) {
	interactorMu.Lock()
	defer interactorMu.Unlock()
	if i == nil {
		interactor = tuiInteractor{}
		return
	}
	interactor = i
}

// getInteractor returns the active interactor.
func getInteractor() Interactor {
	interactorMu.RLock()
	defer interactorMu.RUnlock()
	return interactor
}

// interact runs a request through the active interactor.
func interact(ctx context.Context, req InteractionRequest) (InteractionResponse, error) {
	return getInteractor().Interact(ctx, req)
}

// ── default TUI interactor ───────────────────────────────────────────────────

type tuiInteractor struct{}

func (tuiInteractor) Interact(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
	switch req.Kind {
	case KindTypedConfirm:
		return tuiTypedConfirm(req), nil
	case KindQuestion:
		return tuiQuestions(req)
	default:
		return tuiApproval(req), nil
	}
}

func tuiTypedConfirm(req InteractionRequest) InteractionResponse {
	return InteractionResponse{
		Text: ui.InputText(fmt.Sprintf("%s\nType %q to continue (anything else aborts):", req.Title, req.Expect)),
	}
}

// approvalExtra is an interactive "assistant" option injected before the
// trailing "Deny with suggestion" entry: it opens a viewer and then re-shows the
// prompt. It never becomes the returned Choice.
type approvalExtra struct {
	label    string
	isDetail bool // true = text details, false = diff
}

// buildApprovalOptions inserts the interactive "assistant" options ("See
// changes" / "See details") right before the trailing suggestion entry. It
// returns the rendered option list, the (possibly shifted) suggestion index,
// the index of the first injected option (-1 when none) and the injected
// options themselves.
func buildApprovalOptions(req InteractionRequest) (opts []string, suggIdx, extraStart int, extras []approvalExtra) {
	opts = append([]string(nil), req.Options...)
	suggIdx = req.SuggestionIndex
	if req.SeeChangesLabel != "" {
		extras = append(extras, approvalExtra{label: req.SeeChangesLabel})
	}
	if req.SeeDetailsLabel != "" {
		extras = append(extras, approvalExtra{label: req.SeeDetailsLabel, isDetail: true})
	}
	extraStart = -1
	if len(extras) == 0 {
		return opts, suggIdx, extraStart, extras
	}
	extraStart = len(opts) - 1
	if extraStart < 0 {
		extraStart = 0
	}
	labels := make([]string, len(extras))
	for i, e := range extras {
		labels[i] = e.label
	}
	merged := make([]string, 0, len(opts)+len(labels))
	merged = append(merged, opts[:extraStart]...)
	merged = append(merged, labels...)
	merged = append(merged, opts[extraStart:]...)
	opts = merged
	if suggIdx >= extraStart {
		suggIdx += len(extras)
	}
	return opts, suggIdx, extraStart, extras
}

// approvalChoiceIndex maps a rendered option index back to the caller's option
// space, undoing the inserted "assistant" options. Indices inside the injected
// range never reach here: the caller handles them as viewer activations.
func approvalChoiceIndex(idx, extraStart, extraCount int) int {
	if extraStart >= 0 && idx >= extraStart+extraCount {
		return idx - extraCount
	}
	return idx
}

func tuiApproval(req InteractionRequest) InteractionResponse {
	opts, suggIdx, extraStart, extras := buildApprovalOptions(req)

	stdinMu.Lock()
	defer stdinMu.Unlock()

	for {
		var idx int
		var suggestion string
		// The inline selector renders the compact title when one is provided: a
		// tall Title (a multi-line command) would overflow the terminal.
		selectorTitle := req.Title
		if req.ShortTitle != "" {
			selectorTitle = req.ShortTitle
		}
		if req.StyledTitle {
			idx, suggestion = ui.SelectOptionWithStyledTitleSuggestion(selectorTitle, opts, suggIdx)
		} else {
			idx, suggestion = ui.SelectOptionWithSuggestion(selectorTitle, opts, suggIdx)
		}
		if idx < 0 {
			// Dismissed (q / Ctrl+C / Esc) instead of answering: fail closed —
			// deny. Never fall through to the first option (which is "Allow").
			return InteractionResponse{Choice: req.DenyIndex}
		}
		if extraStart >= 0 && idx >= extraStart && idx < extraStart+len(extras) {
			extra := extras[idx-extraStart]
			title := req.DiffTitle
			if title == "" {
				title = req.Title
			}
			if extra.isDetail {
				detailTitle := req.DetailsTitle
				if detailTitle == "" {
					detailTitle = title
				}
				ui.RunTextViewer(detailTitle, "", req.Details)
			} else {
				ui.RunDiffViewer(title, req.DiffStats, req.DiffFilename, req.Diff)
			}
			continue
		}
		return InteractionResponse{Choice: approvalChoiceIndex(idx, extraStart, len(extras)), Text: suggestion}
	}
}

func tuiQuestions(req InteractionRequest) (InteractionResponse, error) {
	answers := make([]string, 0, len(req.Questions))
	for _, q := range req.Questions {
		switch {
		case len(q.Options) > 0 && q.AllowInput:
			optionsWithCustom := append(append([]string{}, q.Options...), "[type custom...]")
			idx := ui.SelectOption(q.Question, optionsWithCustom)
			switch {
			case idx < 0:
				return InteractionResponse{}, ErrInteractionCanceled
			case idx == len(q.Options):
				answers = append(answers, ui.InputText(q.Question+" (custom)"))
			default:
				answers = append(answers, q.Options[idx])
			}
		case len(q.Options) > 0:
			idx := ui.SelectOption(q.Question, q.Options)
			if idx < 0 {
				return InteractionResponse{}, ErrInteractionCanceled
			}
			answers = append(answers, q.Options[idx])
		default:
			answers = append(answers, ui.InputText(q.Question))
		}
	}
	return InteractionResponse{Answers: answers}, nil
}

// ── headless interactor (AG-UI server) ───────────────────────────────────────

// HeadlessPolicy is the permission policy applied when no interactive TTY is
// available.
type HeadlessPolicy string

const (
	// PolicyNone auto-approves every approval/typed confirmation.
	PolicyNone HeadlessPolicy = "auto"
	// PolicyDeny denies every approval/typed confirmation (default).
	PolicyDeny HeadlessPolicy = "deny"
)

// ErrNoInteractiveInput is returned when a headless run is asked to collect
// free-form user input (questionary), which is not supported over the AG-UI
// MVP: the tool is also removed from the tool list exposed to the model.
var ErrNoInteractiveInput = errors.New("interactive input not available in server mode")

// ErrInteractionCanceled is returned when the user dismisses an interactive
// prompt (q / Ctrl+C / Esc) instead of answering it.
var ErrInteractionCanceled = errors.New("interaction cancelled by the user")

// HeadlessInteractor is a deterministic, non-blocking interactor.
type HeadlessInteractor struct {
	Policy HeadlessPolicy
}

// NewHeadlessInteractor builds a headless interactor from a policy string
// ("auto" or "deny"; anything else falls back to "deny").
func NewHeadlessInteractor(policy string) *HeadlessInteractor {
	p := HeadlessPolicy(strings.TrimSpace(strings.ToLower(policy)))
	if p != PolicyNone {
		p = PolicyDeny
	}
	return &HeadlessInteractor{Policy: p}
}

// Interact implements Interactor without ever touching stdin/stdout.
func (h *HeadlessInteractor) Interact(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
	switch req.Kind {
	case KindTypedConfirm:
		if h.Policy == PolicyNone {
			return InteractionResponse{Text: req.Expect}, nil
		}
		return InteractionResponse{}, nil
	case KindQuestion:
		return InteractionResponse{}, ErrNoInteractiveInput
	default: // KindApproval
		if h.Policy == PolicyNone {
			return InteractionResponse{Choice: req.AllowIndex}, nil
		}
		return InteractionResponse{Choice: req.DenyIndex}, nil
	}
}
