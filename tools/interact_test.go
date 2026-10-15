package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

func TestHeadlessInteractorDenyIsDefault(t *testing.T) {
	h := NewHeadlessInteractor("whatever")
	if h.Policy != PolicyDeny {
		t.Fatalf("expected deny policy, got %q", h.Policy)
	}
	resp, err := h.Interact(context.Background(), InteractionRequest{
		Kind:       KindApproval,
		AllowIndex: 1,
		DenyIndex:  0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choice != 0 {
		t.Fatalf("expected deny index 0, got %d", resp.Choice)
	}
}

func TestHeadlessInteractorAutoAllows(t *testing.T) {
	h := NewHeadlessInteractor("auto")
	resp, err := h.Interact(context.Background(), InteractionRequest{
		Kind:       KindApproval,
		AllowIndex: 1,
		DenyIndex:  0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choice != 1 {
		t.Fatalf("expected allow index 1, got %d", resp.Choice)
	}
}

func TestHeadlessInteractorTypedConfirm(t *testing.T) {
	auto := NewHeadlessInteractor("auto")
	resp, _ := auto.Interact(context.Background(), InteractionRequest{Kind: KindTypedConfirm, Expect: "RESET"})
	if resp.Text != "RESET" {
		t.Fatalf("auto typed confirm should echo the expected word, got %q", resp.Text)
	}

	deny := NewHeadlessInteractor("deny")
	resp, _ = deny.Interact(context.Background(), InteractionRequest{Kind: KindTypedConfirm, Expect: "RESET"})
	if resp.Text == "RESET" {
		t.Fatalf("deny typed confirm must not provide the word")
	}
}

func TestHeadlessInteractorQuestionErrors(t *testing.T) {
	h := NewHeadlessInteractor("auto")
	_, err := h.Interact(context.Background(), InteractionRequest{Kind: KindQuestion})
	if !errors.Is(err, ErrNoInteractiveInput) {
		t.Fatalf("expected ErrNoInteractiveInput, got %v", err)
	}
}

func TestSetInteractorNilRestoresTUI(t *testing.T) {
	SetInteractor(NewHeadlessInteractor("deny"))
	if _, ok := getInteractor().(*HeadlessInteractor); !ok {
		t.Fatalf("expected headless interactor to be installed")
	}
	SetInteractor(nil)
	if _, ok := getInteractor().(tuiInteractor); !ok {
		t.Fatalf("expected default tui interactor after SetInteractor(nil)")
	}
}

// TestBuildApprovalOptionsInjectsExtras checks that the "See changes" / "See
// details" options are inserted right before the trailing suggestion entry and
// that the suggestion index is shifted accordingly.
func TestBuildApprovalOptionsInjectsExtras(t *testing.T) {
	base := []string{"Allow", "Deny", "Deny with suggestion"}

	opts, sugg, start, extras := buildApprovalOptions(InteractionRequest{Options: base, SuggestionIndex: 2})
	if start != -1 || len(extras) != 0 || sugg != 2 {
		t.Fatalf("no extras: start=%d extras=%d sugg=%d", start, len(extras), sugg)
	}
	if !reflect.DeepEqual(opts, base) {
		t.Fatalf("no extras: opts mutated to %v", opts)
	}

	opts, sugg, start, extras = buildApprovalOptions(InteractionRequest{
		Options:         base,
		SuggestionIndex: 2,
		SeeChangesLabel: "See changes",
		SeeDetailsLabel: "See details",
	})
	want := []string{"Allow", "Deny", "See changes", "See details", "Deny with suggestion"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("opts = %v, want %v", opts, want)
	}
	if start != 2 || len(extras) != 2 {
		t.Fatalf("start=%d len(extras)=%d", start, len(extras))
	}
	if sugg != 4 {
		t.Fatalf("suggestion index = %d, want 4", sugg)
	}
	if extras[0].isDetail || !extras[1].isDetail {
		t.Fatalf("extras kinds wrong: %+v", extras)
	}
}

// TestApprovalChoiceIndexRemovesExtras checks the reverse mapping: an index past
// the injected options is shifted back into the caller's option space.
func TestApprovalChoiceIndexRemovesExtras(t *testing.T) {
	cases := []struct {
		idx, start, count, want int
	}{
		{0, 2, 2, 0},
		{1, 2, 2, 1},
		{4, 2, 2, 2}, // suggestion entry after two injected options
		{3, 2, 1, 2}, // one injected option
		{3, -1, 0, 3},
	}
	for _, tc := range cases {
		if got := approvalChoiceIndex(tc.idx, tc.start, tc.count); got != tc.want {
			t.Errorf("approvalChoiceIndex(%d,%d,%d) = %d, want %d", tc.idx, tc.start, tc.count, got, tc.want)
		}
	}
}

// captureInteractor records the request it is asked to resolve and denies it.
type captureInteractor struct{ req InteractionRequest }

func (c *captureInteractor) Interact(_ context.Context, req InteractionRequest) (InteractionResponse, error) {
	c.req = req
	return InteractionResponse{Choice: req.DenyIndex}, nil
}

// TestExecApprovalKeepsFullCommandInTitle guards the regression where the
// permission title was replaced by the compact preview: the full command must
// stay in Title (the AG-UI wire and the viewers use it) while the inline TUI
// selector renders the compact ShortTitle.
func TestExecApprovalKeepsFullCommandInTitle(t *testing.T) {
	prev := config.Get().NoAsk
	config.SetNoAsk(false)
	t.Cleanup(func() { config.SetNoAsk(prev) })

	spy := &captureInteractor{}
	SetInteractor(spy)
	t.Cleanup(func() { SetInteractor(nil) })

	cmd := "python3 -c \"\nimport sys\nprint(sys.version)\n\""
	askExecPermission(context.Background(), cmd, []types.DetailLine{
		{Kind: types.DetailSection, Text: "Command:"},
		{Kind: types.DetailCommand, Text: "  " + cmd},
	})

	if !strings.Contains(spy.req.Title, cmd) {
		t.Fatalf("full command missing from Title (AG-UI wire): %q", spy.req.Title)
	}
	if strings.Contains(spy.req.ShortTitle, "import sys") {
		t.Fatalf("ShortTitle must be a compact preview, got %q", spy.req.ShortTitle)
	}
	if spy.req.SeeDetailsLabel == "" || len(spy.req.Details) == 0 {
		t.Fatalf("expected a See details payload, got %+v", spy.req)
	}
}
