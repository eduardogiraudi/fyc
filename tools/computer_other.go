//go:build !linux && !windows && !darwin

package tools

import (
	"fmt"
	"image"
)

// Stub backend: computer use is not available on this platform.

var errComputerUnsupported = fmt.Errorf("computer use is not supported on this platform (supported: linux/X11, linux/Wayland, windows, macOS)")

func computerPlatformCapture() (*image.RGBA, error) { return nil, errComputerUnsupported }

func computerPlatformCoordSpace(imgW, imgH int) (float64, float64) {
	return float64(imgW), float64(imgH)
}

func computerPlatformMove(x, y int) error { return errComputerUnsupported }

func computerPlatformClick(x, y int, button string, count int) error { return errComputerUnsupported }

func computerPlatformDrag(x1, y1, x2, y2 int, button string) error { return errComputerUnsupported }

func computerPlatformScroll(x, y int, direction string, amount int) error {
	return errComputerUnsupported
}

func computerPlatformTypeText(text string) error { return errComputerUnsupported }

func computerPlatformKey(combo keyCombo) error { return errComputerUnsupported }
