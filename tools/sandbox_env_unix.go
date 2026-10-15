//go:build !windows

package tools

// normalizeEnvKey is the identity on case-sensitive platforms: env var names
// are matched verbatim.
func normalizeEnvKey(k string) string { return k }
