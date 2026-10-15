package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillLocationsUseFycDirectories(t *testing.T) {
	home, root, configDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("FYC_CONFIG_DIR", configDir)
	want := []string{
		filepath.Join(configDir, "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(root, ".claude", "skills"),
		filepath.Join(root, ".fyc", "skills"),
		filepath.Join(root, ".agents", "skills"),
	}
	got := getSkillLocations(root)
	if len(got) != len(want) {
		t.Fatalf("locations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("location %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, dir := range []string{want[0], want[4]} {
		skill := filepath.Join(dir, "test-skill")
		if err := os.MkdirAll(skill, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\ndescription: test\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := DiscoverSkills(root)["test-skill"]; got != "test" {
		t.Fatalf("discovered description = %q, want test", got)
	}
	if got, want := FindSkillPath("test-skill", root), filepath.Join(want[4], "test-skill", "SKILL.md"); got != want {
		t.Fatalf("FindSkillPath() = %q, want project override %q", got, want)
	}
}

func TestReadUserPreferencesUsesConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	t.Setenv("FYC_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PREFERENCES.txt"), []byte("concise"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := executeReadUserPreferencesTool(); got != "concise" {
		t.Fatalf("preferences = %q, want concise", got)
	}
}

func TestUpdateUserPreferencesUsesConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	t.Setenv("FYC_CONFIG_DIR", dir)
	previous := getInteractor()
	SetInteractor(NewHeadlessInteractor("auto"))
	t.Cleanup(func() { SetInteractor(previous) })
	for _, args := range []map[string]any{
		{"old_str": "", "new_str": "concise"},
		{"old_str": "concise", "new_str": "detailed"},
	} {
		if got := executeUpdateUserPreferencesTool(context.Background(), args); got != "preferences updated" {
			t.Fatalf("update = %q, want preferences updated", got)
		}
		data, err := os.ReadFile(filepath.Join(dir, "PREFERENCES.txt"))
		if err != nil || string(data) != args["new_str"] {
			t.Fatalf("preferences = %q, %v; want %q", data, err, args["new_str"])
		}
	}
}

func TestInstallerConfigPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell installers")
	}
	for _, script := range []string{"setup.sh", "install.sh", "install_mac.sh"} {
		t.Run(script, func(t *testing.T) {
			shell := "bash"
			if script == "setup.sh" {
				shell = "sh"
			}
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			raw, err := os.ReadFile(filepath.Join("..", script))
			if err != nil {
				t.Fatal(err)
			}
			source := string(raw)
			section := func(start, end string) string {
				t.Helper()
				i := strings.Index(source, start)
				if i < 0 {
					t.Fatalf("%s: section %q missing", script, start)
				}
				j := strings.Index(source[i:], end)
				if j < 0 {
					t.Fatalf("%s: section end %q missing", script, end)
				}
				return source[i : i+j]
			}
			var platform, assets string
			switch script {
			case "setup.sh":
				platform = section("case \"$(uname -s)\" in", "if [ -z \"$INSTALL_DIR\" ]; then")
				assets = section("# BEGIN fyc-config-dir", "# END fyc-config-dir")
			case "install.sh", "install_mac.sh":
				assets = section("# BEGIN fyc-config-dir", "# END fyc-config-dir")
			}
			cases := []struct{ name, os, xdg, override, suffix string }{
				{"linux_default", "Linux", "", "", ".config/fyc"},
				{"linux_xdg", "Linux", "absolute", "", ""},
				{"linux_relative_xdg", "Linux", "relative/config", "", ".config/fyc"},
				{"linux_override", "Linux", "relative/config", "override", ""},
				{"mac_default", "Darwin", "absolute", "", "Library/Application Support/fyc"},
				{"mac_override", "Darwin", "absolute", "override", ""},
			}
			for _, tc := range cases {
				if script == "install.sh" && tc.os != "Linux" || script == "install_mac.sh" && tc.os != "Darwin" {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					home := filepath.Join(root, "user home")
					xdg, override := tc.xdg, ""
					if xdg == "absolute" {
						xdg = filepath.Join(root, "xdg config")
					}
					want := filepath.Join(home, filepath.FromSlash(tc.suffix))
					if tc.suffix == "" {
						want = filepath.Join(xdg, "fyc")
					}
					if tc.override != "" {
						override = filepath.Join(root, "shared config")
						want = override
					}
					for _, dir := range []string{home, want, filepath.Join(root, "skills", "test-skill"), filepath.Join(root, "models")} {
						if err := os.MkdirAll(dir, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					for path, data := range map[string]string{
						filepath.Join(root, "skills", "test-skill", "SKILL.md"): "skill",
						filepath.Join(root, "models", "catalog.json"):           "{}",
						filepath.Join(want, "unrelated.txt"):                    "preserve",
					} {
						if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					prefix := `set -eu
uname() { if [ "${1:-}" = "-m" ]; then printf '%s\n' arm64; else printf '%s\n' "$TEST_OS"; fi; }
die() { echo "$*" >&2; exit 1; }
warn() { echo "$*" >&2; }
# Mock GNU stat so Linux cases also run on BSD hosts.
stat() {
    [ "$#" -eq 3 ] && [ "$1" = "-c" ] && [ "$2" = "%U" ] && [ "$3" = "$HOME" ] || exit 1
    printf '%s\n' fyc-test-user
}
sudo() { [ "$1" = "-u" ] && [ "$2" = "fyc-test-user" ] || exit 1; shift 2; "$@"; }
chown() { echo 'unexpected ownership change' >&2; exit 1; }
REAL_HOME="$HOME"
WORK="$PWD"
`
					if script == "setup.sh" {
						prefix += "WORK=\"$PWD/work\"\nmkdir -p \"$WORK\"\nln -s \"$PWD\" \"$WORK/extract\"\n"
					}
					cmd := exec.Command(shell, "-c", prefix+platform+assets+"\n[ \"$CONFIG_DIR\" = \"$EXPECTED_CONFIG\" ]\n")
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "HOME="+home, "TEST_OS="+tc.os, "FYC_CONFIG_DIR="+override, "XDG_CONFIG_HOME="+xdg, "EXPECTED_CONFIG="+want)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("installer section: %v\n%s", err, out)
					}
					for path, wantData := range map[string]string{
						filepath.Join(want, "skills", "test-skill", "SKILL.md"): "skill",
						filepath.Join(want, "models.json"):                      "{}",
						filepath.Join(want, "unrelated.txt"):                    "preserve",
					} {
						if got, err := os.ReadFile(path); err != nil || string(got) != wantData {
							t.Errorf("%s = %q, %v; want %q", path, got, err, wantData)
						}
					}
				})
			}
		})
	}
}
