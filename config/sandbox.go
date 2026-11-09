package config

// SandboxConfig holds the opt-out switches of the `sh` command sandbox.
type SandboxConfig struct {
	// GPU exposes the host GPU device nodes to the Linux (bwrap) sandbox. It is a
	// pointer so that "absent" (nil) can mean the default, which is ON; only an
	// explicit `"gpu": false` turns it off. Ignored on macOS and Windows.
	GPU *bool `json:"gpu,omitempty"`
}

// GPUEnabled reports whether GPU device nodes are exposed inside the sandbox.
// Default: true.
func (s SandboxConfig) GPUEnabled() bool {
	return s.GPU == nil || *s.GPU
}

// SetSandboxGPU enables or disables GPU passthrough in the `sh` sandbox.
func SetSandboxGPU(enabled bool) {
	config.Sandbox.GPU = &enabled
}
