package tools

import (
	"context"
	"image"
	"os"
	"runtime"
	"strings"
	"testing"

	"fuckyourcli/config"
)

func TestParseKeyCombo(t *testing.T) {
	cases := []struct {
		in      string
		mods    []string
		key     string
		wantErr bool
	}{
		{in: "enter", key: "enter"},
		{in: "ctrl+shift+t", mods: []string{"ctrl", "shift"}, key: "t"},
		{in: "alt+F4", mods: []string{"alt"}, key: "f4"},
		{in: "cmd+c", mods: []string{"super"}, key: "c"},
		// A bare modifier is a valid modifiers-only combo: the bug fixed here
		// rejected "super" as an unknown key because the trailing token was
		// always treated as the main key.
		{in: "super", mods: []string{"super"}},
		{in: "Meta", mods: []string{"super"}},
		{in: "ctrl+shift", mods: []string{"ctrl", "shift"}},
		{in: "win", mods: []string{"super"}},
		{in: "foo+bar", wantErr: true},
		{in: "notakey", wantErr: true},
		{in: "ctrl+", wantErr: true},
		{in: "ctrl+shift+t+x", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseKeyCombo(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseKeyCombo(%q): expected error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseKeyCombo(%q) error: %v", tc.in, err)
			continue
		}
		if got.Key != tc.key {
			t.Errorf("parseKeyCombo(%q).Key = %q, want %q", tc.in, got.Key, tc.key)
		}
		if len(got.Mods) != len(tc.mods) {
			t.Errorf("parseKeyCombo(%q).Mods = %v, want %v", tc.in, got.Mods, tc.mods)
			continue
		}
		for i := range got.Mods {
			if got.Mods[i] != tc.mods[i] {
				t.Errorf("parseKeyCombo(%q).Mods = %v, want %v", tc.in, got.Mods, tc.mods)
			}
		}
	}
}

func TestNormalizeKeyName(t *testing.T) {
	cases := map[string]string{
		"return": "enter",
		"escape": "esc",
		"del":    "delete",
		"pgup":   "pageup",
		"pgdn":   "pagedown",
		"tab":    "tab",
	}
	for in, want := range cases {
		if got := normalizeKeyName(in); got != want {
			t.Errorf("normalizeKeyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBoxDownscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			i := y*src.Stride + x*4
			v := uint8(0)
			if x >= 2 {
				v = 200
			}
			src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = v, v, v, 255
		}
	}
	dst := boxDownscale(src, 2, 2)
	if dst.Bounds().Dx() != 2 || dst.Bounds().Dy() != 2 {
		t.Fatalf("boxDownscale bounds = %v, want 2x2", dst.Bounds())
	}
	if got := dst.Pix[0]; got != 0 {
		t.Errorf("left dst pixel = %d, want 0", got)
	}
	if got := dst.Pix[4]; got != 200 {
		t.Errorf("right dst pixel = %d, want 200", got)
	}
	if got := dst.Pix[3]; got != 255 {
		t.Errorf("alpha = %d, want 255", got)
	}
}

func TestMappedPoint(t *testing.T) {
	defer func() {
		lastCapture.Lock()
		lastCapture.valid = false
		lastCapture.Unlock()
	}()

	// Without a screenshot the mapping must refuse to run.
	if _, _, err := mappedPoint(map[string]interface{}{"x": float64(1), "y": float64(1)}, "x", "y"); err == nil {
		t.Fatal("mappedPoint without screenshot: expected error")
	}

	lastCapture.Lock()
	lastCapture.valid = true
	lastCapture.imgW = 100
	lastCapture.imgH = 50
	lastCapture.scaleX = 2.0
	lastCapture.scaleY = 2.0
	lastCapture.Unlock()

	x, y, err := mappedPoint(map[string]interface{}{"x": float64(10), "y": float64(20)}, "x", "y")
	if err != nil {
		t.Fatalf("mappedPoint error: %v", err)
	}
	if x != 20 || y != 40 {
		t.Errorf("mappedPoint = (%d,%d), want (20,40)", x, y)
	}

	if _, _, err := mappedPoint(map[string]interface{}{"x": float64(500), "y": float64(1)}, "x", "y"); err == nil {
		t.Error("mappedPoint out-of-bounds: expected error")
	}
	if _, _, err := mappedPoint(map[string]interface{}{}, "x", "y"); err == nil {
		t.Error("mappedPoint missing coords: expected error")
	}
}

func TestComputerButton(t *testing.T) {
	if got := computerButton(map[string]interface{}{}, "click"); got != "left" {
		t.Errorf("default button = %q, want left", got)
	}
	if got := computerButton(map[string]interface{}{}, "right_click"); got != "right" {
		t.Errorf("right_click button = %q, want right", got)
	}
	if got := computerButton(map[string]interface{}{"button": "middle"}, "click"); got != "middle" {
		t.Errorf("middle button = %q, want middle", got)
	}
}

func TestIntArg(t *testing.T) {
	args := map[string]interface{}{"a": float64(3.6), "b": 7, "c": int64(2)}
	if got := intArg(args, "a"); got != 4 {
		t.Errorf("intArg(a) = %d, want 4", got)
	}
	if got := intArg(args, "b"); got != 7 {
		t.Errorf("intArg(b) = %d, want 7", got)
	}
	if got := intArg(args, "c"); got != 2 {
		t.Errorf("intArg(c) = %d, want 2", got)
	}
	if got := intArg(args, "missing"); got != 0 {
		t.Errorf("intArg(missing) = %d, want 0", got)
	}
}

// TestComputerCaptureNoDisplay ensures the X11 backend fails fast with a
// clear error when there is no display (headless / native Wayland).
func TestComputerCaptureNoDisplay(t *testing.T) {
	// The check targets the Linux X11 backend: the Windows (GDI) and macOS
	// (`screencapture`) capture paths never look at DISPLAY/WAYLAND_DISPLAY, so
	// the premise does not hold there — a "headless" capture may legitimately
	// succeed (screencapture works on the macOS CI VM).
	if runtime.GOOS != "linux" {
		t.Skip("headless X11 check only applies to the Linux X11 capture backend")
	}
	if os.Getenv("DISPLAY") != "" || (os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("FYC_PORTAL_TEST") != "1") {
		t.Skip("a display is available; skipping headless check")
	}
	if _, err := computerPlatformCapture(); err == nil {
		t.Fatal("computerPlatformCapture without DISPLAY: expected error")
	}
}

// TestExecuteComputerToolSmoke drives the whole tool path (permission layer
// bypassed via auto mode, platform capture, result shaping). On a headless
// machine it must come back with a clear error; with a live X server it must
// return an attached screenshot image.
func TestExecuteComputerToolSmoke(t *testing.T) {
	// On a Wayland session the portal backend may open a real compositor
	// consent/screenshot dialog. Only exercise it when explicitly opted in
	// (see FYC_PORTAL_TEST in COMPUTER.md).
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("FYC_PORTAL_TEST") != "1" {
		t.Skip("Wayland session detected: skipping live computer-use smoke test (set FYC_PORTAL_TEST=1 to run)")
	}
	config.SetNoAsk(true)
	defer config.SetNoAsk(false)

	res := executeComputerTool(context.Background(), map[string]interface{}{"action": "screenshot"})
	if res.Text == "" {
		t.Fatal("empty tool result")
	}
	if strings.HasPrefix(res.Text, "error:") {
		t.Logf("headless/X11 error (expected in CI): %s", res.Text)
		return
	}
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 screenshot attachment, got %d (text: %s)", len(res.Images), res.Text)
	}
	img := res.Images[0]
	if img.MIMEType != "image/jpeg" || img.Data == "" || img.Width <= 0 || img.Height <= 0 {
		t.Fatalf("unexpected image attachment: %+v", img)
	}

	// With a valid capture the coordinate mapping must now be usable.
	if _, _, err := mappedPoint(map[string]interface{}{"x": float64(1), "y": float64(1)}, "x", "y"); err != nil {
		t.Fatalf("mappedPoint after screenshot: %v", err)
	}
}
