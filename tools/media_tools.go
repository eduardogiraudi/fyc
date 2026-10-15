package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"

	"fuckyourcli/types"
)

// read_media reads an image file from the workspace and returns it as an image
// attachment on the tool result, so image-capable providers can actually see
// it. Like computer use it is disabled by default and hidden from the model
// when the active provider cannot ingest images inside tool results
// (see ToolImageBlockReason).
const (
	// maxMediaBytes caps the raw file size read from disk (anti-OOM).
	maxMediaBytes = 12 << 20 // 12 MiB
	// maxMediaPixels caps the decoded pixel count (anti decompression-bomb).
	maxMediaPixels = 50_000_000
)

// mediaMIME maps the accepted extensions to their MIME type. It is only a cheap
// first gate (so a .txt is rejected with a clear message) and the source of the
// "supported formats" list: the authoritative MIME is derived from the decoded
// content (formatMIME), never from the extension alone — a PNG renamed to .jpg
// must still be announced as image/png.
var mediaMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
}

// formatMIME maps the format names returned by image.Decode/DecodeConfig
// ("png", "jpeg", "gif") to their MIME type.
var formatMIME = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
}

// executeReadMediaTool handles the "read_media" case.
func executeReadMediaTool(args map[string]any, restrictedRoot string, globalIgnore *IgnoreManager) types.ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return textResult("error: path argument required")
	}

	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := mediaMIME[ext]; !ok {
		return textResult("error: unsupported media type '" + ext +
			"' (supported: png, jpg, jpeg, gif). For text files use read.")
	}

	root, err := getRoot(restrictedRoot)
	if err != nil {
		return errResult(err)
	}
	defer root.Close()

	rel, err := validatePathInRoot(path, restrictedRoot, globalIgnore)
	if err != nil {
		return textResult(err.Error())
	}

	f, err := root.Open(rel)
	if err != nil {
		return textResult(err.Error())
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxMediaBytes+1))
	if err != nil {
		return errResult(err)
	}
	if len(data) > maxMediaBytes {
		return textResult(fmt.Sprintf("error: file exceeds the %d MiB limit", maxMediaBytes>>20))
	}

	// Read only the header first: DecodeConfig yields the real dimensions (and
	// format) without decompressing the whole image, so a small-but-highly
	// compressed file cannot blow up memory before the pixel cap is enforced.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return textResult("error: not a decodable image: " + err.Error())
	}
	w, h := cfg.Width, cfg.Height
	if w <= 0 || h <= 0 {
		return textResult("error: image has an empty size")
	}
	if w*h > maxMediaPixels {
		return textResult(fmt.Sprintf("error: image is too large (%dx%d px)", w, h))
	}

	// The MIME comes from the detected format, not from the extension.
	mime, ok := formatMIME[format]
	if !ok {
		return textResult("error: unsupported image format '" + format + "'")
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return textResult("error: not a decodable image: " + err.Error())
	}

	// name is forwarded to providers that carry a display label (e.g. Gemini
	// inlineData.displayName).
	name := filepath.Base(path)

	// Pass the original bytes through untouched when the image already fits the
	// maximum side: no re-encode means full fidelity. Otherwise downscale it.
	outData, outMIME := data, mime
	outW, outH := w, h
	note := ""
	if maxInt(w, h) > maxScreenshotSide {
		scale := float64(maxScreenshotSide) / float64(maxInt(w, h))
		nw := maxInt(1, int(math.Round(float64(w)*scale)))
		nh := maxInt(1, int(math.Round(float64(h)*scale)))
		scaled := boxDownscale(toRGBA(img), nw, nh)

		var buf bytes.Buffer
		if mime == "image/png" {
			err = png.Encode(&buf, scaled) // preserve alpha for line art/diagrams
		} else {
			err = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: jpegQuality})
			outMIME = "image/jpeg"
		}
		if err != nil {
			return textResult("error: failed to encode image: " + err.Error())
		}
		outData, outW, outH = buf.Bytes(), nw, nh
		note = fmt.Sprintf(" (downscaled from %dx%d)", w, h)
	}

	return types.ToolResult{
		Text: fmt.Sprintf("Image %s attached: %s, %dx%d px, %d KB%s.",
			name, outMIME, outW, outH, len(outData)/1024, note),
		Images: []types.ImageAttachment{{
			MIMEType: outMIME,
			Data:     base64.StdEncoding.EncodeToString(outData),
			Width:    outW,
			Height:   outH,
			Name:     name,
		}},
	}
}
