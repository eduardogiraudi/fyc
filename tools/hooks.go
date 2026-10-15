package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// hookTimeout bounds a single hook invocation.
const hookTimeout = 30 * time.Second

// hookMaxOutput caps the captured hook output kept in memory (and, for the
// pre-hook, surfaced to the model when it blocks).
const hookMaxOutput = 10 * 1024

// hookPayload is the JSON document written to the hook's stdin.
type hookPayload struct {
	Hook      string          `json:"hook"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    string          `json:"result,omitempty"`
}

// shellCommand builds the platform-appropriate shell invocation.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// runToolHook executes a configured hook and returns its combined output.
// ran reports whether the hook actually executed (enabled + non-empty command);
// exitErr is non-nil when the hook failed or timed out.
func runToolHook(h config.ToolHook, phase string, call types.ToolCall, resultText, root string) (out string, ran bool, exitErr error) {
	if !h.Enabled || strings.TrimSpace(h.Command) == "" {
		return "", false, nil
	}

	argsJSON, _ := json.Marshal(call.Function.Arguments)
	payload, err := json.Marshal(hookPayload{
		Hook:      phase,
		Tool:      call.Function.Name,
		Arguments: argsJSON,
		Result:    resultText,
	})
	if err != nil {
		return "hook error: " + err.Error(), true, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	cmd := shellCommand(ctx, h.Command)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"FYC_HOOK="+phase,
		"FYC_TOOL_NAME="+call.Function.Name,
		"FYC_TOOL_ARGS="+string(argsJSON),
	)
	cmd.Stdin = bytes.NewReader(payload)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err = cmd.Run()
	out = strings.TrimRight(buf.String(), "\n")
	if len(out) > hookMaxOutput {
		out = out[:hookMaxOutput] + "\n…[hook output truncated]"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(out + "\nhook timeout after " + hookTimeout.String()), true, ctx.Err()
	}
	if err != nil && strings.TrimSpace(out) == "" {
		out = err.Error()
	}
	return out, true, err
}

// RunPreToolHook executes the pre-tool hook. blocked is true when the hook exits
// with a non-zero status (or times out): the tool must not run and the returned
// output explains why. On success the output is discarded (it is not shown nor
// sent to the model).
func RunPreToolHook(h config.ToolHook, call types.ToolCall, root string) (out string, blocked bool) {
	output, ran, err := runToolHook(h, "pre_tool", call, "", root)
	if !ran {
		return "", false
	}
	if err != nil {
		return output, true
	}
	return "", false
}

// RunPostToolHook executes the post-tool hook for side effects only. Its exit
// status and output are intentionally ignored: the result is neither added to
// the model context nor written to the CLI output.
func RunPostToolHook(h config.ToolHook, call types.ToolCall, res types.ToolResult, root string) {
	_, _, _ = runToolHook(h, "post_tool", call, res.Text, root)
}
