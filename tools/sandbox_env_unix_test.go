//go:build !windows

package tools

import (
	"strings"
	"testing"
)

// TestFilteredEnvIsCaseSensitiveOnUnix pins the platform-neutral refactor of
// filteredEnv: on case-sensitive platforms the match stays exact, so a
// differently-cased variable is NOT stripped.
func TestFilteredEnvIsCaseSensitiveOnUnix(t *testing.T) {
	t.Setenv("aws_secret_access_key", "keep")
	out := filteredEnv([]string{"AWS_SECRET_ACCESS_KEY"})
	found := false
	for _, e := range out {
		if strings.HasPrefix(e, "aws_secret_access_key=") {
			found = true
		}
	}
	if !found {
		t.Fatal("lowercase var must NOT be filtered on a case-sensitive platform")
	}
}
