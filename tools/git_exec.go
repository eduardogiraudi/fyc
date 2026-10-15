package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fuckyourcli/types"
)

// ── git execution layer ──────────────────────────────────────────────────────
//
// Git is never driven through the `sh` tool: the command sandbox masks `.git`,
// so the git tools execute the real binary directly with an argv (no shell), a
// sanitized non-interactive environment, a hard timeout and credential
// redaction applied to everything sent back to the model.

const (
	// gitDefaultTimeout is the timeout for local operations.
	gitDefaultTimeout = 30 * time.Second
	// gitNetworkTimeout is the timeout for fetch/pull/push/clone.
	gitNetworkTimeout = 5 * time.Minute
	// gitMaxOutputBytes caps the output kept in the model context.
	gitMaxOutputBytes = 200 * 1024
	// gitMaxLines caps line-oriented output (log/blame/ls-files).
	gitMaxLines = 2000
	// gitPathEnv overrides the git binary (used by tests with a fake git).
	gitPathEnv = "FYC_GIT_PATH"
)

// gitEnvDeny lists the environment variables that could redirect git to
// another repository, index or configuration. They are dropped before running.
var gitEnvDeny = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_CEILING_DIRECTORIES":          true,
	"GIT_PREFIX":                       true,
	"GIT_EDITOR":                       true,
	"GIT_SEQUENCE_EDITOR":              true,
	"GIT_PAGER":                        true,
	"GIT_ASKPASS":                      true,
	"GIT_EXTERNAL_DIFF":                true,
	"GIT_FLUSH":                        true,
	"GIT_TRACE":                        true,
	"GIT_TRACE2":                       true,
	"GIT_TRACE2_EVENT":                 true,
}

// gitEnvFixed is the non-interactive environment enforced on every git run.
var gitEnvFixed = []string{
	"GIT_TERMINAL_PROMPT=0", // never wait for credentials on the terminal
	"GIT_PAGER=cat",
	"GIT_EDITOR=true",       // never open an editor
	"GIT_SEQUENCE_EDITOR=:", // interactive-sequence editors become no-ops
	"GIT_MERGE_AUTOEDIT=no",
	"LC_ALL=C", // stable, parseable output
	"LANG=C",
}

// gitOptions configures a single git invocation.
type gitOptions struct {
	// Dir is the working directory (the workspace root).
	Dir string
	// Stdin is piped to the process when non-empty.
	Stdin string
	// Timeout defaults to gitDefaultTimeout.
	Timeout time.Duration
	// Env holds extra environment entries appended after the sanitized env.
	Env []string
}

// gitResult is the outcome of one git invocation.
type gitResult struct {
	Args      []string
	Stdout    string
	Stderr    string
	Exit      int
	Err       error
	TimedOut  bool
	Truncated bool
}

// OK reports whether the command completed successfully.
func (r gitResult) OK() bool { return r.Err == nil && !r.TimedOut && r.Exit == 0 }

// CommandLine returns a display/redacted form of the executed command.
func (r gitResult) CommandLine() string {
	return redactGitOutput("git " + strings.Join(r.Args, " "))
}

// Output returns stdout (falling back to stderr), already redacted/truncated.
func (r gitResult) Output() string {
	out := strings.TrimRight(r.Stdout, "\n")
	if strings.TrimSpace(out) == "" {
		out = strings.TrimRight(r.Stderr, "\n")
	}
	if strings.TrimSpace(out) == "" {
		return "(no output)"
	}
	return out
}

// Message combines stdout and stderr, for error reporting.
func (r gitResult) Message() string {
	var parts []string
	if s := strings.TrimRight(r.Stdout, "\n"); strings.TrimSpace(s) != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimRight(r.Stderr, "\n"); strings.TrimSpace(s) != "" {
		parts = append(parts, s)
	}
	if r.Truncated {
		parts = append(parts, fmt.Sprintf("[output truncated at %d KB]", gitMaxOutputBytes/1024))
	}
	return strings.Join(parts, "\n")
}

// gitBinary returns the git executable to run.
func gitBinary() string {
	if p := strings.TrimSpace(os.Getenv(gitPathEnv)); p != "" {
		return p
	}
	return "git"
}

// GitAvailable reports whether the git binary is installed and executable.
func GitAvailable() bool {
	bin := gitBinary()
	if strings.Contains(bin, string(os.PathSeparator)) || strings.Contains(bin, "/") {
		if fi, err := os.Stat(bin); err != nil || fi.IsDir() {
			return false
		}
	} else if _, err := exec.LookPath(bin); err != nil {
		return false
	}
	return runGit([]string{"--version"}, gitOptions{Timeout: 10 * time.Second}).OK()
}

// gitEnv builds the environment for a git run: the parent environment minus
// the variables that could hijack the repository/config, plus a fixed
// non-interactive set (later entries win).
func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+len(gitEnvFixed))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if gitEnvDeny[name] || strings.HasPrefix(name, "GIT_CONFIG_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, gitEnvFixed...)
}

// runGit executes git with the given argv.
func runGit(args []string, opts gitOptions) gitResult {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = gitDefaultTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	full := make([]string, 0, len(args)+8)
	full = append(full,
		"--no-pager",
		"-c", "core.quotepath=false",
		"-c", "color.ui=false",
		"-c", "advice.detachedHead=false",
	)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, gitBinary(), full...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = append(gitEnv(), opts.Env...)
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	res := gitResult{Args: args}
	runErr := cmd.Run()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
	case runErr != nil:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.Exit = ee.ExitCode()
		} else {
			res.Err = runErr
		}
	}

	res.Stdout = redactGitOutput(stdout.String())
	res.Stderr = redactGitOutput(stderr.String())

	var truncated bool
	res.Stdout, truncated = truncateGitOutput(res.Stdout)
	res.Truncated = truncated
	res.Stderr, truncated = truncateGitOutput(res.Stderr)
	res.Truncated = res.Truncated || truncated
	return res
}

// ── output hygiene ───────────────────────────────────────────────────────────

var (
	// gitURLCredsRe rewrites scheme://user:secret@host to scheme://[REDACTED]@host.
	gitURLCredsRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`)
	// gitSecretKVRe rewrites password/token/secret style key=value pairs.
	gitSecretKVRe = regexp.MustCompile(`(?i)\b(password|passwd|token|secret|api[_-]?key|access[_-]?key|access[_-]?token|auth[_-]?token)\b(\s*[:=]\s*)(\S+)`)
	// gitAuthHeaderRe rewrites Authorization headers.
	gitAuthHeaderRe = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization)(\s*[:=]\s*)(.+)`)
)

// redactGitOutput removes credentials from git output before it reaches the
// model (URLs, key=value secrets, Authorization headers).
func redactGitOutput(s string) string {
	if s == "" {
		return s
	}
	s = gitURLCredsRe.ReplaceAllString(s, "$1[REDACTED]@")
	s = gitSecretKVRe.ReplaceAllString(s, "$1$2[REDACTED]")
	s = gitAuthHeaderRe.ReplaceAllString(s, "$1$2[REDACTED]")
	return s
}

// truncateGitOutput caps the output size on a line boundary.
func truncateGitOutput(s string) (string, bool) {
	if len(s) <= gitMaxOutputBytes {
		return s, false
	}
	cut := strings.LastIndexByte(s[:gitMaxOutputBytes], '\n')
	if cut <= 0 {
		cut = gitMaxOutputBytes
	}
	return s[:cut] + fmt.Sprintf("\n... [output truncated at %d KB]", gitMaxOutputBytes/1024), true
}

// truncateGitLines keeps at most max lines of a text block.
func truncateGitLines(s string, max int) string {
	if max <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	kept := lines[:max]
	return strings.Join(kept, "\n") + fmt.Sprintf("\n... [%d more lines omitted, refine the request or use start/end]", len(lines)-max)
}

// ── tool plumbing ────────────────────────────────────────────────────────────

// gitToolRun executes a git command for a tool and formats the result for the
// model.
func gitToolRun(workspaceRoot string, timeout time.Duration, stdin string, args []string) types.ToolResult {
	res := runGit(args, gitOptions{Dir: workspaceRoot, Stdin: stdin, Timeout: timeout})
	return gitResultToTool(res, timeout)
}

// gitResultToTool converts a gitResult into a ToolResult, turning failures into
// readable error messages.
func gitResultToTool(res gitResult, timeout time.Duration) types.ToolResult {
	if res.TimedOut {
		return textResult(fmt.Sprintf("error: `%s` timed out after %s", res.CommandLine(), timeout))
	}
	if res.Err != nil {
		return textResult(fmt.Sprintf("error: cannot execute git: %v (git >= 2.23 must be installed and on PATH)", res.Err))
	}
	if res.Exit != 0 {
		msg := res.Message()
		if strings.TrimSpace(msg) == "" {
			msg = fmt.Sprintf("exit status %d", res.Exit)
		}
		return textResult(fmt.Sprintf("error: `%s` failed (exit %d)\n%s", res.CommandLine(), res.Exit, msg))
	}
	return textResult(res.Output())
}

// boolArg reads a boolean argument (missing → false).
func boolArg(args map[string]interface{}, key string) bool {
	v, ok := args[key].(bool)
	return ok && v
}

// boolArgDefault reads a boolean argument with an explicit default.
func boolArgDefault(args map[string]interface{}, key string, def bool) bool {
	if args[key] == nil {
		return def
	}
	v, ok := args[key].(bool)
	if !ok {
		return def
	}
	return v
}

// gitVersionRe matches the output of `git --version`.
var gitVersionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

var (
	gitVersionOnce     sync.Once
	gitSupportsSwitchV bool
)

// gitSupportsSwitch reports whether the installed git understands
// `git switch` (>= 2.23). Older versions fall back to `git checkout`.
func gitSupportsSwitch() bool {
	gitVersionOnce.Do(func() {
		gitSupportsSwitchV = true
		res := runGit([]string{"--version"}, gitOptions{Timeout: 10 * time.Second})
		if !res.OK() {
			return
		}
		m := gitVersionRe.FindStringSubmatch(res.Stdout)
		if m == nil {
			return
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major < 2 || (major == 2 && minor < 23) {
			gitSupportsSwitchV = false
		}
	})
	return gitSupportsSwitchV
}
