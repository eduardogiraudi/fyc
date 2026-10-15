package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, dir, name string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func TestReadMediaAttachesImage(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "logo.png", 8, 6)

	res := executeReadMediaTool(map[string]any{"path": "logo.png"}, dir, nil)
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 image, got %d (text: %s)", len(res.Images), res.Text)
	}
	img := res.Images[0]
	if img.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", img.MIMEType)
	}
	if img.Name != "logo.png" {
		t.Errorf("Name = %q, want logo.png", img.Name)
	}
	if img.Width != 8 || img.Height != 6 {
		t.Errorf("size = %dx%d, want 8x6", img.Width, img.Height)
	}
	if _, err := base64.StdEncoding.DecodeString(img.Data); err != nil {
		t.Errorf("data is not valid base64: %v", err)
	}
}

// The MIME must come from the decoded content, not from the extension.
func TestReadMediaMIMEComesFromContent(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "mislabelled.jpg", 4, 4)

	res := executeReadMediaTool(map[string]any{"path": "mislabelled.jpg"}, dir, nil)
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 image, got %d (text: %s)", len(res.Images), res.Text)
	}
	if got := res.Images[0].MIMEType; got != "image/png" {
		t.Errorf("MIMEType = %q, want the content format image/png", got)
	}
}

func TestReadMediaDownscalesLargePNG(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "big.png", maxScreenshotSide+200, 100)

	res := executeReadMediaTool(map[string]any{"path": "big.png"}, dir, nil)
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 image, got %d (text: %s)", len(res.Images), res.Text)
	}
	img := res.Images[0]
	if got := maxInt(img.Width, img.Height); got != maxScreenshotSide {
		t.Errorf("longest side = %d, want %d", got, maxScreenshotSide)
	}
	if img.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png (a downscaled PNG stays PNG)", img.MIMEType)
	}
	if !strings.Contains(res.Text, "downscaled") {
		t.Errorf("text should mention the downscale: %q", res.Text)
	}
}

func TestReadMediaRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake.png"), []byte("not an image at all"), 0644); err != nil {
		t.Fatal(err)
	}

	if res := executeReadMediaTool(map[string]any{"path": "notes.txt"}, dir, nil); !strings.HasPrefix(res.Text, "error:") || len(res.Images) != 0 {
		t.Errorf("txt should be rejected, got %q", res.Text)
	}
	if res := executeReadMediaTool(map[string]any{"path": "fake.png"}, dir, nil); !strings.HasPrefix(res.Text, "error:") || len(res.Images) != 0 {
		t.Errorf("undecodable .png should be rejected, got %q", res.Text)
	}
	if res := executeReadMediaTool(map[string]any{"path": "missing.png"}, dir, nil); res.Text == "" || len(res.Images) != 0 {
		t.Errorf("missing file should return an error text, got %q", res.Text)
	}
	if res := executeReadMediaTool(map[string]any{}, dir, nil); !strings.HasPrefix(res.Text, "error:") {
		t.Errorf("missing path arg should error, got %q", res.Text)
	}
	if res := executeReadMediaTool(map[string]any{"path": "../../etc/passwd.png"}, dir, nil); res.Text == "" || len(res.Images) != 0 {
		t.Errorf("path traversal should be denied, got %q", res.Text)
	}
}
