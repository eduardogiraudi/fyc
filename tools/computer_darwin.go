//go:build darwin

package tools

import (
	"fmt"
	"image"
	"image/draw"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
)

// macOS backend for computer use: input via CoreGraphics CGEvent (called
// through purego, so no cgo and cross-compilation keeps working) and screen
// capture via the built-in `screencapture` utility.
//
// Runtime requirements (not installs): Screen Recording permission for
// screenshots and Accessibility permission for synthetic input, both granted
// from System Settings > Privacy & Security.

const (
	cgMouseLeftDown     = 1
	cgMouseLeftUp       = 2
	cgMouseRightDown    = 3
	cgMouseRightUp      = 4
	cgMouseMoved        = 5
	cgMouseLeftDragged  = 6
	cgMouseRightDragged = 7
	cgMouseOtherDown    = 25
	cgMouseOtherUp      = 26
	cgMouseOtherDragged = 27

	cgHIDEventTap = 0

	cgScrollEventUnitLine = 1

	cgFlagMaskShift     = 1 << 17
	cgFlagMaskControl   = 1 << 18
	cgFlagMaskAlternate = 1 << 19
	cgFlagMaskCommand   = 1 << 20
)

var (
	cgEventCreateMouseEvent         func(source uintptr, mouseType uint32, x, y float64, mouseButton uint32) uintptr
	cgEventCreateKeyboardEvent      func(source uintptr, virtualKey uint16, keyDown bool) uintptr
	cgEventKeyboardSetUnicodeString func(event uintptr, length uint64, unicodeString uintptr)
	cgEventPost                     func(tap uint32, event uintptr)
	cgEventSetFlags                 func(event uintptr, flags uint64)
	cgEventCreateScrollWheelEvent2  func(source uintptr, units uint32, wheelCount uint32, wheel1, wheel2, wheel3 int32) uintptr
	cgWarpMouseCursorPosition       func(x, y float64) int32
	cgMainDisplayID                 func() uint32
	cgDisplayCopyDisplayMode        func(display uint32) uintptr
	cgDisplayModeGetWidth           func(mode uintptr) uint64
	cgDisplayModeGetHeight          func(mode uintptr) uint64
	cgDisplayModeRelease            func(mode uintptr)

	cfRelease func(cf uintptr)

	axIsProcessTrusted func() bool
)

var cgInitState struct {
	sync.Once
	err error
}

func registerLibFunc(fptr interface{}, handle uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cannot bind %s: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fptr, handle, name)
	return nil
}

func cgInit() error {
	cgInitState.Do(func() {
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_LAZY)
		if err != nil {
			cgInitState.err = fmt.Errorf("cannot load CoreGraphics: %w", err)
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY)
		if err != nil {
			cgInitState.err = fmt.Errorf("cannot load CoreFoundation: %w", err)
			return
		}
		as, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_LAZY)
		if err != nil {
			cgInitState.err = fmt.Errorf("cannot load ApplicationServices: %w", err)
			return
		}

		bindings := []struct {
			fptr interface{}
			lib  uintptr
			name string
		}{
			{&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent"},
			{&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent"},
			{&cgEventKeyboardSetUnicodeString, cg, "CGEventKeyboardSetUnicodeString"},
			{&cgEventPost, cg, "CGEventPost"},
			{&cgEventSetFlags, cg, "CGEventSetFlags"},
			{&cgEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2"},
			{&cgWarpMouseCursorPosition, cg, "CGWarpMouseCursorPosition"},
			{&cgMainDisplayID, cg, "CGMainDisplayID"},
			{&cgDisplayCopyDisplayMode, cg, "CGDisplayCopyDisplayMode"},
			{&cgDisplayModeGetWidth, cg, "CGDisplayModeGetWidth"},
			{&cgDisplayModeGetHeight, cg, "CGDisplayModeGetHeight"},
			{&cgDisplayModeRelease, cg, "CGDisplayModeRelease"},
			{&cfRelease, cf, "CFRelease"},
			{&axIsProcessTrusted, as, "AXIsProcessTrusted"},
		}
		for _, b := range bindings {
			if err := registerLibFunc(b.fptr, b.lib, b.name); err != nil {
				cgInitState.err = err
				return
			}
		}
	})
	return cgInitState.err
}

func darwinEnsureTrusted() error {
	if !axIsProcessTrusted() {
		return fmt.Errorf("Accessibility permission is required to control the mouse/keyboard: enable it for your terminal in System Settings > Privacy & Security > Accessibility")
	}
	return nil
}

// ── screenshot ───────────────────────────────────────────────────────────────

// darwinPoints caches the main display size in points (event coordinate space)
// so the model-space to screen-space mapping is correct on Retina displays.
var darwinPoints struct {
	sync.Mutex
	w, h float64
}

func darwinMainDisplayPoints() (float64, float64) {
	disp := cgMainDisplayID()
	mode := cgDisplayCopyDisplayMode(disp)
	if mode == 0 {
		return 0, 0
	}
	defer cgDisplayModeRelease(mode)
	return float64(cgDisplayModeGetWidth(mode)), float64(cgDisplayModeGetHeight(mode))
}

func computerPlatformCapture() (*image.RGBA, error) {
	if err := cgInit(); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "fyc-screenshot-*.png")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command("screencapture", "-x", "-m", "-t", "png", tmpPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("screencapture failed (%v): %s — check Screen Recording permission in System Settings > Privacy & Security", err, strings.TrimSpace(string(out)))
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open screenshot: %w", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("cannot decode screenshot: %w", err)
	}

	// Refresh the points size used for coordinate mapping.
	pw, ph := darwinMainDisplayPoints()
	if pw > 0 && ph > 0 {
		darwinPoints.Lock()
		darwinPoints.w, darwinPoints.h = pw, ph
		darwinPoints.Unlock()
	}

	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	return rgba, nil
}

// computerPlatformCoordSpace converts captured image pixel dimensions into the
// coordinate space used by input events (points on macOS, pixels elsewhere).
func computerPlatformCoordSpace(imgW, imgH int) (float64, float64) {
	darwinPoints.Lock()
	defer darwinPoints.Unlock()
	if darwinPoints.w > 0 && darwinPoints.h > 0 {
		return darwinPoints.w, darwinPoints.h
	}
	return float64(imgW), float64(imgH)
}

// ── mouse ────────────────────────────────────────────────────────────────────

func darwinPostMouse(mouseType uint32, x, y float64, button uint32) {
	ev := cgEventCreateMouseEvent(0, mouseType, x, y, button)
	if ev == 0 {
		return
	}
	cgEventPost(cgHIDEventTap, ev)
	cfRelease(ev)
}

func darwinWarp(x, y float64) error {
	if r := cgWarpMouseCursorPosition(x, y); r != 0 {
		return fmt.Errorf("CGWarpMouseCursorPosition failed (error %d)", r)
	}
	// Make the move observable to apps listening for mouse-moved events.
	darwinPostMouse(cgMouseMoved, x, y, 0)
	return nil
}

func computerPlatformMove(x, y int) error {
	if err := cgInit(); err != nil {
		return err
	}
	if err := darwinEnsureTrusted(); err != nil {
		return err
	}
	return darwinWarp(float64(x), float64(y))
}

func computerPlatformClick(x, y int, button string, count int) error {
	if err := cgInit(); err != nil {
		return err
	}
	if err := darwinEnsureTrusted(); err != nil {
		return err
	}
	fx, fy := float64(x), float64(y)
	if err := darwinWarp(fx, fy); err != nil {
		return err
	}
	var down, up uint32
	var mouseButton uint32
	switch button {
	case "right":
		down, up, mouseButton = cgMouseRightDown, cgMouseRightUp, 1
	case "middle":
		down, up, mouseButton = cgMouseOtherDown, cgMouseOtherUp, 2
	default:
		down, up, mouseButton = cgMouseLeftDown, cgMouseLeftUp, 0
	}
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(60 * time.Millisecond)
		}
		darwinPostMouse(down, fx, fy, mouseButton)
		darwinPostMouse(up, fx, fy, mouseButton)
	}
	return nil
}

func computerPlatformDrag(x1, y1, x2, y2 int, button string) error {
	if err := cgInit(); err != nil {
		return err
	}
	if err := darwinEnsureTrusted(); err != nil {
		return err
	}
	dragType := uint32(cgMouseLeftDragged)
	mouseButton := uint32(0)
	switch button {
	case "right":
		dragType, mouseButton = cgMouseRightDragged, 1
	case "middle":
		dragType, mouseButton = cgMouseOtherDragged, 2
	}
	if err := darwinWarp(float64(x1), float64(y1)); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	darwinPostMouse(cgMouseLeftDown, float64(x1), float64(y1), mouseButton)
	const steps = 16
	for i := 1; i <= steps; i++ {
		x := x1 + (x2-x1)*i/steps
		y := y1 + (y2-y1)*i/steps
		darwinPostMouse(dragType, float64(x), float64(y), mouseButton)
		time.Sleep(8 * time.Millisecond)
	}
	up := uint32(cgMouseLeftUp)
	if button == "right" {
		up = cgMouseRightUp
	} else if button == "middle" {
		up = cgMouseOtherUp
	}
	darwinPostMouse(up, float64(x2), float64(y2), mouseButton)
	return nil
}

func computerPlatformScroll(x, y int, direction string, amount int) error {
	if err := cgInit(); err != nil {
		return err
	}
	if err := darwinEnsureTrusted(); err != nil {
		return err
	}
	if err := darwinWarp(float64(x), float64(y)); err != nil {
		return err
	}
	var wheel1, wheel2 int32
	switch direction {
	case "up":
		wheel1 = int32(amount)
	case "down":
		wheel1 = -int32(amount)
	case "left":
		wheel2 = -int32(amount)
	case "right":
		wheel2 = int32(amount)
	default:
		return fmt.Errorf("unknown scroll direction %q", direction)
	}
	ev := cgEventCreateScrollWheelEvent2(0, cgScrollEventUnitLine, 2, wheel1, wheel2, 0)
	if ev == 0 {
		return fmt.Errorf("CGEventCreateScrollWheelEvent2 failed")
	}
	cgEventPost(cgHIDEventTap, ev)
	cfRelease(ev)
	return nil
}

// ── keyboard ─────────────────────────────────────────────────────────────────

func darwinKeyEvent(keycode uint16, down bool, flags uint64) {
	ev := cgEventCreateKeyboardEvent(0, keycode, down)
	if ev == 0 {
		return
	}
	if flags != 0 {
		cgEventSetFlags(ev, flags)
	}
	cgEventPost(cgHIDEventTap, ev)
	cfRelease(ev)
}

func computerPlatformTypeText(text string) error {
	if err := cgInit(); err != nil {
		return err
	}
	if err := darwinEnsureTrusted(); err != nil {
		return err
	}
	for _, r := range text {
		units := utf16.Encode([]rune{r})
		down := cgEventCreateKeyboardEvent(0, 0, true)
		up := cgEventCreateKeyboardEvent(0, 0, false)
		if down == 0 || up == 0 {
			return fmt.Errorf("CGEventCreateKeyboardEvent failed")
		}
		cgEventKeyboardSetUnicodeString(down, uint64(len(units)), uintptr(unsafe.Pointer(&units[0])))
		cgEventKeyboardSetUnicodeString(up, uint64(len(units)), uintptr(unsafe.Pointer(&units[0])))
		cgEventPost(cgHIDEventTap, down)
		cgEventPost(cgHIDEventTap, up)
		cfRelease(down)
		cfRelease(up)
		runtime.KeepAlive(units)
		time.Sleep(3 * time.Millisecond)
	}
	return nil
}

// macOS virtual keycodes (ANSI US layout).
var darwinKeycodes = map[string]uint16{
	"a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
	"z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C,
	"w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11, "o": 0x1F,
	"u": 0x20, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26, "k": 0x28,
	"n": 0x2D, "m": 0x2E,
	"1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "5": 0x17,
	"6": 0x16, "7": 0x1A, "8": 0x1C, "9": 0x19, "0": 0x1D,
	"=": 0x18, "-": 0x1B, "]": 0x1E, "[": 0x21, "'": 0x27, ";": 0x29,
	"\\": 0x2A, ",": 0x2B, "/": 0x2C, ".": 0x2F, "`": 0x32,
	"enter": 0x24, "tab": 0x30, "space": 0x31, "backspace": 0x33,
	"esc": 0x35, "delete": 0x75, "home": 0x73, "end": 0x77,
	"pageup": 0x74, "pagedown": 0x79,
	"up": 0x7E, "down": 0x7D, "left": 0x7B, "right": 0x7C,
	"f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60, "f6": 0x61,
	"f7": 0x62, "f8": 0x64, "f9": 0x65, "f10": 0x6D, "f11": 0x67, "f12": 0x6F,
}

var darwinModifiers = map[string]struct {
	keycode uint16
	flag    uint64
}{
	"ctrl":  {0x3B, cgFlagMaskControl},
	"shift": {0x38, cgFlagMaskShift},
	"alt":   {0x3A, cgFlagMaskAlternate},
	"super": {0x37, cgFlagMaskCommand},
}

func darwinKeycodeFor(name string) (uint16, error) {
	name = normalizeKeyName(name)
	if kc, ok := darwinKeycodes[name]; ok {
		return kc, nil
	}
	runes := []rune(name)
	if len(runes) == 1 {
		kc, ok := darwinKeycodes[string(unicode.ToLower(runes[0]))]
		if ok {
			return kc, nil
		}
	}
	return 0, fmt.Errorf("key %q is not supported in key combos on macOS", name)
}

func computerPlatformKey(combo keyCombo) error {
	if err := cgInit(); err != nil {
		return err
	}
	if err := darwinEnsureTrusted(); err != nil {
		return err
	}
	// A combo with no main key ("super", "ctrl+shift") is a bare modifier tap.
	var mainKC uint16
	if combo.Key != "" {
		var err error
		mainKC, err = darwinKeycodeFor(combo.Key)
		if err != nil {
			return err
		}
	}

	type mod struct {
		keycode uint16
		flag    uint64
	}
	var mods []mod
	for _, m := range combo.Mods {
		if mm, ok := darwinModifiers[m]; ok {
			mods = append(mods, mod{mm.keycode, mm.flag})
		}
	}
	if runes := []rune(combo.Key); len(runes) == 1 && unicode.IsLetter(runes[0]) && unicode.IsUpper(runes[0]) {
		if mm, ok := darwinModifiers["shift"]; ok {
			mods = append([]mod{mm}, mods...)
		}
	}

	var flags uint64
	for _, m := range mods {
		flags |= m.flag
		darwinKeyEvent(m.keycode, true, flags)
		time.Sleep(2 * time.Millisecond)
	}
	if combo.Key != "" {
		darwinKeyEvent(mainKC, true, flags)
		darwinKeyEvent(mainKC, false, flags)
	}
	for i := len(mods) - 1; i >= 0; i-- {
		darwinKeyEvent(mods[i].keycode, false, flags)
	}
	return nil
}
