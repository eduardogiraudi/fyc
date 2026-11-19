package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceTrueColor keeps the styles in the rendered text: a test has no TTY and
// lipgloss would otherwise strip every colour.
func forceTrueColor(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
}

// controlRow returns the row offsets of the clickable controls, with their ids.
func controlIDs(controls []BlockControl) []int {
	ids := make([]int, len(controls))
	for i, c := range controls {
		ids[i] = c.ID
	}
	return ids
}

// A truncated tool result keeps the whole payload: the marker row is the
// control that reveals it, and expanding appends a collapse control.
func TestToolOutputExpandsAndCollapses(t *testing.T) {
	out := NewToolOutput("read file a.go", "one\ntwo\nthree\nfour", "")

	rows, controls := out.Rows(80)
	plain := strings.Join(rows, "\n")
	if !strings.Contains(plain, "… 2 more lines hidden · click to expand") {
		t.Fatalf("collapsed marker missing: %q", plain)
	}
	if strings.Contains(plain, "four") {
		t.Fatalf("hidden rows leaked into the collapsed view: %q", plain)
	}
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlExpandResult {
		t.Fatalf("controls = %v, want the expand control", ids)
	}
	if row := rows[controls[0].Row]; !strings.Contains(row, "more lines hidden") {
		t.Fatalf("the control is not on the marker row: %q", row)
	}

	if !out.Toggle(ControlExpandResult) {
		t.Fatal("expanding the result changed nothing")
	}
	rows, controls = out.Rows(80)
	plain = strings.Join(rows, "\n")
	if !strings.Contains(plain, "four") || !strings.Contains(plain, "⋯ collapse 2 lines") {
		t.Fatalf("expanded view is incomplete: %q", plain)
	}
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlCollapseResult {
		t.Fatalf("controls = %v, want the collapse control", ids)
	}

	if !out.Toggle(ControlCollapseResult) {
		t.Fatal("collapsing the result changed nothing")
	}
	rows, _ = out.Rows(80)
	if plain := strings.Join(rows, "\n"); strings.Contains(plain, "four") {
		t.Fatalf("the result stayed expanded: %q", plain)
	}
}

// A tool result that fits needs no marker, and the plain (non-interactive)
// rendering never advertises a click.
func TestToolOutputWithoutHiddenRowsHasNoControl(t *testing.T) {
	if _, controls := NewToolOutput("ran 'true'", "ok", "").Rows(80); len(controls) != 0 {
		t.Fatalf("a complete result got controls: %v", controls)
	}
	plain := FormatToolOutput("read file a.go", "one\ntwo\nthree\nfour", 80, ToolOutputRows)
	if strings.Contains(plain, "click to expand") {
		t.Fatalf("the fallback renderer advertised a click it cannot honour: %q", plain)
	}
}

// The changed lines of an anchored edit are a control of their own: the summary
// stays one row, and the diff is revealed on demand.
func TestToolOutputShowsChangedLinesOnDemand(t *testing.T) {
	detail := " 1 first\n-2 second\n+2 TWO\n 3 third"
	out := NewToolOutput("patched file a.go", "updated (+1 -1 lines)\nnew anchors:\nk7fwd§two", detail)

	rows, controls := out.Rows(80)
	plain := strings.Join(rows, "\n")
	if !strings.Contains(plain, "⋯ show changed lines") {
		t.Fatalf("missing the changed-lines control: %q", plain)
	}
	if strings.Contains(plain, "+TWO") {
		t.Fatalf("the diff was drawn before the click: %q", plain)
	}
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlOpenDetail {
		t.Fatalf("controls = %v, want the detail control", ids)
	}

	out.Toggle(ControlOpenDetail)
	rows, controls = out.Rows(80)
	plain = strings.Join(rows, "\n")
	for _, want := range []string{
		DeleteStyle.Render("-2 second"),
		AddStyle.Render("+2 TWO"),
		" 3 third",
		"⋯ hide changed lines",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("open detail is missing %q in:\n%q", want, plain)
		}
	}
	// The counts belong to the collapsed view: an open diff replaces them.
	if strings.Contains(plain, "+1 -1 lines") {
		t.Fatalf("the collapsed counts survived the expanded diff:\n%q", plain)
	}
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlCloseDetail {
		t.Fatalf("controls = %v, want the close control", ids)
	}

	out.Toggle(ControlCloseDetail)
	rows, _ = out.Rows(80)
	if plain := strings.Join(rows, "\n"); strings.Contains(plain, "+TWO") || !strings.Contains(plain, "+1 -1 lines") {
		t.Fatalf("the collapsed view did not come back: %q", plain)
	}
}

// A truncated result with a detail has two independent controls while it is
// collapsed. The diff replaces the result summary when it is open: the counts
// and the hidden-rows marker belong to the collapsed view.
func TestToolOutputKeepsBothControlsApart(t *testing.T) {
	out := NewToolOutput("read file a.go", "one\ntwo\nthree\nfour", " ctx\n-old\n+new")
	rows, controls := out.Rows(80)
	if ids := controlIDs(controls); len(ids) != 2 || ids[0] != ControlExpandResult || ids[1] != ControlOpenDetail {
		t.Fatalf("controls = %v, want expand then detail", ids)
	}
	if controls[0].Row == controls[1].Row {
		t.Fatalf("both controls landed on row %d", controls[0].Row)
	}
	if !strings.Contains(rows[controls[0].Row], "more lines hidden") || !strings.Contains(rows[controls[1].Row], "show changed lines") {
		t.Fatalf("controls are not on their own rows:\n%s", strings.Join(rows, "\n"))
	}

	out.Toggle(ControlOpenDetail)
	rows, controls = out.Rows(80)
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlCloseDetail {
		t.Fatalf("controls = %v, want only the close control", ids)
	}
	plain := strings.Join(rows, "\n")
	if !strings.Contains(plain, "+new") || strings.Contains(plain, "one") {
		t.Fatalf("the diff is incomplete:\n%s", plain)
	}
	if strings.Contains(plain, "more lines hidden") {
		t.Fatalf("the hidden-rows marker survived the expanded diff:\n%s", plain)
	}
}

// The pointer lifts a control row from grey to white on the hover background,
// and only that row.
func TestToolOutputHighlightTurnsTheControlWhite(t *testing.T) {
	forceTrueColor(t)
	out := NewToolOutput("read file a.go", "one\ntwo\nthree\nfour", "")
	rows, controls := out.Rows(80)
	idle := rows[controls[0].Row]
	if !strings.Contains(idle, "\x1b[38;5;240m") {
		t.Fatalf("the control row is not grey: %q", idle)
	}

	if !out.SetHover(controls[0].ID) {
		t.Fatal("hovering the control changed nothing")
	}
	rows, _ = out.Rows(80)
	hovered := rows[controls[0].Row]
	if !strings.Contains(hovered, "38;2;255;255;255") {
		t.Fatalf("the hovered control is not white: %q", hovered)
	}
	if stripANSI(hovered) != stripANSI(idle) {
		t.Fatalf("the hover changed the text: %q -> %q", stripANSI(idle), stripANSI(hovered))
	}
	for i, row := range rows {
		if i != controls[0].Row && strings.Contains(row, "38;2;255;255;255") {
			t.Fatalf("the highlight leaked onto row %d: %q", i, row)
		}
	}

	if !out.SetHover(-1) {
		t.Fatal("clearing the hover changed nothing")
	}
	if rows, _ = out.Rows(80); rows[controls[0].Row] != idle {
		t.Fatalf("the control kept its highlight: %q", rows[controls[0].Row])
	}
	if out.SetHover(-1) {
		t.Fatal("clearing an absent hover must report no change")
	}
}
