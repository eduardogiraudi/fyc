//go:build linux

package ui

import (
	"errors"
	"testing"
)

// Without a display there is no selection to own, and the caller has to keep
// looking: the native helpers and OSC 52 are the only options left.
func TestX11ClipboardNeedsADisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	if err := x11ClipboardSet("text"); !errors.Is(err, errNoX11) {
		t.Fatalf("x11ClipboardSet without DISPLAY = %v, want errNoX11", err)
	}
}

// VTE implements no OSC 52 at all, so the status line must not claim a copy
// there. Other terminals get the unconfirmed wording instead.
func TestOSC52TerminalSupportDetectsVTE(t *testing.T) {
	t.Setenv("VTE_VERSION", "7600")
	if OSC52TerminalSupport() {
		t.Fatal("VTE must be reported as dropping OSC 52")
	}
	t.Setenv("VTE_VERSION", "")
	if !OSC52TerminalSupport() {
		t.Fatal("a terminal without VTE must stay a maybe")
	}
}
