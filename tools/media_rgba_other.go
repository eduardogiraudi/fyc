//go:build !linux

package tools

import (
	"image"
	"image/draw"
)

// toRGBA converts any decoded image to *image.RGBA so boxDownscale can use it.
// On Linux the identical helper is provided by computer_wayland.go.
func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}
