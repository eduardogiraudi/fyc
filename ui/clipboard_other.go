//go:build !linux

package ui

import "errors"

// errNoX11 keeps the non-Linux build on the native helpers and OSC 52: there is
// no X11 selection to own here (macOS has pbcopy, Windows clip.exe).
var errNoX11 = errors.New("no X11 display")

func x11ClipboardSet(string) error { return errNoX11 }
