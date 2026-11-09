//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"sort"
)

// gpuDeviceGlobs lists the host device nodes needed for GPU compute/rendering.
// Deliberately narrow: /dev/dri/card* (display / KMS control) is NOT included —
// only render nodes, which are enough for CUDA, Vulkan, OpenCL and ROCm compute.
var gpuDeviceGlobs = []string{
	"/dev/dri/renderD*", // Mesa (Intel/AMD/Nouveau), Vulkan, OpenCL
	"/dev/nvidia*",      // NVIDIA proprietary: nvidiactl, nvidia0.., nvidia-uvm, nvidia-caps
	"/dev/kfd",          // AMD ROCm compute
}

// gpuDeviceNodes returns the GPU device nodes that exist on this host, sorted.
func gpuDeviceNodes() []string { return globExisting(gpuDeviceGlobs) }

func globExisting(patterns []string) []string {
	var out []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if _, err := os.Stat(m); err == nil {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// gpuBindArgs builds the bwrap arguments that bind each node read-write into the
// sandbox (device nodes need rw to be usable; bwrap's --dev-bind keeps them
// usable inside the new mount namespace).
func gpuBindArgs(nodes []string) []string {
	args := make([]string, 0, len(nodes)*3)
	for _, n := range nodes {
		args = append(args, "--dev-bind", n, n)
	}
	return args
}
