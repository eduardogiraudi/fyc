//go:build linux

package tools

import (
	"fmt"
	"image"
	"os"
	"sync"
	"time"
	"unicode"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Linux/X11 backend for computer use, implemented in pure Go on top of the
// XGB protocol bindings (no cgo, no xdotool). XTEST is used for synthetic
// input, GetImage for screen capture.
//
// On Wayland sessions (WAYLAND_DISPLAY set) the functions below route to the
// XDG-portal backend in computer_wayland.go: rootless Xwayland refuses X11
// screen capture and XTEST input only reaches X11 clients, so the portal
// backend is used instead.

var x11State struct {
	sync.Mutex
	conn *xgb.Conn
}

func x11Conn() (*xgb.Conn, error) {
	x11State.Lock()
	defer x11State.Unlock()
	if x11State.conn != nil {
		return x11State.conn, nil
	}
	if os.Getenv("DISPLAY") == "" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return nil, fmt.Errorf("computer use requires X11 on Linux: Wayland session detected (no DISPLAY)")
		}
		return nil, fmt.Errorf("computer use requires X11 on Linux: DISPLAY is not set")
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the X server: %w", err)
	}
	if err := xtest.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("XTEST extension not available on this X server: %w", err)
	}
	x11State.conn = conn
	return conn, nil
}

func x11Root(c *xgb.Conn) xproto.Window {
	setup := xproto.Setup(c)
	return setup.DefaultScreen(c).Root
}

// ── screenshot ───────────────────────────────────────────────────────────────

func computerPlatformCapture() (*image.RGBA, error) {
	if waylandSessionActive() {
		return waylandCapture()
	}
	c, err := x11Conn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(c)
	screen := setup.DefaultScreen(c)
	w := int(screen.WidthInPixels)
	h := int(screen.HeightInPixels)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("X server reported an empty screen")
	}

	bpp := 0
	for _, f := range setup.PixmapFormats {
		if f.Depth == screen.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if bpp != 24 && bpp != 32 {
		return nil, fmt.Errorf("unsupported root pixel format (%d bpp)", bpp)
	}
	bytesPerPixel := bpp / 8
	rowStride := (w*bytesPerPixel + 3) &^ 3

	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// Capture in horizontal strips to keep every GetImage reply small.
	stripRows := (8 * 1024 * 1024) / maxInt(1, w*bytesPerPixel)
	if stripRows < 16 {
		stripRows = 16
	}
	if stripRows > 1024 {
		stripRows = 1024
	}

	lsb := setup.ImageByteOrder == xproto.ImageOrderLSBFirst
	for startY := 0; startY < h; startY += stripRows {
		rows := stripRows
		if startY+rows > h {
			rows = h - startY
		}
		reply, err := xproto.GetImage(c, xproto.ImageFormatZPixmap, xproto.Drawable(screen.Root), 0, int16(startY), uint16(w), uint16(rows), 0xffffffff).Reply()
		if err != nil {
			hint := ""
			if os.Getenv("WAYLAND_DISPLAY") != "" {
				hint = " — this looks like a Wayland session with rootless Xwayland, where X11 screen capture is refused by design"
			}
			return nil, fmt.Errorf("screen capture via X11 GetImage failed (%w)%s", err, hint)
		}
		data := reply.Data
		for row := 0; row < rows; row++ {
			ro := row * rowStride
			if ro+rowStride > len(data) {
				break
			}
			dst := img.Pix[(startY+row)*img.Stride:]
			for x := 0; x < w; x++ {
				px := ro + x*bytesPerPixel
				o := x * 4
				if lsb {
					// little endian BGR(X)
					dst[o] = data[px+2]
					dst[o+1] = data[px+1]
					dst[o+2] = data[px]
				} else {
					// big endian (X)RGB
					dst[o] = data[px+1]
					dst[o+1] = data[px+2]
					dst[o+2] = data[px+3]
				}
				dst[o+3] = 255
			}
		}
	}
	return img, nil
}

// ── mouse ────────────────────────────────────────────────────────────────────

func fakeInput(c *xgb.Conn, typ, detail byte, root xproto.Window, x, y int16) error {
	return xtest.FakeInputChecked(c, typ, detail, 0, root, x, y, 0).Check()
}

func x11ButtonNumber(button string) byte {
	switch button {
	case "middle":
		return 2
	case "right":
		return 3
	default:
		return 1
	}
}

func computerPlatformMove(x, y int) error {
	if waylandSessionActive() {
		return waylandMove(x, y)
	}
	c, err := x11Conn()
	if err != nil {
		return err
	}
	return fakeInput(c, xproto.MotionNotify, 0, x11Root(c), int16(x), int16(y))
}

func computerPlatformClick(x, y int, button string, count int) error {
	if waylandSessionActive() {
		return waylandClick(x, y, button, count)
	}
	c, err := x11Conn()
	if err != nil {
		return err
	}
	if err := computerPlatformMove(x, y); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	num := x11ButtonNumber(button)
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(60 * time.Millisecond) // allow double-click detection
		}
		if err := fakeInput(c, xproto.ButtonPress, num, 0, 0, 0); err != nil {
			return err
		}
		if err := fakeInput(c, xproto.ButtonRelease, num, 0, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

func computerPlatformDrag(x1, y1, x2, y2 int, button string) error {
	if waylandSessionActive() {
		return waylandDrag(x1, y1, x2, y2, button)
	}
	c, err := x11Conn()
	if err != nil {
		return err
	}
	if err := computerPlatformMove(x1, y1); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	num := x11ButtonNumber(button)
	if err := fakeInput(c, xproto.ButtonPress, num, 0, 0, 0); err != nil {
		return err
	}
	const steps = 16
	for i := 1; i <= steps; i++ {
		x := x1 + (x2-x1)*i/steps
		y := y1 + (y2-y1)*i/steps
		if err := fakeInput(c, xproto.MotionNotify, 0, x11Root(c), int16(x), int16(y)); err != nil {
			return err
		}
		time.Sleep(8 * time.Millisecond)
	}
	return fakeInput(c, xproto.ButtonRelease, num, 0, 0, 0)
}

func computerPlatformScroll(x, y int, direction string, amount int) error {
	if waylandSessionActive() {
		return waylandScroll(x, y, direction, amount)
	}
	c, err := x11Conn()
	if err != nil {
		return err
	}
	if err := computerPlatformMove(x, y); err != nil {
		return err
	}
	var btn byte
	switch direction {
	case "up":
		btn = 4
	case "down":
		btn = 5
	case "left":
		btn = 6
	case "right":
		btn = 7
	default:
		return fmt.Errorf("unknown scroll direction %q", direction)
	}
	for i := 0; i < amount; i++ {
		if err := fakeInput(c, xproto.ButtonPress, btn, 0, 0, 0); err != nil {
			return err
		}
		if err := fakeInput(c, xproto.ButtonRelease, btn, 0, 0, 0); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// ── keyboard ─────────────────────────────────────────────────────────────────

var keymapCache struct {
	sync.Mutex
	conn         *xgb.Conn
	minKC, maxKC xproto.Keycode
	per          int
	syms         []xproto.Keysym
}

func loadKeymap(c *xgb.Conn) error {
	keymapCache.Lock()
	defer keymapCache.Unlock()
	if keymapCache.conn == c && keymapCache.syms != nil {
		return nil
	}
	setup := xproto.Setup(c)
	minKC, maxKC := setup.MinKeycode, setup.MaxKeycode
	reply, err := xproto.GetKeyboardMapping(c, minKC, byte(maxKC-minKC+1)).Reply()
	if err != nil {
		return fmt.Errorf("GetKeyboardMapping failed: %w", err)
	}
	keymapCache.conn = c
	keymapCache.minKC = minKC
	keymapCache.maxKC = maxKC
	keymapCache.per = int(reply.KeysymsPerKeycode)
	keymapCache.syms = reply.Keysyms
	return nil
}

// lookupKeysymLocked finds the keycode/column producing the given keysym.
// Caller must NOT hold the cache lock (it loads the keymap itself).
func lookupKeysym(c *xgb.Conn, ks xproto.Keysym) (xproto.Keycode, int, bool) {
	if ks == 0 {
		return 0, 0, false
	}
	if err := loadKeymap(c); err != nil {
		return 0, 0, false
	}
	keymapCache.Lock()
	defer keymapCache.Unlock()
	per := keymapCache.per
	if per <= 0 {
		return 0, 0, false
	}
	n := int(keymapCache.maxKC-keymapCache.minKC) + 1
	for i := 0; i < n; i++ {
		for col := 0; col < per; col++ {
			idx := i*per + col
			if idx < len(keymapCache.syms) && keymapCache.syms[idx] == ks {
				return keymapCache.minKC + xproto.Keycode(i), col, true
			}
		}
	}
	return 0, 0, false
}

func tapKeycode(c *xgb.Conn, kc xproto.Keycode) error {
	if err := fakeInput(c, xproto.KeyPress, byte(kc), 0, 0, 0); err != nil {
		return err
	}
	time.Sleep(time.Millisecond)
	return fakeInput(c, xproto.KeyRelease, byte(kc), 0, 0, 0)
}

func tapKeysym(c *xgb.Conn, ks xproto.Keysym) error {
	kc, col, ok := lookupKeysym(c, ks)
	if !ok {
		return errKeysymNotFound(ks)
	}
	if col == 1 {
		// shifted level on the standard layout
		shiftKC, _, ok := lookupKeysym(c, 0xffe1) // Shift_L
		if !ok {
			return fmt.Errorf("Shift key not found in the current layout")
		}
		if err := fakeInput(c, xproto.KeyPress, byte(shiftKC), 0, 0, 0); err != nil {
			return err
		}
		err := tapKeycode(c, kc)
		_ = fakeInput(c, xproto.KeyRelease, byte(shiftKC), 0, 0, 0)
		return err
	}
	return tapKeycode(c, kc)
}

func errKeysymNotFound(ks xproto.Keysym) error {
	return fmt.Errorf("keysym 0x%x not found in the current keyboard layout", uint32(ks))
}

func runeToKeysym(r rune) xproto.Keysym {
	if r >= 0x20 && r <= 0xff {
		return xproto.Keysym(r)
	}
	return xproto.Keysym(0x01000000) | xproto.Keysym(r)
}

func computerPlatformTypeText(text string) error {
	if waylandSessionActive() {
		return waylandTypeText(text)
	}
	c, err := x11Conn()
	if err != nil {
		return err
	}
	for _, r := range text {
		var err error
		switch r {
		case '\n':
			err = tapKeysym(c, 0xff0d) // Return
		case '\t':
			err = tapKeysym(c, 0xff09) // Tab
		default:
			err = typeRuneX11(c, r)
		}
		if err != nil {
			return err
		}
		time.Sleep(4 * time.Millisecond)
	}
	return nil
}

func typeRuneX11(c *xgb.Conn, r rune) error {
	ks := runeToKeysym(r)
	if _, _, ok := lookupKeysym(c, ks); ok {
		return tapKeysym(c, ks)
	}
	// Not present in the layout: temporarily remap a spare keycode
	// (the technique xdotool uses for arbitrary unicode input).
	return typeViaRemap(c, ks)
}

func findSpareKeycodeLocked() (xproto.Keycode, bool) {
	per := keymapCache.per
	n := int(keymapCache.maxKC-keymapCache.minKC) + 1
	for i := 0; i < n; i++ {
		allZero := true
		for col := 0; col < per; col++ {
			idx := i*per + col
			if idx < len(keymapCache.syms) && keymapCache.syms[idx] != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return keymapCache.minKC + xproto.Keycode(i), true
		}
	}
	return 0, false
}

func typeViaRemap(c *xgb.Conn, ks xproto.Keysym) error {
	if err := loadKeymap(c); err != nil {
		return err
	}
	keymapCache.Lock()
	per := keymapCache.per
	spare, ok := findSpareKeycodeLocked()
	keymapCache.Unlock()
	if !ok || per <= 0 {
		return fmt.Errorf("cannot type character %q: no spare keycode to remap", string(rune(uint32(ks)&0xffffff)))
	}

	keysyms := make([]xproto.Keysym, per)
	keysyms[0] = ks
	xproto.ChangeKeyboardMapping(c, 1, spare, byte(per), keysyms)
	c.Sync()
	time.Sleep(6 * time.Millisecond)

	err := tapKeycode(c, spare)

	zeros := make([]xproto.Keysym, per)
	xproto.ChangeKeyboardMapping(c, 1, spare, byte(per), zeros)
	c.Sync()
	// The mapping cache is still valid: the spare keycode was all-NoSymbol.
	return err
}

var namedKeysyms = map[string]xproto.Keysym{
	"enter": 0xff0d, "tab": 0xff09, "esc": 0xff1b, "space": 0x20,
	"backspace": 0xff08, "delete": 0xffff, "insert": 0xff63,
	"home": 0xff50, "end": 0xff57, "pageup": 0xff55, "pagedown": 0xff56,
	"up": 0xff52, "down": 0xff54, "left": 0xff51, "right": 0xff53,
	"f1": 0xffbe, "f2": 0xffbf, "f3": 0xffc0, "f4": 0xffc1,
	"f5": 0xffc2, "f6": 0xffc3, "f7": 0xffc4, "f8": 0xffc5,
	"f9": 0xffc6, "f10": 0xffc7, "f11": 0xffc8, "f12": 0xffc9,
}

var modifierKeysyms = map[string]xproto.Keysym{
	"ctrl":  0xffe3, // Control_L
	"shift": 0xffe1, // Shift_L
	"alt":   0xffe9, // Alt_L
	"super": 0xffeb, // Super_L
}

func keyNameToKeysym(name string) (xproto.Keysym, error) {
	name = normalizeKeyName(name)
	if ks, ok := namedKeysyms[name]; ok {
		return ks, nil
	}
	runes := []rune(name)
	if len(runes) == 1 {
		// letters: prefer the unshifted lowercase keysym, modifiers are explicit
		return runeToKeysym(unicode.ToLower(runes[0])), nil
	}
	return 0, fmt.Errorf("unknown key %q", name)
}

func computerPlatformKey(combo keyCombo) error {
	if waylandSessionActive() {
		return waylandKey(combo)
	}
	c, err := x11Conn()
	if err != nil {
		return err
	}
	// Resolve the main key first so an invalid key fails before any modifier is
	// pressed. A combo with no main key ("super", "ctrl+shift") is a bare
	// modifier tap: only the modifiers are pressed and released.
	var mainKC xproto.Keycode
	var col int
	if combo.Key != "" {
		ks, err := keyNameToKeysym(combo.Key)
		if err != nil {
			return err
		}
		var ok bool
		mainKC, col, ok = lookupKeysym(c, ks)
		if !ok {
			return errKeysymNotFound(ks)
		}
	}

	var pressed []xproto.Keycode
	releaseAll := func() {
		for i := len(pressed) - 1; i >= 0; i-- {
			_ = fakeInput(c, xproto.KeyRelease, byte(pressed[i]), 0, 0, 0)
		}
	}

	for _, m := range combo.Mods {
		sym, ok := modifierKeysyms[m]
		if !ok {
			continue
		}
		kc, _, ok := lookupKeysym(c, sym)
		if !ok {
			continue
		}
		if err := fakeInput(c, xproto.KeyPress, byte(kc), 0, 0, 0); err != nil {
			releaseAll()
			return err
		}
		pressed = append(pressed, kc)
		time.Sleep(2 * time.Millisecond)
	}

	if combo.Key == "" {
		releaseAll()
		return nil
	}

	if col == 1 {
		if kc, _, ok := lookupKeysym(c, 0xffe1); ok {
			if err := fakeInput(c, xproto.KeyPress, byte(kc), 0, 0, 0); err == nil {
				pressed = append(pressed, kc)
			}
		}
	}

	err = tapKeycode(c, mainKC)
	releaseAll()
	return err
}

// computerPlatformCoordSpace: X11 screen coordinates are already in pixels.
func computerPlatformCoordSpace(imgW, imgH int) (float64, float64) {
	if waylandSessionActive() {
		return waylandCoordSpace(imgW, imgH)
	}
	return float64(imgW), float64(imgH)
}
