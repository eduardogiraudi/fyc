//go:build windows

package tools

import "strings"

// normalizeEnvKey folds an env var name to a canonical form. Windows env names
// are case-insensitive (PATH == Path), so filtering must be case-insensitive
// too or a secret such as AWS_SECRET_ACCESS_KEY could slip through as
// Aws_Secret_Access_Key.
func normalizeEnvKey(k string) string { return strings.ToUpper(k) }
