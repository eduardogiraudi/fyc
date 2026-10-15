package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"strings"
	"sync"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// Computer-use tool: drives the user's real desktop (screenshot + mouse +
// keyboard). Input synthesis is implemented natively in Go for every
// supported OS (see computer_linux.go / computer_wayland.go /
// computer_windows.go / computer_darwin.go); no external binaries are required.
//
// The `computer` tool returns a ToolResult whose Images field carries the
// screenshot. Providers that cannot digest images inside tool results
// (OpenAI Chat Completions, Gemini < 3) never see the tool: it is filtered
// out of the advertised tool list and a message is shown to the user.

const (
	// maxScreenshotSide is the maximum width/height of the screenshot that is
	// sent to the model (bigger sides are downscaled to save tokens).
	maxScreenshotSide = 1568
	jpegQuality       = 80
	// computerActionDelay lets the UI settle after an input event before the
	// next screenshot is taken.
	computerActionDelay = 150 * time.Millisecond
)

// IsComputerTool reports whether name is the computer-use tool.
func IsComputerTool(name string) bool {
	return name == "computer"
}

// IsMediaTool reports whether name is the read_media tool.
func IsMediaTool(name string) bool {
	return name == "read_media"
}

// ToolImageBlockReason returns a human-readable reason when the currently
// selected provider/model cannot consume images inside tool results, or ""
// when they are supported. It is shared by every tool that returns images
// (computer use, read_media).
func ToolImageBlockReason() string {
	cfg := config.Get()
	switch cfg.Provider {
	case config.ProviderOpenAICompatible:
		return "the OpenAI Completions API does not support images in tool results"
	case config.ProviderGemini:
		model := strings.ToLower(cfg.Model)
		if strings.HasPrefix(model, "gemini-3") {
			return ""
		}
		return "images in function responses require a Gemini 3 series model"
	default:
		return ""
	}
}

// ComputerUseBlockReason is a thin alias of ToolImageBlockReason kept for the
// computer-use tool call sites.
func ComputerUseBlockReason() string {
	return ToolImageBlockReason()
}

// lastCapture tracks the scaling between the last screenshot sent to the
// model (image space) and real screen pixels.
var lastCapture struct {
	sync.Mutex
	valid  bool
	imgW   int
	imgH   int
	scaleX float64
	scaleY float64
}

// computerActionMu serializes computer actions (defense in depth: the tool
// dispatcher already serializes tool execution).
var computerActionMu sync.Mutex

// closeComputerSessions releases platform resources held by the computer
// backend (on Wayland: the XDG portal session). Platform files assign it in
// their init function; it is a no-op elsewhere.
var closeComputerSessions = func() {}

// ClosePortalSession releases any desktop-portal session held by the computer
// backend. Safe to call on every platform.
func ClosePortalSession() { closeComputerSessions() }

// executeComputerTool is the platform-independent handler for the `computer`
// tool. Platform files only implement the low-level primitives.
func executeComputerTool(ctx context.Context, args map[string]interface{}) types.ToolResult {
	action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
	if action == "" {
		return textResult("error: 'action' is required (one of: screenshot, move, click, double_click, right_click, drag, scroll, type, key)")
	}

	if allowed, suggestion := askComputerUsePermission(ctx, describeComputerAction(action, args)); !allowed {
		if suggestion != "" {
			return textResult("error: user denied the computer action. Suggestion: " + suggestion)
		}
		return textResult("error: user denied the computer action")
	}

	computerActionMu.Lock()
	defer computerActionMu.Unlock()

	switch action {
	case "screenshot":
		return computerDoScreenshot()

	case "move":
		x, y, err := mappedPoint(args, "x", "y")
		if err != nil {
			return errResult(err)
		}
		if err := computerPlatformMove(x, y); err != nil {
			return errResult(err)
		}
		time.Sleep(computerActionDelay)
		return textResult(fmt.Sprintf("moved mouse to (%d,%d) in image space [screen (%d,%d)]", intArg(args, "x"), intArg(args, "y"), x, y))

	case "click", "double_click", "right_click":
		x, y, err := mappedPoint(args, "x", "y")
		if err != nil {
			return errResult(err)
		}
		button := computerButton(args, action)
		count := 1
		if action == "double_click" {
			count = 2
		}
		if err := computerPlatformClick(x, y, button, count); err != nil {
			return errResult(err)
		}
		time.Sleep(computerActionDelay)
		return textResult(fmt.Sprintf("clicked %s %d time(s) at image (%d,%d) [screen (%d,%d)]", button, count, intArg(args, "x"), intArg(args, "y"), x, y))

	case "drag":
		x1, y1, err := mappedPoint(args, "x", "y")
		if err != nil {
			return errResult(err)
		}
		x2, y2, err := mappedPoint(args, "x2", "y2")
		if err != nil {
			return errResult(err)
		}
		button := computerButton(args, action)
		if err := computerPlatformDrag(x1, y1, x2, y2, button); err != nil {
			return errResult(err)
		}
		time.Sleep(computerActionDelay)
		return textResult(fmt.Sprintf("dragged %s from image (%d,%d) to (%d,%d) [screen (%d,%d) -> (%d,%d)]", button, intArg(args, "x"), intArg(args, "y"), intArg(args, "x2"), intArg(args, "y2"), x1, y1, x2, y2))

	case "scroll":
		x, y, err := mappedPoint(args, "x", "y")
		if err != nil {
			return errResult(err)
		}
		direction := strings.ToLower(strings.TrimSpace(strArg(args, "direction")))
		if direction == "" {
			direction = "down"
		}
		switch direction {
		case "up", "down", "left", "right":
		default:
			return textResult("error: 'direction' must be one of: up, down, left, right")
		}
		amount := intArg(args, "amount")
		if amount <= 0 {
			amount = 3
		}
		if err := computerPlatformScroll(x, y, direction, amount); err != nil {
			return errResult(err)
		}
		time.Sleep(computerActionDelay)
		return textResult(fmt.Sprintf("scrolled %s by %d at image (%d,%d) [screen (%d,%d)]", direction, amount, intArg(args, "x"), intArg(args, "y"), x, y))

	case "type":
		text := strArg(args, "text")
		if text == "" {
			return textResult("error: 'text' is required for action 'type'")
		}
		if err := computerPlatformTypeText(text); err != nil {
			return errResult(err)
		}
		time.Sleep(computerActionDelay)
		return textResult(fmt.Sprintf("typed %d character(s): %q", len([]rune(text)), truncateForDisplay(text, 60)))

	case "key":
		raw := strings.TrimSpace(strArg(args, "keys"))
		if raw == "" {
			return textResult("error: 'keys' is required for action 'key' (e.g. \"enter\", \"ctrl+shift+t\")")
		}
		combo, err := parseKeyCombo(raw)
		if err != nil {
			return errResult(err)
		}
		if err := computerPlatformKey(combo); err != nil {
			return errResult(err)
		}
		time.Sleep(computerActionDelay)
		return textResult(fmt.Sprintf("pressed key combo %q", raw))

	default:
		return textResult(fmt.Sprintf("error: unknown computer action %q (one of: screenshot, move, click, double_click, right_click, drag, scroll, type, key)", action))
	}
}

// computerDoScreenshot captures the screen, downscales it if needed, encodes
// it as JPEG and returns it as an image attachment. It also records the
// image-space -> screen-space mapping used by every input action.
func computerDoScreenshot() types.ToolResult {
	img, err := computerPlatformCapture()
	if err != nil {
		return errResult(err)
	}
	b := img.Bounds()
	realW, realH := b.Dx(), b.Dy()
	if realW <= 0 || realH <= 0 {
		return textResult("error: screen capture returned an empty image")
	}

	shot := img
	if realW > maxScreenshotSide || realH > maxScreenshotSide {
		scale := float64(maxScreenshotSide) / float64(maxInt(realW, realH))
		nw := maxInt(1, int(math.Round(float64(realW)*scale)))
		nh := maxInt(1, int(math.Round(float64(realH)*scale)))
		shot = boxDownscale(img, nw, nh)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, shot, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return textResult("error: failed to encode screenshot: " + err.Error())
	}

	w, h := shot.Bounds().Dx(), shot.Bounds().Dy()

	cw, ch := computerPlatformCoordSpace(realW, realH)
	lastCapture.Lock()
	lastCapture.valid = true
	lastCapture.imgW = w
	lastCapture.imgH = h
	lastCapture.scaleX = cw / float64(w)
	lastCapture.scaleY = ch / float64(h)
	lastCapture.Unlock()

	text := fmt.Sprintf(
		"Screenshot captured: %dx%d px (real screen %dx%d). The image is attached. "+
			"Coordinates for move/click/drag/scroll must be in this image's space; they are mapped to real screen coordinates automatically. "+
			"Take a new screenshot after each action to verify the result.",
		w, h, int(math.Round(cw)), int(math.Round(ch)))

	return types.ToolResult{
		Text: text,
		Images: []types.ImageAttachment{{
			MIMEType: "image/jpeg",
			Data:     base64.StdEncoding.EncodeToString(buf.Bytes()),
			Width:    w,
			Height:   h,
			Name:     "screenshot.jpg",
		}},
	}
}

// mappedPoint reads a point in image space and maps it to real screen pixels
// using the mapping recorded by the last screenshot.
func mappedPoint(args map[string]interface{}, xKey, yKey string) (int, int, error) {
	if args[xKey] == nil || args[yKey] == nil {
		return 0, 0, fmt.Errorf("error: '%s' and '%s' are required (coordinates in the last screenshot's space)", xKey, yKey)
	}
	lastCapture.Lock()
	defer lastCapture.Unlock()
	if !lastCapture.valid {
		return 0, 0, fmt.Errorf("error: no screenshot yet — take one first with action='screenshot', then pass coordinates in that image's space")
	}
	ix, iy := intArg(args, xKey), intArg(args, yKey)
	if ix < 0 || iy < 0 || ix >= lastCapture.imgW || iy >= lastCapture.imgH {
		return 0, 0, fmt.Errorf("error: point (%d,%d) is outside the last screenshot (%dx%d)", ix, iy, lastCapture.imgW, lastCapture.imgH)
	}
	sx := int(math.Round(float64(ix) * lastCapture.scaleX))
	sy := int(math.Round(float64(iy) * lastCapture.scaleY))
	return sx, sy, nil
}

func computerButton(args map[string]interface{}, action string) string {
	switch action {
	case "right_click":
		return "right"
	}
	button := strings.ToLower(strings.TrimSpace(strArg(args, "button")))
	switch button {
	case "left", "right", "middle":
		return button
	case "":
		return "left"
	default:
		return "left"
	}
}

func describeComputerAction(action string, args map[string]interface{}) string {
	switch action {
	case "screenshot":
		return "take a screenshot of the screen"
	case "move":
		return fmt.Sprintf("move the mouse to image coordinates (%d,%d)", intArg(args, "x"), intArg(args, "y"))
	case "click", "double_click", "right_click":
		return fmt.Sprintf("click at image coordinates (%d,%d)", intArg(args, "x"), intArg(args, "y"))
	case "drag":
		return fmt.Sprintf("drag from (%d,%d) to (%d,%d)", intArg(args, "x"), intArg(args, "y"), intArg(args, "x2"), intArg(args, "y2"))
	case "scroll":
		return fmt.Sprintf("scroll %s at image coordinates (%d,%d)", strArg(args, "direction"), intArg(args, "x"), intArg(args, "y"))
	case "type":
		return fmt.Sprintf("type %q", truncateForDisplay(strArg(args, "text"), 60))
	case "key":
		return fmt.Sprintf("press keys %q", strArg(args, "keys"))
	default:
		return action
	}
}

// ── key combo parsing (shared, platform-independent) ─────────────────────────

// keyCombo is a parsed key sequence: zero or more modifiers plus an optional
// main key. A combo with no main key is a bare modifier tap (e.g. "super"
// opens the Overview on GNOME, "alt" alone taps Alt).
type keyCombo struct {
	Mods []string // canonical: "ctrl", "shift", "alt", "super"
	Key  string   // canonical name, a single character, or "" for a modifiers-only tap
}

var comboKeyNames = map[string]bool{
	"enter": true, "return": true, "tab": true, "esc": true, "escape": true,
	"space": true, "backspace": true, "delete": true, "del": true, "insert": true,
	"home": true, "end": true, "pageup": true, "pgup": true, "pagedown": true, "pgdn": true,
	"up": true, "down": true, "left": true, "right": true,
	"f1": true, "f2": true, "f3": true, "f4": true, "f5": true, "f6": true,
	"f7": true, "f8": true, "f9": true, "f10": true, "f11": true, "f12": true,
}

// comboModifiers maps every accepted modifier alias to its canonical name. A
// modifier token is recognized in any position, including when it stands alone:
// "super" or "ctrl+shift" are valid modifiers-only taps.
var comboModifiers = map[string]string{
	"ctrl": "ctrl", "control": "ctrl",
	"shift": "shift",
	"alt":   "alt", "option": "alt", "opt": "alt",
	"super": "super", "meta": "super", "win": "super", "windows": "super",
	"cmd": "super", "command": "super",
}

// parseKeyCombo parses strings like "ctrl+shift+t", "enter", "alt+F4" or a bare
// modifier such as "super".
func parseKeyCombo(s string) (keyCombo, error) {
	parts := strings.Split(s, "+")
	var c keyCombo
	for _, p := range parts {
		token := strings.ToLower(strings.TrimSpace(p))
		if token == "" {
			return c, fmt.Errorf("error: invalid key combo %q", s)
		}
		if mod, ok := comboModifiers[token]; ok {
			c.Mods = append(c.Mods, mod)
			continue
		}
		if c.Key != "" {
			return c, fmt.Errorf("error: key combo %q has more than one main key", s)
		}
		c.Key = token
	}
	if c.Key == "" && len(c.Mods) == 0 {
		return c, fmt.Errorf("error: missing key in combo %q", s)
	}
	if c.Key != "" && len([]rune(c.Key)) != 1 && !comboKeyNames[c.Key] {
		return c, fmt.Errorf("error: unknown key %q (use a single character, enter, tab, esc, space, backspace, delete, insert, home, end, pageup, pagedown, arrows, f1-f12, or a modifier: ctrl, shift, alt, super/meta/win/cmd)", c.Key)
	}
	return c, nil
}

// normalizeKeyName canonicalizes key aliases used by every platform backend.
func normalizeKeyName(name string) string {
	switch name {
	case "return":
		return "enter"
	case "escape":
		return "esc"
	case "del":
		return "delete"
	case "pgup":
		return "pageup"
	case "pgdn":
		return "pagedown"
	default:
		return name
	}
}

// ── image helpers ────────────────────────────────────────────────────────────

// boxDownscale scales src to dstW x dstH using an area-average (box) filter,
// which is well suited for downscaling UI screenshots.
func boxDownscale(src *image.RGBA, dstW, dstH int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		y0 := dy * sh / dstH
		y1 := (dy + 1) * sh / dstH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < dstW; dx++ {
			x0 := dx * sw / dstW
			x1 := (dx + 1) * sw / dstW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sbl, sa, n uint64
			for y := y0; y < y1; y++ {
				row := src.Pix[y*src.Stride:]
				for x := x0; x < x1; x++ {
					i := x * 4
					sr += uint64(row[i])
					sg += uint64(row[i+1])
					sbl += uint64(row[i+2])
					sa += uint64(row[i+3])
					n++
				}
			}
			o := dy*dst.Stride + dx*4
			dst.Pix[o] = uint8(sr / n)
			dst.Pix[o+1] = uint8(sg / n)
			dst.Pix[o+2] = uint8(sbl / n)
			dst.Pix[o+3] = uint8(sa / n)
		}
	}
	return dst
}

// ── small helpers ────────────────────────────────────────────────────────────

func textResult(s string) types.ToolResult {
	return types.ToolResult{Text: s}
}

func errResult(err error) types.ToolResult {
	return types.ToolResult{Text: "error: " + err.Error()}
}

func strArg(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return s
}

func intArg(args map[string]interface{}, key string) int {
	if args[key] == nil {
		return 0
	}
	switch v := args[key].(type) {
	case float64:
		return int(math.Round(v))
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateForDisplay(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}
