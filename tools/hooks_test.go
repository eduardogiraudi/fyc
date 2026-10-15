package tools

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// hookCmd returns a hook command written in the platform shell's syntax (sh -c
// elsewhere, cmd /C on Windows — see shellCommand in hooks.go).
func hookCmd(unix, win string) string {
	if runtime.GOOS == "windows" {
		return win
	}
	return unix
}

// psEncoded wraps a PowerShell script in -EncodedCommand (base64 of UTF-16LE)
// so it survives cmd.exe untouched: no quoting and no %-expansion.
func psEncoded(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		b[i*2] = byte(v)
		b[i*2+1] = byte(v >> 8)
	}
	return "powershell -NoProfile -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
}

func sampleCall() types.ToolCall {
	return types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.ToolCallFunction{
			Name:      "sh",
			Arguments: map[string]interface{}{"command": "ls"},
		},
	}
}

func TestRunPreToolHookDisabledIsNoop(t *testing.T) {
	// Disabled hook.
	out, blocked := RunPreToolHook(config.ToolHook{Enabled: false, Command: "exit 1"}, sampleCall(), t.TempDir())
	if blocked || out != "" {
		t.Fatalf("disabled hook should be a no-op, got out=%q blocked=%v", out, blocked)
	}
	// Enabled but empty command.
	out, blocked = RunPreToolHook(config.ToolHook{Enabled: true, Command: "   "}, sampleCall(), t.TempDir())
	if blocked || out != "" {
		t.Fatalf("empty command should be a no-op, got out=%q blocked=%v", out, blocked)
	}
}

func TestRunPreToolHookBlocksOnNonZeroExit(t *testing.T) {
	h := config.ToolHook{Enabled: true, Command: hookCmd("echo 'policy violation'; exit 2", "echo policy violation & exit /b 2")}
	out, blocked := RunPreToolHook(h, sampleCall(), t.TempDir())
	if !blocked {
		t.Fatalf("expected hook to block on non-zero exit")
	}
	if !strings.Contains(out, "policy violation") {
		t.Fatalf("expected hook output in result, got %q", out)
	}
}

func TestRunPreToolHookAllowsOnZeroExit(t *testing.T) {
	h := config.ToolHook{Enabled: true, Command: "echo ok"}
	out, blocked := RunPreToolHook(h, sampleCall(), t.TempDir())
	if blocked {
		t.Fatalf("expected hook to allow execution, got blocked with %q", out)
	}
	if out != "" {
		t.Fatalf("pre-hook output on success must be discarded, got %q", out)
	}
}

func TestRunPostToolHookRunsForSideEffectsOnly(t *testing.T) {
	dir := t.TempDir()
	// The post hook writes side-effect files and prints to stdout; neither the
	// side effect nor the stdout may leak into the tool result.
	h := config.ToolHook{Enabled: true, Command: "echo side > side.txt"}
	call := sampleCall()
	res := types.ToolResult{Text: "original result"}

	RunPostToolHook(h, call, res, dir)

	data, err := os.ReadFile(filepath.Join(dir, "side.txt"))
	if err != nil {
		t.Fatalf("post hook did not run: %v", err)
	}
	// `echo side > side.txt` emits "side\n" on Unix and "side\r\n" (possibly
	// with a trailing space) via cmd.exe, so compare on the trimmed content.
	if strings.TrimSpace(string(data)) != "side" {
		t.Fatalf("unexpected side effect content: %q", data)
	}
	if res.Text != "original result" {
		t.Fatalf("post hook must not modify the tool result, got %q", res.Text)
	}
}

func TestToolHookEnvAndPayload(t *testing.T) {
	dir := t.TempDir()
	h := config.ToolHook{
		Enabled: true,
		Command: hookCmd(
			`cat > payload.json && printf '%s' "$FYC_HOOK" > hook.txt && printf '%s' "$FYC_TOOL_NAME" > name.txt && printf '%s' "$FYC_TOOL_ARGS" > args.txt`,
			psEncoded(`$p=[Console]::In.ReadToEnd(); [IO.File]::WriteAllText('payload.json',$p); [IO.File]::WriteAllText('hook.txt',$env:FYC_HOOK); [IO.File]::WriteAllText('name.txt',$env:FYC_TOOL_NAME); [IO.File]::WriteAllText('args.txt',$env:FYC_TOOL_ARGS)`),
		),
	}
	call := sampleCall()

	if _, blocked := RunPreToolHook(h, call, dir); blocked {
		t.Fatalf("hook should not block")
	}

	payload, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatalf("payload not written: %v", err)
	}
	ps := string(payload)
	if !strings.Contains(ps, `"hook":"pre_tool"`) || !strings.Contains(ps, `"tool":"sh"`) {
		t.Fatalf("unexpected payload: %s", ps)
	}

	hook, _ := os.ReadFile(filepath.Join(dir, "hook.txt"))
	if string(hook) != "pre_tool" {
		t.Fatalf("unexpected FYC_HOOK: %q", hook)
	}
	name, _ := os.ReadFile(filepath.Join(dir, "name.txt"))
	if string(name) != "sh" {
		t.Fatalf("unexpected FYC_TOOL_NAME: %q", name)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if !strings.Contains(string(args), `"command":"ls"`) {
		t.Fatalf("unexpected FYC_TOOL_ARGS: %q", args)
	}
}
