package updater

import (
	"strconv"
	"strings"
)

// semver is a loose x.y.z version. Anything unparsable (a git hash, "dev") is
// reported as *not* valid so the caller can treat it as "older than everything".
type semver struct {
	nums [3]int
	pre  string
	ok   bool
}

func parseSemver(s string) semver {
	var v semver
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return v
	}
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) > len(v.nums) {
		parts = parts[:len(v.nums)]
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}
		}
		v.nums[i] = n
	}
	v.ok = true
	return v
}

// compare returns -1, 0 or 1. A version without a pre-release suffix wins over
// one with it (1.0.0 > 1.0.0-rc1).
func (a semver) compare(b semver) int {
	for i := range a.nums {
		switch {
		case a.nums[i] < b.nums[i]:
			return -1
		case a.nums[i] > b.nums[i]:
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	case a.pre < b.pre:
		return -1
	default:
		return 1
	}
}

// isNewer reports whether latest is strictly newer than current. An unparsable
// current version (a dev build) is always considered older, so those builds are
// offered the published release.
func isNewer(latest, current string) bool {
	l := parseSemver(latest)
	if !l.ok {
		return false
	}
	c := parseSemver(current)
	if !c.ok {
		return true
	}
	return l.compare(c) > 0
}
