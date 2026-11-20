package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pickerKey(m numberPickerModel, key tea.KeyType) numberPickerModel {
	updated, _ := m.Update(tea.KeyMsg{Type: key})
	return updated.(numberPickerModel)
}

func TestNumberPickerArrowsAndFloor(t *testing.T) {
	m := numberPickerModel{value: 250, step: 10, min: 1, unit: "iterations"}
	if view := m.View(); !strings.Contains(view, "250") || !strings.Contains(view, "◀") || !strings.Contains(view, "▶") {
		t.Fatalf("picker view = %q", view)
	}
	if m = pickerKey(m, tea.KeyRight); m.value != 260 {
		t.Fatalf("right arrow = %d, want 260", m.value)
	}
	if m = pickerKey(m, tea.KeyPgDown); m.value != 160 {
		t.Fatalf("page down = %d, want 160", m.value)
	}
	for range 40 {
		m = pickerKey(m, tea.KeyLeft)
	}
	if m.value != 1 {
		t.Fatalf("floor = %d, want 1", m.value)
	}
	// No ceiling: a hard task may need a budget far above any default.
	m.value = 1000000
	if m = pickerKey(m, tea.KeyRight); m.value != 1000010 {
		t.Fatalf("value must not be capped, got %d", m.value)
	}
}

func TestNumberPickerConfirmAndCancel(t *testing.T) {
	m := pickerKey(numberPickerModel{value: 25, step: 5, min: 1, unit: "minutes"}, tea.KeyEnter)
	if !m.ok || !m.done || m.View() != "" {
		t.Fatal("Enter must confirm")
	}
	m = pickerKey(numberPickerModel{value: 25, step: 5, min: 1, unit: "minutes"}, tea.KeyEsc)
	if m.ok || !m.done || m.View() != "" {
		t.Fatal("Escape must cancel")
	}
}

func TestMouseNumberPickerArrowsAndConfirm(t *testing.T) {
	m := WithMouseButtons(numberPickerModel{title: "Limits", value: 250, step: 10, min: 1, unit: "iterations"})
	x, y := buttonCell(t, drawButtons(m), "▶")
	next, _ := clickButtons(m, x, y)
	if got := WithoutMouseButtons(next).(numberPickerModel).value; got != 260 {
		t.Fatalf("click on ▶ = %d, want 260", got)
	}
	m = next
	x, y = buttonCell(t, drawButtons(m), "[Confirm]")
	next, cmd := clickButtons(m, x, y)
	nm := WithoutMouseButtons(next).(numberPickerModel)
	if cmd == nil || !nm.ok {
		t.Fatal("Confirm button must submit the value")
	}
}
