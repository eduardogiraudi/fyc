//go:build !linux

package tools

// GPU passthrough is only meaningful for the Linux bwrap sandbox: macOS already
// allows IOKit and Windows does not block the GPU, so there is nothing to bind.
func gpuDeviceNodes() []string { return nil }
