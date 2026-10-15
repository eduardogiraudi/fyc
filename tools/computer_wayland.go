//go:build linux

package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"net/url"
	"os"
	"sync"
	"time"

	"fuckyourcli/config"

	"github.com/godbus/dbus/v5"
)

// Wayland backend for computer use, implemented on top of the XDG desktop
// portals (pure Go, no cgo, no external binaries):
//
//   - screen capture : org.freedesktop.portal.Screenshot
//   - input          : org.freedesktop.portal.RemoteDesktop (+ ScreenCast to
//                      obtain a PipeWire stream whose logical geometry defines
//                      the absolute pointer coordinate space)
//
// On Wayland sessions with rootless Xwayland the X11 backend cannot be used
// (GetImage returns BadMatch and XTEST input only reaches X11 clients), so this
// backend is selected whenever WAYLAND_DISPLAY is set.
//
// The first input action triggers the compositor consent dialog
// (RemoteDesktop.Start); the resulting restore_token is persisted in the config
// so subsequent runs are silent.

const (
	portalScreenshotIface    = "org.freedesktop.portal.Screenshot"
	portalScreenCastIface    = "org.freedesktop.portal.ScreenCast"
	portalRemoteDesktopIface = "org.freedesktop.portal.RemoteDesktop"

	portalDeviceKeyboard = 1 // RemoteDesktop.AvailableDeviceTypes: KEYBOARD
	portalDevicePointer  = 2 // RemoteDesktop.AvailableDeviceTypes: POINTER

	portalSourceMonitor      = 1 // ScreenCast.AvailableSourceTypes: MONITOR
	portalCursorModeEmbedded = 2 // ScreenCast cursor drawn inside the stream

	portalPersistUntilRevoked = 2 // persist_mode: keep the permission until revoked

	// portalDialogTimeout bounds the calls that may wait for a compositor dialog.
	portalDialogTimeout = 2 * time.Minute
	// portalNotifyTimeout bounds the fire-and-forget input calls.
	portalNotifyTimeout = 10 * time.Second
)

// Wayland-specific portal responses.
const (
	portalResponseSuccess  = 0
	portalResponseCanceled = 1
	portalResponseFailed   = 2
)

// errNoRemoteInteraction marks a session that the compositor granted with
// screen sharing only: `devices` carried neither KEYBOARD nor POINTER.
var errNoRemoteInteraction = errors.New("the consent dialog was accepted without remote interaction")

// waylandStream describes the monitor shared through the portal. Its logical
// geometry (size in compositor coordinates, offset within the desktop) is the
// coordinate space used by the absolute pointer notifications.
type waylandStream struct {
	handle     dbus.ObjectPath
	nodeID     uint32
	posX, posY int
	sizeW      int
	sizeH      int
}

var waylandState struct {
	sync.Mutex
	stream    *waylandStream
	initError error
}

func init() {
	// Release the portal session on process exit (see main.go).
	closeComputerSessions = closePortalSessions
}

// waylandSessionActive reports whether the process runs under a Wayland session.
func waylandSessionActive() bool {
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// closePortalSessions closes the remote-desktop session, if any.
func closePortalSessions() {
	waylandState.Lock()
	st := waylandState.stream
	waylandState.stream = nil
	waylandState.initError = nil
	waylandState.Unlock()
	if st == nil {
		return
	}
	if cl, err := getPortalClient(); err == nil {
		cl.closeSession(st.handle)
	}
}

// waylandSessionClosed is invoked when the compositor tears the session down;
// the next action recreates it.
func waylandSessionClosed(handle dbus.ObjectPath) {
	waylandState.Lock()
	if waylandState.stream != nil && waylandState.stream.handle == handle {
		waylandState.stream = nil
	}
	waylandState.Unlock()
}

// ── screenshot ───────────────────────────────────────────────────────────────

func waylandCapture() (*image.RGBA, error) {
	cl, err := getPortalClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), portalDialogTimeout)
	defer cancel()

	token := newPortalToken("fycscreenshot")
	code, results, err := cl.request(ctx, token, func() error {
		opts := portalOptions(map[string]interface{}{
			"handle_token": token,
			"interactive":  false,
			"modal":        false,
		})
		return cl.call(portalScreenshotIface+".Screenshot", "", opts)
	})
	if err != nil {
		return nil, fmt.Errorf("screenshot via the desktop portal failed: %w", err)
	}
	switch code {
	case portalResponseCanceled:
		return nil, fmt.Errorf("the screenshot was cancelled by the user")
	case portalResponseFailed:
		return nil, fmt.Errorf("the desktop portal refused to take a screenshot. " +
			"On GNOME this usually means screenshots are denied for this app: allow them in " +
			"Settings → Privacy → Screenshots, ensure ~/Pictures exists, and reset a stale " +
			"decision by removing ~/.local/share/flatpak/db/screenshot (xdg-desktop-portal " +
			"restarts on its own)")
	}

	path, err := fileFromURI(variantString(results["uri"]))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open the screenshot produced by the portal (%s): %w", path, err)
	}
	defer f.Close()
	decoded, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("cannot decode the screenshot produced by the portal: %w", err)
	}
	// The portal leaves the file in ~/Pictures: remove it to avoid accumulation.
	_ = f.Close()
	_ = os.Remove(path)

	return toRGBA(decoded), nil
}

// fileFromURI converts a file:// URI (percent-encoded) to a local path.
func fileFromURI(uri string) (string, error) {
	if uri == "" {
		return "", fmt.Errorf("the desktop portal returned no screenshot URI")
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid screenshot URI %q: %w", uri, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported screenshot URI scheme %q", u.Scheme)
	}
	return u.Path, nil
}

func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// ── coordinate space ─────────────────────────────────────────────────────────

// waylandCoordSpace returns the size of the logical desktop. The screenshot is
// the whole desktop in physical pixels, while portal input uses compositor
// logical coordinates, so the mapping must divide by the scale factor. On GNOME
// the logical layout is read from Mutter's DisplayConfig (no consent needed);
// otherwise the shared monitor geometry is used as a single-monitor best effort.
func waylandCoordSpace(imgW, imgH int) (float64, float64) {
	if w, h, ok := waylandGlobalLogicalSize(); ok && w > 0 && h > 0 {
		return float64(w), float64(h)
	}
	waylandState.Lock()
	st := waylandState.stream
	waylandState.Unlock()
	if st != nil && st.sizeW > 0 && st.sizeH > 0 {
		return float64(st.posX + st.sizeW), float64(st.posY + st.sizeH)
	}
	return float64(imgW), float64(imgH)
}

// waylandGlobalLogicalSize asks Mutter for the full logical desktop bounding box.
func waylandGlobalLogicalSize() (int, int, bool) {
	cl, err := getPortalClient()
	if err != nil {
		return 0, 0, false
	}
	obj := cl.conn.Object("org.gnome.Mutter.DisplayConfig", "/org/gnome/Mutter/DisplayConfig")
	var body []interface{}
	if err := obj.Call("org.gnome.Mutter.DisplayConfig.GetCurrentState", 0).Store(&body); err != nil {
		return 0, 0, false
	}
	return logicalDesktopSizeFromState(body)
}

// logicalDesktopSizeFromState extracts the logical desktop bounding box from a
// Mutter DisplayConfig GetCurrentState reply:
//
//	(u serial, a(...) monitors, a(iiduba(ssss)a{sv}) logical_monitors, a{sv} props)
//
// Each logical monitor has (x, y, scale, transform, primary, monitors, props);
// its logical size is the current mode size divided by the scale.
func logicalDesktopSizeFromState(body []interface{}) (int, int, bool) {
	if len(body) < 3 {
		return 0, 0, false
	}
	monitors, ok := body[1].([]interface{})
	if !ok {
		return 0, 0, false
	}
	logicalMonitors, ok := body[2].([]interface{})
	if !ok {
		return 0, 0, false
	}

	// connector -> current mode pixel size
	modeSize := make(map[string][2]int)
	for _, raw := range monitors {
		mon, ok := raw.([]interface{})
		if !ok || len(mon) < 2 {
			continue
		}
		conn, _ := mon[0].([]interface{})
		if len(conn) == 0 {
			continue
		}
		name, _ := conn[0].(string)
		modes, _ := mon[1].([]interface{})
		for _, rawMode := range modes {
			mode, ok := rawMode.([]interface{})
			if !ok || len(mode) < 3 {
				continue
			}
			props, _ := mode[6].(map[string]dbus.Variant)
			if cur, ok := props["is-current"]; ok {
				if b, _ := cur.Value().(bool); b {
					w, _ := mode[1].(int32)
					h, _ := mode[2].(int32)
					modeSize[name] = [2]int{int(w), int(h)}
				}
			}
		}
	}

	minX, minY := math.MaxInt32, math.MaxInt32
	maxX, maxY := math.MinInt32, math.MinInt32
	for _, raw := range logicalMonitors {
		lm, ok := raw.([]interface{})
		if !ok || len(lm) < 6 {
			continue
		}
		x, _ := lm[0].(int32)
		y, _ := lm[1].(int32)
		scale, _ := lm[2].(float64)
		if scale <= 0 {
			scale = 1
		}
		names, _ := lm[5].([]interface{})
		for _, rawName := range names {
			pair, ok := rawName.([]interface{})
			if !ok || len(pair) == 0 {
				continue
			}
			name, _ := pair[0].(string)
			size, ok := modeSize[name]
			if !ok {
				continue
			}
			lw := int(math.Round(float64(size[0]) / scale))
			lh := int(math.Round(float64(size[1]) / scale))
			if int(x) < minX {
				minX = int(x)
			}
			if int(y) < minY {
				minY = int(y)
			}
			if int(x)+lw > maxX {
				maxX = int(x) + lw
			}
			if int(y)+lh > maxY {
				maxY = int(y) + lh
			}
		}
	}
	if maxX <= minX || maxY <= minY {
		return 0, 0, false
	}
	return maxX - minX, maxY - minY, true
}

// ── input session ────────────────────────────────────────────────────────────

// waylandInputSession returns the portal client and the input session, creating
// the session (and thus showing the consent dialog on first use) if needed.
func waylandInputSession() (*portalClient, *waylandStream, error) {
	cl, err := getPortalClient()
	if err != nil {
		return nil, nil, err
	}
	waylandState.Lock()
	defer waylandState.Unlock()
	if st := waylandState.stream; st != nil {
		return cl, st, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), portalDialogTimeout)
	defer cancel()
	st, err := waylandCreateSession(ctx, cl)
	if errors.Is(err, errNoRemoteInteraction) && config.Get().WaylandRestoreToken != "" {
		// A stored restore token is restored silently, without the dialog, so a
		// grant persisted as "screen only" (devices == 0) would otherwise lock
		// the user out for good: the dialog never comes back and the token is
		// kept in the config. Drop it and prompt afresh.
		config.SetWaylandRestoreToken("")
		config.Save()
		st, err = waylandCreateSession(ctx, cl)
	}
	if err != nil {
		return nil, nil, err
	}
	waylandState.stream = st
	return cl, st, nil
}

func waylandCreateSession(ctx context.Context, cl *portalClient) (*waylandStream, error) {
	// 1. Remote-desktop session.
	createToken := newPortalToken("fycrdcreate")
	sessionToken := newPortalToken("fycrd")
	code, results, err := cl.request(ctx, createToken, func() error {
		return cl.call(portalRemoteDesktopIface+".CreateSession", portalOptions(map[string]interface{}{
			"handle_token":         createToken,
			"session_handle_token": sessionToken,
		}))
	})
	if err != nil {
		return nil, fmt.Errorf("cannot create the remote-desktop session: %w", err)
	}
	if code != portalResponseSuccess {
		return nil, fmt.Errorf("the portal refused to create the remote-desktop session (response %d)", code)
	}
	handle := variantPath(results["session_handle"])
	if handle == "" {
		return nil, fmt.Errorf("the portal did not return a remote-desktop session handle")
	}

	// 2. Screen cast sources on the same session: needed so that absolute
	//    pointer coordinates can be expressed in the monitor's logical space.
	if err := waylandSelectSources(ctx, cl, handle); err != nil {
		cl.closeSession(handle)
		return nil, err
	}

	// 3. Input devices, with persistence so later runs are silent.
	if err := waylandSelectDevices(ctx, cl, handle); err != nil {
		cl.closeSession(handle)
		return nil, err
	}

	// 4. Start: on first use the compositor shows the consent dialog.
	startToken := newPortalToken("fycrdstart")
	code, results, err = cl.request(ctx, startToken, func() error {
		return cl.call(portalRemoteDesktopIface+".Start", handle, "", portalOptions(map[string]interface{}{
			"handle_token": startToken,
		}))
	})
	if err != nil {
		cl.closeSession(handle)
		return nil, fmt.Errorf("cannot start the remote-desktop session: %w", err)
	}
	switch code {
	case portalResponseCanceled:
		cl.closeSession(handle)
		return nil, fmt.Errorf("the computer-use consent dialog was cancelled")
	case portalResponseFailed:
		cl.closeSession(handle)
		return nil, fmt.Errorf("the portal refused to start the remote-desktop session " +
			"(check Settings → Privacy and restart xdg-desktop-portal if needed)")
	}

	streams := parseStreams(results["streams"])
	if len(streams) == 0 {
		cl.closeSession(handle)
		return nil, fmt.Errorf("the portal did not provide any screen stream: " +
			"the selected monitor could not be determined")
	}
	st := streams[0]
	st.handle = handle
	if st.sizeW <= 0 || st.sizeH <= 0 {
		cl.closeSession(handle)
		return nil, fmt.Errorf("the portal returned an invalid monitor geometry (%dx%d)", st.sizeW, st.sizeH)
	}

	// Start reports the device types the backend actually granted. On GNOME the
	// consent dialog ("Remote Desktop") has an "Allow Remote Interaction" switch
	// that is OFF by default while the "Share" button is enabled by the screen
	// source alone: accepting such a dialog starts a screen-sharing-only session
	// and every Notify* would then fail with the cryptic "Session is not allowed
	// to call NotifyPointer methods". Fail early with an actionable message.
	if devices := variantUint32(results["devices"]); devices&(portalDeviceKeyboard|portalDevicePointer) == 0 {
		cl.closeSession(handle)
		return nil, fmt.Errorf("%w: the session can share the screen but not the "+
			"keyboard/pointer. Re-run and enable \"Allow Remote Interaction\" in the "+
			"portal dialog (if the dialog no longer appears, a remembered choice is "+
			"being reused: reset the stored remote-desktop permission)", errNoRemoteInteraction)
	}

	if token := variantString(results["restore_token"]); token != "" {
		config.SetWaylandRestoreToken(token)
		config.Save()
	}
	cl.watchSession(handle, func() { waylandSessionClosed(handle) })
	return st, nil
}

func waylandSelectSources(ctx context.Context, cl *portalClient, handle dbus.ObjectPath) error {
	token := newPortalToken("fycscreencast")
	code, _, err := cl.request(ctx, token, func() error {
		return cl.call(portalScreenCastIface+".SelectSources", handle, portalOptions(map[string]interface{}{
			"handle_token": token,
			"types":        uint32(portalSourceMonitor),
			"multiple":     false,
			"cursor_mode":  uint32(portalCursorModeEmbedded),
		}))
	})
	if err != nil {
		return fmt.Errorf("cannot select the screen source: %w", err)
	}
	if code != portalResponseSuccess {
		return fmt.Errorf("the portal refused to select the screen source (response %d)", code)
	}
	return nil
}

func waylandSelectDevices(ctx context.Context, cl *portalClient, handle dbus.ObjectPath) error {
	base := map[string]interface{}{
		"types":        uint32(portalDeviceKeyboard | portalDevicePointer),
		"persist_mode": uint32(portalPersistUntilRevoked),
	}
	restoreToken := config.Get().WaylandRestoreToken

	do := func(withToken bool) (uint32, error) {
		token := newPortalToken("fycdevices")
		opts := map[string]interface{}{"handle_token": token}
		for k, v := range base {
			opts[k] = v
		}
		if withToken && restoreToken != "" {
			opts["restore_token"] = restoreToken
		}
		code, _, err := cl.request(ctx, token, func() error {
			return cl.call(portalRemoteDesktopIface+".SelectDevices", handle, portalOptions(opts))
		})
		return code, err
	}

	code, err := do(true)
	if err != nil && restoreToken != "" {
		// The stored token may have been invalidated: retry once without it.
		code, err = do(false)
	}
	if err != nil {
		return fmt.Errorf("cannot select the input devices: %w", err)
	}
	if code != portalResponseSuccess {
		return fmt.Errorf("the portal refused to select the input devices (response %d)", code)
	}
	return nil
}

// parseStreams decodes the ScreenCast/RemoteDesktop "streams" result
// (a(ua{sv})) into monitor descriptions.
func parseStreams(v dbus.Variant) []*waylandStream {
	// godbus decodes the "streams" array (a(ua{sv})) into a typed
	// [][]interface{} — one inner slice per stream struct — so a plain
	// []interface{} assertion silently yields zero streams. Accept both shapes
	// to stay independent of the decoder's slice typing.
	var arr []interface{}
	switch t := v.Value().(type) {
	case []interface{}:
		arr = t
	case [][]interface{}:
		for _, e := range t {
			arr = append(arr, e)
		}
	}
	var out []*waylandStream
	for _, raw := range arr {
		tuple, ok := raw.([]interface{})
		if !ok || len(tuple) < 2 {
			continue
		}
		st := &waylandStream{}
		st.nodeID, _ = tuple[0].(uint32)
		props, _ := tuple[1].(map[string]dbus.Variant)
		if pos, ok := intTuple(props["position"]); ok {
			st.posX, st.posY = pos[0], pos[1]
		}
		if size, ok := intTuple(props["size"]); ok {
			st.sizeW, st.sizeH = size[0], size[1]
		}
		out = append(out, st)
	}
	return out
}

// toStreamCoords converts a desktop logical point into the shared monitor's
// local logical space (the space expected by NotifyPointerMotionAbsolute).
func (s *waylandStream) toStreamCoords(x, y int) (float64, float64, error) {
	lx, ly := x-s.posX, y-s.posY
	if lx < 0 || ly < 0 || lx >= s.sizeW || ly >= s.sizeH {
		return 0, 0, fmt.Errorf("point (%d,%d) is outside the shared monitor (%dx%d at %d,%d): "+
			"take a screenshot again and re-run selecting the monitor that contains it",
			x, y, s.sizeW, s.sizeH, s.posX, s.posY)
	}
	return float64(lx), float64(ly), nil
}

// ── mouse ────────────────────────────────────────────────────────────────────

func evdevButton(button string) int32 {
	switch button {
	case "middle":
		return 0x112 // BTN_MIDDLE
	case "right":
		return 0x111 // BTN_RIGHT
	default:
		return 0x110 // BTN_LEFT
	}
}

func waylandButton(button int32, pressed bool) error {
	cl, st, err := waylandInputSession()
	if err != nil {
		return err
	}
	state := uint32(0)
	if pressed {
		state = 1
	}
	return cl.notify(portalRemoteDesktopIface+".NotifyPointerButton", st.handle, portalEmptyOptions, button, state)
}

func waylandMove(x, y int) error {
	cl, st, err := waylandInputSession()
	if err != nil {
		return err
	}
	lx, ly, err := st.toStreamCoords(x, y)
	if err != nil {
		return err
	}
	return cl.notify(portalRemoteDesktopIface+".NotifyPointerMotionAbsolute",
		st.handle, portalEmptyOptions, st.nodeID, lx, ly)
}

func waylandClick(x, y int, button string, count int) error {
	if err := waylandMove(x, y); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	code := evdevButton(button)
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(80 * time.Millisecond) // allow double-click detection
		}
		if err := waylandButton(code, true); err != nil {
			return err
		}
		if err := waylandButton(code, false); err != nil {
			return err
		}
	}
	return nil
}

func waylandDrag(x1, y1, x2, y2 int, button string) error {
	if err := waylandMove(x1, y1); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	code := evdevButton(button)
	if err := waylandButton(code, true); err != nil {
		return err
	}
	const steps = 16
	for i := 1; i <= steps; i++ {
		x := x1 + (x2-x1)*i/steps
		y := y1 + (y2-y1)*i/steps
		if err := waylandMove(x, y); err != nil {
			_ = waylandButton(code, false)
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return waylandButton(code, false)
}

func waylandScroll(x, y int, direction string, amount int) error {
	if err := waylandMove(x, y); err != nil {
		return err
	}
	// axis 0 = vertical, 1 = horizontal; negative steps scroll up/left.
	var axis uint32
	var steps int32
	switch direction {
	case "up":
		axis, steps = 0, -int32(amount)
	case "down":
		axis, steps = 0, int32(amount)
	case "left":
		axis, steps = 1, -int32(amount)
	case "right":
		axis, steps = 1, int32(amount)
	default:
		return fmt.Errorf("unknown scroll direction %q", direction)
	}
	cl, st, err := waylandInputSession()
	if err != nil {
		return err
	}
	return cl.notify(portalRemoteDesktopIface+".NotifyPointerAxisDiscrete",
		st.handle, portalEmptyOptions, axis, steps)
}

// ── keyboard ─────────────────────────────────────────────────────────────────

// notifyKeysym presses or releases a keysym (layout-aware: the compositor maps
// it to the right keycode and shift level).
func notifyKeysym(cl *portalClient, st *waylandStream, keysym uint32, pressed bool) error {
	state := uint32(0)
	if pressed {
		state = 1
	}
	return cl.notify(portalRemoteDesktopIface+".NotifyKeyboardKeysym",
		st.handle, portalEmptyOptions, int32(keysym), state)
}

// notifyKeycode presses or releases an evdev keycode.
func notifyKeycode(cl *portalClient, st *waylandStream, keycode int32, pressed bool) error {
	state := uint32(0)
	if pressed {
		state = 1
	}
	return cl.notify(portalRemoteDesktopIface+".NotifyKeyboardKeycode",
		st.handle, portalEmptyOptions, keycode, state)
}

func waylandTypeText(text string) error {
	cl, st, err := waylandInputSession()
	if err != nil {
		return err
	}
	for _, r := range text {
		var keysym uint32
		switch r {
		case '\n':
			keysym = 0xff0d // Return
		case '\t':
			keysym = 0xff09 // Tab
		default:
			keysym = uint32(runeToKeysym(r))
		}
		if err := notifyKeysym(cl, st, keysym, true); err != nil {
			return err
		}
		if err := notifyKeysym(cl, st, keysym, false); err != nil {
			return err
		}
		time.Sleep(4 * time.Millisecond)
	}
	return nil
}

// evdev keycodes (linux/input-event-codes.h) for key combos.
var evdevKeycodes = map[string]int32{
	"a": 30, "b": 48, "c": 46, "d": 32, "e": 18, "f": 33, "g": 34, "h": 35,
	"i": 23, "j": 36, "k": 37, "l": 38, "m": 50, "n": 49, "o": 24, "p": 25,
	"q": 16, "r": 19, "s": 31, "t": 20, "u": 22, "v": 47, "w": 17, "x": 45,
	"y": 21, "z": 44,
	"1": 2, "2": 3, "3": 4, "4": 5, "5": 6, "6": 7, "7": 8, "8": 9, "9": 10, "0": 11,
	"enter": 28, "tab": 15, "esc": 1, "space": 57, "backspace": 14,
	"delete": 111, "insert": 110, "home": 102, "end": 107,
	"pageup": 104, "pagedown": 109,
	"up": 103, "down": 108, "left": 105, "right": 106,
	"f1": 59, "f2": 60, "f3": 61, "f4": 62, "f5": 63, "f6": 64,
	"f7": 65, "f8": 66, "f9": 67, "f10": 68, "f11": 87, "f12": 88,
	"-": 12, "=": 13, "[": 26, "]": 27, "\\": 43, ";": 39,
	"'": 40, "`": 41, ",": 51, ".": 52, "/": 53,
}

var evdevModifiers = map[string]int32{
	"ctrl":  29,  // KEY_LEFTCTRL
	"shift": 42,  // KEY_LEFTSHIFT
	"alt":   56,  // KEY_LEFTALT
	"super": 125, // KEY_LEFTMETA
}

func waylandKey(combo keyCombo) error {
	cl, st, err := waylandInputSession()
	if err != nil {
		return err
	}
	var pressed []int32
	releaseAll := func() {
		for i := len(pressed) - 1; i >= 0; i-- {
			_ = notifyKeycode(cl, st, pressed[i], false)
		}
	}

	for _, m := range combo.Mods {
		kc, ok := evdevModifiers[m]
		if !ok {
			continue
		}
		if err := notifyKeycode(cl, st, kc, true); err != nil {
			releaseAll()
			return err
		}
		pressed = append(pressed, kc)
		time.Sleep(2 * time.Millisecond)
	}

	// A combo with no main key ("super", "ctrl+shift") is a bare modifier tap:
	// the modifiers are pressed and released without a main key.
	if combo.Key == "" {
		releaseAll()
		return nil
	}

	key := normalizeKeyName(combo.Key)
	if kc, ok := evdevKeycodes[key]; ok {
		if err := notifyKeycode(cl, st, kc, true); err != nil {
			releaseAll()
			return err
		}
		if err := notifyKeycode(cl, st, kc, false); err != nil {
			releaseAll()
			return err
		}
	} else {
		// Fall back to the keysym path for keys not in the evdev table.
		keysym, err := keyNameToKeysym(combo.Key)
		if err != nil {
			releaseAll()
			return err
		}
		if err := notifyKeysym(cl, st, uint32(keysym), true); err != nil {
			releaseAll()
			return err
		}
		if err := notifyKeysym(cl, st, uint32(keysym), false); err != nil {
			releaseAll()
			return err
		}
	}
	releaseAll()
	return nil
}
