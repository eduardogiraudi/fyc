package releasenotes

import (
	"strings"
	"testing"
	"time"
)

func TestValidVersion(t *testing.T) {
	for _, version := range []string{"0.0.0", "1.2.3", "1.2.3-rc.1", "1.2.3+build.01", "1.2.3-rc.1+abc"} {
		if !ValidVersion(version) {
			t.Errorf("rejected %q", version)
		}
	}
	for _, version := range []string{"", "v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3-a.00", "1.2.3\n", "../1.2.3"} {
		if ValidVersion(version) {
			t.Errorf("accepted %q", version)
		}
	}
}

func TestParseLog(t *testing.T) {
	commits, err := ParseLog("abcdef0123456789\x00feat(cli): add notes\n\nBREAKING CHANGE: remove old flow\n\x00\nfedcba9876543210\x00fix: correct notes\n\x00\n")
	if err != nil || len(commits) != 2 || !strings.Contains(commits[0].Message, "BREAKING CHANGE:") {
		t.Fatalf("commits = %#v, err = %v", commits, err)
	}
	if commits, err := ParseLog(""); err != nil || len(commits) != 0 {
		t.Fatalf("empty log: %#v, %v", commits, err)
	}
	for _, invalid := range []string{"abcdef0\x00message", "wrong\x00message\x00", "abcdef0\x00message\x00garbage"} {
		if _, err := ParseLog(invalid); err == nil {
			t.Errorf("accepted malformed log %q", invalid)
		}
	}
}

func TestDraft(t *testing.T) {
	commits := []Commit{
		{"abcdef012345", "feat(cli): add review"},
		{"123456789abc", "fix: correct typo"},
		{"23456789abcd", "perf: reduce allocations"},
		{"3456789abcde", "chore!: drop old platform\n\nBREAKING CHANGE: Go 1.25 is required"},
		{"456789abcdef", "docs: explain usage"},
		{"56789abcdef0", "okiiii"},
		{"6789abcdef01", "fix(api)!: remove endpoint"},
		{"789abcdef012", "feat: support flag\n\nBREAKING-CHANGE: replace old flag"},
		{"89abcdef0123", "revert: remove regression"},
	}
	draft, err := Draft("1.2.3", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), commits)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## [1.2.3] - 2026-10-13", "### Added", "cli: add review (`abcdef0`)", "### Fixed", "### Changed", "**Breaking:** drop old platform", "Go 1.25 is required", "TODO: rewrite or remove legacy commit: okiiii", "**Breaking:** api: remove endpoint", "replace old flag", "Revert: remove regression"} {
		if !strings.Contains(draft, want) {
			t.Errorf("missing %q in %s", want, draft)
		}
	}
	if strings.Contains(draft, "explain usage") {
		t.Error("nonbreaking maintenance commit included")
	}
	if Validate("1.2.3", draft) == nil {
		t.Error("legacy TODO draft was approvable")
	}
}

func TestEmptyDraftNeedsReview(t *testing.T) {
	for _, commits := range [][]Commit{nil, {{"abcdef0", "chore: bump version"}}} {
		draft, err := Draft("1.2.3", time.Now(), commits)
		if err != nil || !strings.Contains(draft, "TODO:") || Validate("1.2.3", draft) == nil {
			t.Fatalf("draft = %q, err = %v", draft, err)
		}
	}
}

const approvedSection = "## [1.2.3] - 2026-10-13\n\n### Added\n\n- Add editable release notes.\n"

func TestValidate(t *testing.T) {
	if err := Validate("1.2.3", approvedSection); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"", strings.ReplaceAll(approvedSection, "1.2.3", "1.2.4"),
		strings.ReplaceAll(approvedSection, "2026-10-13", "2026-02-30"),
		strings.ReplaceAll(approvedSection, "Added", "Features"),
		strings.ReplaceAll(approvedSection, "Add editable", "TODO: add editable"),
		"## [1.2.3] - 2026-10-13\n\n- No category.\n",
		approvedSection + "\n## [1.2.2]\n- Older notes.\n",
		approvedSection + "\n### Added\n- Duplicate category.\n",
		"## [1.2.3] - 2026-10-13\n\n### Changed\n",
	} {
		if Validate("1.2.3", invalid) == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestAssembleAndExtract(t *testing.T) {
	history := "## [1.2.2] - 2026-10-01\n\n### Fixed\n\n- Prior fix.\n\n## [1.0.0]\n- Initial release.\n"
	for _, prefix := range []string{"# Changelog\n\n", "# Changelog\n\nIntro.\n\n## [Unreleased]\n\n### Added\n\n- Future feature.\n\n"} {
		assembled, err := Assemble("1.2.3", approvedSection, prefix+history)
		if err != nil {
			t.Fatal(err)
		}
		if assembled != prefix+approvedSection+"\n"+history {
			t.Errorf("history was changed:\n%s", assembled)
		}
		extracted, err := Extract("1.2.3", assembled)
		if err != nil || extracted != approvedSection {
			t.Fatalf("extracted = %q, err = %v", extracted, err)
		}
		if _, err := Assemble("1.2.3", approvedSection, assembled); err == nil {
			t.Fatal("duplicate version accepted")
		}
		if _, err := Extract("1.2.3", assembled+approvedSection); err == nil {
			t.Fatal("duplicate section extracted")
		}
	}
	if _, err := Extract("1.2.3", history); err == nil {
		t.Fatal("missing version extracted")
	}
	last, err := Extract("1.0.0", history)
	if err != nil || last != "## [1.0.0]\n- Initial release.\n" {
		t.Fatalf("last = %q, err = %v", last, err)
	}
	if _, err := Assemble("1.0.0", strings.ReplaceAll(approvedSection, "1.2.3", "1.0.0"), history); err == nil {
		t.Fatal("undated historical duplicate accepted")
	}
}

func TestDraftMultipleEntries(t *testing.T) {
	message := "feat(cli): add preview\n\nContext for the new preview.\n\nfix(api): repair validation\n\nfeat(config): support profiles\nfix(cli): repair cancellation\n\ndocs: document workflow\n"
	draft, err := Draft("1.2.3", time.Now(), []Commit{{"abcdef012345", message}})
	if err != nil {
		t.Fatal(err)
	}
	added, changed := strings.SplitN(draft, "### Fixed", 2)[0], strings.SplitN(draft, "### Fixed", 2)[1]
	for _, want := range []string{"cli: add preview", "config: support profiles"} {
		if !strings.Contains(added, want) {
			t.Errorf("missing Added entry %q: %s", want, draft)
		}
	}
	for _, want := range []string{"api: repair validation", "cli: repair cancellation"} {
		if !strings.Contains(changed, want) {
			t.Errorf("missing Fixed entry %q: %s", want, draft)
		}
	}
	if strings.Count(draft, "(`abcdef0`)") != 4 || strings.Contains(draft, "Context for") || strings.Contains(draft, "document workflow") {
		t.Fatalf("incorrect entries: %s", draft)
	}
	if err := Validate("1.2.3", draft); err != nil {
		t.Fatal(err)
	}
}

func TestDraftEntryBreakingFooter(t *testing.T) {
	message := "feat: add preview\n\nfix(api): remove invalid fallback\n\nBREAKING CHANGE: clients must supply a token\n\nfix(cli): correct cancellation\n\nchore!: drop old platform\n\nBREAKING-CHANGE: Go 1.25 is required\n"
	draft, err := Draft("1.2.3", time.Now(), []Commit{{"abcdef012345", message}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- add preview (`abcdef0`)", "**Breaking:** api: remove invalid fallback — clients must supply a token", "- cli: correct cancellation (`abcdef0`)", "**Breaking:** drop old platform — Go 1.25 is required"} {
		if !strings.Contains(draft, want) {
			t.Errorf("missing %q: %s", want, draft)
		}
	}
	if strings.Count(draft, "**Breaking:**") != 2 {
		t.Fatalf("footer leaked across entries: %s", draft)
	}
}

func TestDraftIgnoresTypedExamples(t *testing.T) {
	message := "feat: add examples\n\nBody explanation.\n    fix: indented example\n\n```text\nfix: fenced example\n```\n\n~~~text\nfeat: another fenced example\n~~~\n\nfix: correct actual bug\n"
	draft, err := Draft("1.2.3", time.Now(), []Commit{{"abcdef012345", message}})
	if err != nil || strings.Count(draft, "(`abcdef0`)") != 2 || strings.Contains(draft, "fenced example") || strings.Contains(draft, "indented example") {
		t.Fatalf("draft = %s, err = %v", draft, err)
	}
}

func TestDraftLegacySubjectWithTypedBody(t *testing.T) {
	draft, err := Draft("1.2.3", time.Now(), []Commit{{"abcdef012345", "old summary\n\nfix: repair actual bug\n"}})
	if err != nil || !strings.Contains(draft, "TODO: rewrite or remove legacy commit: old summary") || !strings.Contains(draft, "### Fixed\n\n- repair actual bug") {
		t.Fatalf("draft = %s, err = %v", draft, err)
	}
}
