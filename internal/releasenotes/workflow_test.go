package releasenotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// All Git calls resolve to this fixture, never to the host's Git executable.
const mockGit = `#!/bin/bash
set -eu
printf 'git %s\n' "$*" >> "$MOCK_LOG"
case "$1" in
  var) printf '%s\n' '"'"$MOCK_EDITOR"'" --wait' ;;
  rev-parse)
    if [ "$2" = --show-toplevel ]; then pwd
    elif [ -f changed-head ]; then echo changed
    else echo abcdef0123456789; fi ;;
  symbolic-ref) echo "${MOCK_BRANCH:-master}" ;;
  status)
    if [ "${MOCK_DIRTY:-0}" = 1 ] || [ -f dirty ]; then echo ' M unexpected'; fi ;;
  show-ref) [ "${MOCK_LOCAL_TAG:-0}" = 1 ] ;;
  ls-remote)
    [ "${MOCK_REMOTE_FAIL:-0}" != 1 ] || exit 1
    if [ "${MOCK_REMOTE_TAG:-0}" = 1 ]; then echo 'abc refs/tags/v1.2.3'; fi ;;
  describe)
    [ "${MOCK_NO_TAG:-0}" != 1 ] || exit 1
    echo v1.2.2 ;;
  log)
    if [ "${MOCK_LEGACY:-0}" = 1 ]; then printf 'abcdef0123456789\0okiiii\n\0\n'
    elif [ "${MOCK_MULTI:-0}" = 1 ]; then printf 'abcdef0123456789\0feat(cli): add reviewed releases\n\nfix(cli): repair approval\n\0\n'
    else printf 'abcdef0123456789\0feat(cli): add reviewed releases\n\0\n'; fi ;;
  diff)
    case "$*" in
      *--quiet*)
        if [ "${MOCK_EMPTY:-0}" = 1 ] || [ "${MOCK_SCRIPT:-release}" = release ]; then exit 0; else exit 1; fi ;;
      *--name-only*) echo "${MOCK_FILE:-main.go}" ;;
      *--stat*) echo ' main.go | 2 +-' ;;
      *--binary*) if [ -f changed-index ]; then echo changed; else echo original; fi ;;
    esac ;;
  -c) cp "${!#}" "$MOCK_COMMITTED"; [ "${MOCK_COMMIT_FAIL:-0}" != 1 ] ;;
  commit) [ "${MOCK_COMMIT_FAIL:-0}" != 1 ] ;;
  push) [ "${MOCK_PUSH_FAIL:-0}" != 1 ] ;;
  add|tag) : ;;
  *) echo "Unexpected mock Git call: $*" >&2; exit 2 ;;
esac
`

const mockEditor = `#!/bin/bash
set -eu
[ "$1" = --wait ]
printf 'editor\n' >> "$MOCK_LOG"
if [ -f editor-count ]; then count=$(cat editor-count); else count=0; fi
count=$((count + 1))
echo "$count" > editor-count
cp "$2" "$MOCK_CAPTURE"
case "${MOCK_EDITOR_MODE:-keep}" in
  fail) exit 1 ;;
  empty) : > "$2" ;;
  message) printf '%s\n' "${MOCK_MESSAGE:-feat(cli): add release workflow}" > "$2" ;;
  notes) printf '## [1.2.3] - 2026-10-13\n\n### Added\n\n- Approved English notes.\n' > "$2" ;;
  second)
    if [ "$count" -gt 1 ]; then
      printf '## [1.2.3] - 2026-10-13\n\n### Fixed\n\n- Revised English notes.\n' > "$2"
    fi ;;
  dirty) touch dirty ;;
  head) touch changed-head ;;
  index) touch changed-index; echo 'feat: add workflow' > "$2" ;;
esac
`

const mockGo = `#!/bin/bash
set -eu
case "$1" in
  build) cp "$MOCK_HELPER" "$4" ;;
  run)
    echo refresh-codex >> "$MOCK_LOG"
    echo 9.9.9 > codexversion/VERSION ;;
  *) exit 2 ;;
esac
`

func TestWorkflows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash maintainer workflows; notes logic is tested on all platforms")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash unavailable")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "release-notes")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", helper, "./cmd/release-notes")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}

	run := func(t *testing.T, script, input string, settings map[string]string, args ...string) (string, string, string, string, error) {
		t.Helper()
		dir := t.TempDir()
		write := func(name, content string, mode os.FileMode) {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), mode); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"push", "release.sh", "scripts/workflow-common.sh"} {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			write(name, string(data), 0o755)
		}
		original := "# Changelog\n\n## [1.2.2] - 2026-10-01\n\n### Fixed\n\n- Historical notes.\n"
		if settings["MOCK_DUPLICATE"] == "1" {
			original += "\n## [1.2.3]\n- Future placeholder.\n"
		}
		write("CHANGELOG.md", original, 0o644)
		write("version", "1.2.2\n", 0o644)
		write("codexversion/VERSION", "1.0.0\n", 0o644)
		write("models/catalog.json", "{}\n", 0o644)
		write("bin/git", mockGit, 0o755)
		write("bin/go", mockGo, 0o755)
		write("bin/editor with spaces", mockEditor, 0o755)
		write("update_catalog.sh", "#!/bin/bash\necho refresh-catalog >> \"$MOCK_LOG\"\nexit 1\n", 0o755)
		command := exec.Command(bash, append([]string{filepath.Join(dir, script)}, args...)...)
		command.Dir = dir
		command.Stdin = strings.NewReader(input)
		command.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "MOCK_HELPER="+helper, "MOCK_LOG="+filepath.Join(dir, "calls"), "MOCK_EDITOR="+filepath.Join(dir, "bin/editor with spaces"), "MOCK_CAPTURE="+filepath.Join(dir, "capture"), "MOCK_COMMITTED="+filepath.Join(dir, "committed"), "MOCK_SCRIPT="+strings.TrimSuffix(script, ".sh"))
		for key, value := range settings {
			command.Env = append(command.Env, key+"="+value)
		}
		output, err := command.CombinedOutput()
		read := func(name string) string {
			data, _ := os.ReadFile(filepath.Join(dir, name))
			return string(data)
		}
		if script == "release.sh" && !strings.Contains(read("calls"), "git commit") {
			if read("CHANGELOG.md") != original || read("version") != "1.2.2\n" || read("codexversion/VERSION") != "1.0.0\n" {
				t.Fatal("unapproved release modified artifacts")
			}
		}
		if script == "push" && strings.Contains(read("calls"), "git -c") {
			committed := read("committed")
			if strings.Contains(committed, "<what was") || strings.Contains(committed, "# Write") {
				t.Fatalf("committed placeholders or instructions: %s", committed)
			}
			if expected, ok := settings["MOCK_EXPECT_COMMITTED"]; ok && strings.TrimSpace(committed) != expected {
				t.Fatalf("committed = %q, want %q", committed, expected)
			}
		}
		return string(output), read("calls"), read("CHANGELOG.md"), read("capture"), err
	}

	for _, tc := range []struct {
		name, input string
		settings    map[string]string
		want        string
		fail        bool
	}{
		{"cancel", "n\n", nil, "Release cancelled", false},
		{"eof", "", nil, "Release cancelled", false},
		{"editor-failure", "y\n", map[string]string{"MOCK_EDITOR_MODE": "fail"}, "", true},
		{"legacy-todo", "n\n", map[string]string{"MOCK_LEGACY": "1"}, "rewrite or remove every TODO", true},
		{"dirty-tree", "y\n", map[string]string{"MOCK_DIRTY": "1"}, "Commit or stash", true},
		{"wrong-branch", "y\n", map[string]string{"MOCK_BRANCH": "feature"}, "master", true},
		{"local-tag", "y\n", map[string]string{"MOCK_LOCAL_TAG": "1"}, "already exists", true},
		{"remote-tag", "y\n", map[string]string{"MOCK_REMOTE_TAG": "1"}, "already exists on origin", true},
		{"remote-failure", "y\n", map[string]string{"MOCK_REMOTE_FAIL": "1"}, "", true},
		{"duplicate-section", "y\n", map[string]string{"MOCK_DUPLICATE": "1"}, "already contains", true},
		{"changed-tree", "y\n", map[string]string{"MOCK_EDITOR_MODE": "dirty"}, "Working tree changed", true},
		{"changed-head", "y\n", map[string]string{"MOCK_EDITOR_MODE": "head"}, "HEAD changed", true},
	} {
		t.Run("release-"+tc.name, func(t *testing.T) {
			output, calls, _, capture, err := run(t, "release.sh", tc.input, tc.settings, "1.2.3")
			if (err != nil) != tc.fail || !strings.Contains(output, tc.want) {
				t.Fatalf("err = %v, output = %s", err, output)
			}
			for _, forbidden := range []string{"git commit", "git tag", "git push", "refresh-"} {
				if strings.Contains(calls, forbidden) {
					t.Fatalf("unapproved release performed %s: %s", forbidden, calls)
				}
			}
			if strings.Contains(capture, "Historical notes") {
				t.Fatal("editor displayed old changelog")
			}
		})
	}

	for _, tc := range []struct {
		name, input, want string
		settings          map[string]string
	}{
		{"approve", "y\n", "cli: add reviewed releases", nil},
		{"revise", "e\ny\n", "Revised English notes", map[string]string{"MOCK_EDITOR_MODE": "second"}},
		{"legacy-rewritten", "y\n", "Approved English notes", map[string]string{"MOCK_LEGACY": "1", "MOCK_EDITOR_MODE": "notes"}},
		{"first-release", "y\n", "cli: add reviewed releases", map[string]string{"MOCK_NO_TAG": "1"}},
		{"multiple-entries", "y\n", "### Fixed\n\n- cli: repair approval", map[string]string{"MOCK_MULTI": "1"}},
	} {
		t.Run("release-"+tc.name, func(t *testing.T) {
			output, calls, changelog, capture, err := run(t, "release.sh", tc.input, tc.settings, "1.2.3")
			if err != nil {
				t.Fatalf("%v: %s\n%s", err, output, calls)
			}
			for _, want := range []string{"git add -- version CHANGELOG.md codexversion/VERSION models/catalog.json", "git commit -m chore(release): v1.2.3", "git tag -a v1.2.3", "git push --atomic origin HEAD:refs/heads/master refs/tags/v1.2.3"} {
				if !strings.Contains(calls, want) {
					t.Errorf("missing %q: %s", want, calls)
				}
			}
			if !strings.Contains(changelog, tc.want) || !strings.Contains(changelog, "Historical notes") || strings.Contains(capture, "Historical notes") {
				t.Fatalf("changelog = %s, editor = %s", changelog, capture)
			}
			if strings.Index(calls, "editor") > strings.Index(calls, "refresh-codex") {
				t.Fatal("refresh occurred before review")
			}
		})
	}

	t.Run("release-commit-failure", func(t *testing.T) {
		output, calls, _, _, err := run(t, "release.sh", "y\n", map[string]string{"MOCK_COMMIT_FAIL": "1"}, "1.2.3")
		if err == nil || strings.Contains(calls, "git tag") || strings.Contains(calls, "git push") {
			t.Fatalf("err = %v, output = %s, calls = %s", err, output, calls)
		}
	})

	for _, tc := range []struct {
		name               string
		settings           map[string]string
		fail, commit, push bool
	}{
		{"success", map[string]string{"MOCK_EDITOR_MODE": "message"}, false, true, true},
		{"multiple", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat: add preview\n\nfix: repair approval\n\nfix(api): correct validation", "MOCK_EXPECT_COMMITTED": "feat: add preview\n\nfix: repair approval\n\nfix(api): correct validation"}, false, true, true},
		{"adjacent", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat: add preview\nfix: repair approval", "MOCK_EXPECT_COMMITTED": "feat: add preview\n\nfix: repair approval"}, false, true, true},
		{"feature-only", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat: add preview\n\nfix: <what was fixed>", "MOCK_EXPECT_COMMITTED": "feat: add preview"}, false, true, true},
		{"fix-only", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat: <what was added>\n\nfix: repair approval", "MOCK_EXPECT_COMMITTED": "fix: repair approval"}, false, true, true},
		{"blank-unused", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat:\n\nfix: repair approval", "MOCK_EXPECT_COMMITTED": "fix: repair approval"}, false, true, true},
		{"invalid-body-entry", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat: add preview\n\nfix:broken syntax"}, true, false, false},
		{"fenced-example", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "feat: add preview\n\n```text\nfix:broken example\n```"}, false, true, true},
		{"empty", map[string]string{"MOCK_EDITOR_MODE": "empty"}, true, false, false},
		{"placeholder", nil, true, false, false},
		{"invalid", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_MESSAGE": "okiiii"}, true, false, false},
		{"no-changes", map[string]string{"MOCK_EMPTY": "1"}, true, false, false},
		{"commit-fails", map[string]string{"MOCK_EDITOR_MODE": "message", "MOCK_COMMIT_FAIL": "1"}, true, true, false},
		{"editor-fails", map[string]string{"MOCK_EDITOR_MODE": "fail"}, true, false, false},
		{"index-changes", map[string]string{"MOCK_EDITOR_MODE": "index"}, true, false, false},
	} {
		t.Run("push-"+tc.name, func(t *testing.T) {
			output, calls, _, capture, err := run(t, "push", "n\n", tc.settings)
			if tc.name == "placeholder" && !strings.HasPrefix(capture, "feat: <what was added>\n\nfix: <what was fixed>\n") {
				t.Fatalf("missing default feat/fix entries: %s", capture)
			}
			if (err != nil) != tc.fail || strings.Contains(calls, "git -c") != tc.commit || strings.Contains(calls, "git push") != tc.push {
				t.Fatalf("err = %v, output = %s, calls = %s", err, output, calls)
			}
		})
	}
	t.Run("push-prefilled", func(t *testing.T) {
		output, calls, _, capture, err := run(t, "push", "", nil, "fix(cli): correct workflow")
		if err != nil || !strings.Contains(calls, "editor") || !strings.HasPrefix(capture, "fix(cli): correct workflow") || !strings.Contains(calls, "git push") {
			t.Fatalf("err = %v, output = %s, calls = %s, capture = %s", err, output, calls, capture)
		}
	})
}
