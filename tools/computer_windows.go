//go:build windows

package tools

import (
	"fmt"
	"image"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"
	"unsafe"
)

// Windows backend for computer use: GDI screen capture + SendInput input
// synthesis. Pure Go syscalls, no cgo, no external tools.

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC                     = user32.NewProc("GetDC")
	procReleaseDC                 = user32.NewProc("ReleaseDC")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procSendInput                 = user32.NewProc("SendInput")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procVkKeyScanW                = user32.NewProc("VkKeyScanW")
	procCreateCompatibleDC        = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap    = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject              = gdi32.NewProc("SelectObject")
	procBitBlt                    = gdi32.NewProc("BitBlt")
	procGetDIBits                 = gdi32.NewProc("GetDIBits")
	procDeleteObject              = gdi32.NewProc("DeleteObject")
	procDeleteDC                  = gdi32.NewProc("DeleteDC")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srcCopy      = 0x00CC0020
	dibRGBColors = 0

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfUnicode     = 0x0004

	inputMouse    = 0
	inputKeyboard = 1

	wheelDelta = 120
)

type winMouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type winKeyboardInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// winInput mirrors the Windows INPUT union. winMouseInput is the largest
// union member, so it doubles as the storage for keyboard input too.
type winInput struct {
	Type  uint32
	Union winMouseInput
}

var dpiOnce sync.Once

func ensureDPIAware() {
	dpiOnce.Do(func() {
		if err := procSetProcessDpiAwarenessCtx.Find(); err == nil {
			// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (HANDLE)-4
			procSetProcessDpiAwarenessCtx.Call(^uintptr(3))
			return
		}
		procSetProcessDPIAware.Call()
	})
}

func systemMetrics(index int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(v)
}

func virtualScreenBounds() (x, y, w, h int) {
	return systemMetrics(smXVirtualScreen),
		systemMetrics(smYVirtualScreen),
		systemMetrics(smCXVirtualScreen),
		systemMetrics(smCYVirtualScreen)
}

// ── screenshot ───────────────────────────────────────────────────────────────

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

func computerPlatformCapture() (*image.RGBA, error) {
	ensureDPIAware()
	vx, vy, w, h := virtualScreenBounds()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("cannot determine the virtual screen size")
	}

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(hdcMem)

	hbmp, _, _ := procCreateCompatibleBitmap.Call(hdcScreen, uintptr(w), uintptr(h))
	if hbmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbmp)

	old, _, _ := procSelectObject.Call(hdcMem, hbmp)
	if old != 0 {
		defer procSelectObject.Call(hdcMem, old)
	}

	ret, _, _ := procBitBlt.Call(hdcMem, 0, 0, uintptr(w), uintptr(h), hdcScreen, uintptr(vx), uintptr(vy), srcCopy)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	buf := make([]byte, w*h*4)
	bmi := bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(w),
		BiHeight:      -int32(h), // top-down
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: 0, // BI_RGB
	}
	ret, _, _ = procGetDIBits.Call(hdcMem, hbmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bmi)), dibRGBColors)
	runtime.KeepAlive(buf)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b := buf[i*4]
		g := buf[i*4+1]
		r := buf[i*4+2]
		img.Pix[i*4] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = 255
	}
	return img, nil
}

// ── input ────────────────────────────────────────────────────────────────────

func sendInputs(ins []winInput) error {
	if len(ins) == 0 {
		return nil
	}
	ret, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0]))
	runtime.KeepAlive(ins)
	if int(ret) != len(ins) {
		if err != syscall.Errno(0) {
			return fmt.Errorf("SendInput failed: %v", err)
		}
		return fmt.Errorf("SendInput inserted %d of %d events", int(ret), len(ins))
	}
	return nil
}

func mouseInput(flags uint32, dx, dy int32, data uint32) winInput {
	in := winInput{Type: inputMouse}
	in.Union.Dx = dx
	in.Union.Dy = dy
	in.Union.MouseData = data
	in.Union.DwFlags = flags
	return in
}

func normalizedCoords(x, y int) (int32, int32) {
	vx, vy, vw, vh := virtualScreenBounds()
	if vw <= 1 {
		vw = 2
	}
	if vh <= 1 {
		vh = 2
	}
	nx := int32((x - vx) * 65535 / (vw - 1))
	ny := int32((y - vy) * 65535 / (vh - 1))
	return nx, ny
}

func moveMouseAbsolute(x, y int) error {
	nx, ny := normalizedCoords(x, y)
	return sendInputs([]winInput{mouseInput(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk, nx, ny, 0)})
}

func computerPlatformMove(x, y int) error {
	ensureDPIAware()
	return moveMouseAbsolute(x, y)
}

func buttonFlags(button string) (down, up uint32, err error) {
	switch button {
	case "left", "":
		return mouseeventfLeftDown, mouseeventfLeftUp, nil
	case "right":
		return mouseeventfRightDown, mouseeventfRightUp, nil
	case "middle":
		return mouseeventfMiddleDown, mouseeventfMiddleUp, nil
	}
	return 0, 0, fmt.Errorf("unknown mouse button %q", button)
}

func computerPlatformClick(x, y int, button string, count int) error {
	ensureDPIAware()
	down, up, err := buttonFlags(button)
	if err != nil {
		return err
	}
	if err := moveMouseAbsolute(x, y); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(60 * time.Millisecond)
		}
		if err := sendInputs([]winInput{
			mouseInput(down, 0, 0, 0),
			mouseInput(up, 0, 0, 0),
		}); err != nil {
			return err
		}
	}
	return nil
}

func computerPlatformDrag(x1, y1, x2, y2 int, button string) error {
	ensureDPIAware()
	down, up, err := buttonFlags(button)
	if err != nil {
		return err
	}
	if err := moveMouseAbsolute(x1, y1); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := sendInputs([]winInput{mouseInput(down, 0, 0, 0)}); err != nil {
		return err
	}
	const steps = 16
	for i := 1; i <= steps; i++ {
		x := x1 + (x2-x1)*i/steps
		y := y1 + (y2-y1)*i/steps
		nx, ny := normalizedCoords(x, y)
		if err := sendInputs([]winInput{mouseInput(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk, nx, ny, 0)}); err != nil {
			_ = sendInputs([]winInput{mouseInput(up, 0, 0, 0)})
			return err
		}
		time.Sleep(8 * time.Millisecond)
	}
	return sendInputs([]winInput{mouseInput(up, 0, 0, 0)})
}

func computerPlatformScroll(x, y int, direction string, amount int) error {
	ensureDPIAware()
	if err := moveMouseAbsolute(x, y); err != nil {
		return err
	}
	var flag uint32
	var delta int32
	switch direction {
	case "up":
		flag, delta = mouseeventfWheel, wheelDelta
	case "down":
		flag, delta = mouseeventfWheel, -wheelDelta
	case "left":
		flag, delta = mouseeventfHWheel, -wheelDelta
	case "right":
		flag, delta = mouseeventfHWheel, wheelDelta
	default:
		return fmt.Errorf("unknown scroll direction %q", direction)
	}
	var ins []winInput
	for i := 0; i < amount; i++ {
		ins = append(ins, mouseInput(flag, 0, 0, uint32(delta)))
	}
	return sendInputs(ins)
}

// ── keyboard ─────────────────────────────────────────────────────────────────

func keyInput(vk uint16, scan uint16, flags uint32) winInput {
	in := winInput{Type: inputKeyboard}
	ki := (*winKeyboardInput)(unsafe.Pointer(&in.Union))
	ki.WVk = vk
	ki.WScan = scan
	ki.DwFlags = flags
	return in
}

func computerPlatformTypeText(text string) error {
	ensureDPIAware()
	units := utf16.Encode([]rune(text))
	for _, u := range units {
		if err := sendInputs([]winInput{
			keyInput(0, u, keyeventfUnicode),
			keyInput(0, u, keyeventfUnicode|keyeventfKeyUp),
		}); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

var namedVKs = map[string]uint16{
	"enter": 0x0D, "tab": 0x09, "esc": 0x1B, "space": 0x20,
	"backspace": 0x08, "delete": 0x2E, "insert": 0x2D,
	"home": 0x24, "end": 0x23, "pageup": 0x21, "pagedown": 0x22,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73, "f5": 0x74, "f6": 0x75,
	"f7": 0x76, "f8": 0x77, "f9": 0x78, "f10": 0x79, "f11": 0x7A, "f12": 0x7B,
}

var modifierVKs = map[string]uint16{
	"ctrl":  0x11, // VK_CONTROL
	"shift": 0x10, // VK_SHIFT
	"alt":   0x12, // VK_MENU
	"super": 0x5B, // VK_LWIN
}

// vkForKey resolves a key name (or single character) to a VK code and whether
// Shift must be held while pressing it.
func vkForKey(name string) (uint16, bool, error) {
	name = normalizeKeyName(name)
	if vk, ok := namedVKs[name]; ok {
		return vk, false, nil
	}
	runes := []rune(name)
	if len(runes) == 1 {
		ch := unicode.ToLower(runes[0])
		ret, _, _ := procVkKeyScanW.Call(uintptr(ch))
		if int16(ret) == -1 {
			return 0, false, fmt.Errorf("character %q cannot be typed on the current keyboard layout", string(runes[0]))
		}
		vk := uint16(ret & 0xff)
		shift := (ret>>8)&1 == 1
		return vk, shift, nil
	}
	return 0, false, fmt.Errorf("unknown key %q", name)
}

func computerPlatformKey(combo keyCombo) error {
	ensureDPIAware()
	// A combo with no main key ("super", "ctrl+shift") is a bare modifier tap.
	var vk uint16
	var needShift bool
	if combo.Key != "" {
		var err error
		vk, needShift, err = vkForKey(combo.Key)
		if err != nil {
			return err
		}
	}

	var downs []winInput
	var ups []winInput

	shiftPressed := false
	for _, m := range combo.Mods {
		mvk, ok := modifierVKs[m]
		if !ok {
			continue
		}
		downs = append(downs, keyInput(mvk, 0, 0))
		ups = append([]winInput{keyInput(mvk, 0, keyeventfKeyUp)}, ups...)
		if m == "shift" {
			shiftPressed = true
		}
	}
	if needShift && !shiftPressed {
		if svk, ok := modifierVKs["shift"]; ok {
			downs = append([]winInput{keyInput(svk, 0, 0)}, downs...)
			ups = append(ups, keyInput(svk, 0, keyeventfKeyUp))
		}
	}

	if combo.Key != "" {
		downs = append(downs, keyInput(vk, 0, 0))
		ups = append([]winInput{keyInput(vk, 0, keyeventfKeyUp)}, ups...)
	}

	seq := append(append([]winInput{}, downs...), ups...)
	return sendInputs(seq)
}

// computerPlatformCoordSpace: Windows coordinates are already in pixels.
func computerPlatformCoordSpace(imgW, imgH int) (float64, float64) {
	return float64(imgW), float64(imgH)
}
