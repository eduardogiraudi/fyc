package ui

import (
	"fmt"
	"strings"
	"testing"
)

// A finished reasoning trace is one summary row until it is clicked, then the
// whole text — however long — with a collapse control at its end.
func TestReasoningBlockUnfoldsTheWholeTrace(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("thought %02d", i))
	}
	trace := strings.Join(lines, "\n")

	block := NewReasoningBlock("reasoned for 2.1s", trace)
	rows, controls := block.Rows(60)
	if len(rows) != 1 {
		t.Fatalf("the collapsed trace took %d rows, want the summary only", len(rows))
	}
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlExpandResult {
		t.Fatalf("controls = %v, want the expand control", ids)
	}
	if plain := strings.Join(rows, "\n"); strings.Contains(plain, "thought 00") || !strings.Contains(plain, "reasoned for 2.1s") {
		t.Fatalf("collapsed view = %q", plain)
	}

	if !block.Toggle(ControlExpandResult) {
		t.Fatal("expanding the trace changed nothing")
	}
	rows, controls = block.Rows(60)
	plain := strings.Join(rows, "\n")
	if !strings.Contains(plain, "thought 39") {
		t.Fatalf("the tail of the trace was cut:\n%s", plain)
	}
	if ids := controlIDs(controls); len(ids) != 1 || ids[0] != ControlCollapseResult {
		t.Fatalf("controls = %v, want the collapse control", ids)
	}
	if row := rows[controls[0].Row]; !strings.Contains(row, "hide reasoning") {
		t.Fatalf("the collapse control is not on its own row: %q", row)
	}

	block.Toggle(ControlCollapseResult)
	if rows, _ := block.Rows(60); len(rows) != 1 {
		t.Fatalf("the trace stayed open: %d rows", len(rows))
	}
}

// Nothing to unfold means no click to advertise.
func TestReasoningBlockWithoutTextHasNoControl(t *testing.T) {
	if _, controls := NewReasoningBlock("reasoned for 1s", "   \n").Rows(80); len(controls) != 0 {
		t.Fatalf("an empty trace got controls: %v", controls)
	}
}

// The pointer lifts the summary row from grey to white, and only that row.
func TestReasoningBlockHighlightTurnsTheSummaryWhite(t *testing.T) {
	forceTrueColor(t)
	block := NewReasoningBlock("reasoned for 1.2s", "a thought")
	rows, controls := block.Rows(80)
	idle := rows[controls[0].Row]
	if !strings.Contains(idle, "\x1b[38;5;240m") {
		t.Fatalf("the summary row is not grey: %q", idle)
	}
	if !block.SetHover(controls[0].ID) {
		t.Fatal("hovering the summary changed nothing")
	}
	rows, _ = block.Rows(80)
	hovered := rows[controls[0].Row]
	if !strings.Contains(hovered, "38;2;255;255;255") {
		t.Fatalf("the hovered summary is not white: %q", hovered)
	}
	if stripANSI(hovered) != stripANSI(idle) {
		t.Fatalf("the hover changed the text: %q -> %q", stripANSI(idle), stripANSI(hovered))
	}
}
