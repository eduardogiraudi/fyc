//go:build windows

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Sandbox toggle. Source-only, deliberate for a solo-dev setup
// flipping it requires editing the constant and rebuilding. Promoting it
// to a `config` key (windows_sandbox = restricted-token | off) is the escape
// hatch planned for when it is actually needed.
//
//   - defaultWindowsSandbox: true gives `sh` a sandbox helper; setting it to
//     false runs commands unsandboxed.
//   - defaultWindowsSandboxNetwork: a leftover from the removed AppContainer
//     engine, where it granted the internetClient / privateNetworkClientServer
//     capabilities. Under the restricted-token engine network fail-closed is
//     not yet implemented: it needs a one-time admin firewall
//     rule, not a capability SID — so this flag is advisory only; see
//     restrictedTokenSandboxStatus's Note.
const (
	defaultWindowsSandbox        = true
	defaultWindowsSandboxNetwork = true
)

const (
	// sandboxChildModeArg is the first argument of the self-re-exec helper.
	sandboxChildModeArg = "__sandbox-child"
	// sandboxSpecEnv carries the base64 JSON spec to the helper. Its presence
	// (not the argv) is the authoritative "I am the sandbox child" trigger.
	sandboxSpecEnv = "FYC_SANDBOX_SPEC"
)

// sandboxSpec is the JSON payload handed to the helper so no quoting or
// backslash can survive (or break) the trip through env / command line.
type sandboxSpec struct {
	Cwd            string   `json:"cwd"`
	AllowedRW      []string `json:"rw"`
	AllowedRO      []string `json:"ro"`
	DenyEnv        []string `json:"denyEnv"`
	SecretEnvPairs []string `json:"secretEnv"`
	CmdStr         string   `json:"cmd"`
	Network        bool     `json:"network"`
}

func marshalSandboxSpec(spec sandboxSpec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func unmarshalSandboxSpec(enc string) (sandboxSpec, error) {
	var spec sandboxSpec
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return spec, fmt.Errorf("sandbox spec: %w", err)
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("sandbox spec: %w", err)
	}
	return spec, nil
}

// windowsShell returns the command interpreter, honouring %COMSPEC%.
func windowsShell() string {
	if s := os.Getenv("COMSPEC"); s != "" {
		return s
	}
	return "cmd.exe"
}

// windowsShellCmdLine renders `<shell> /d /s /c "<cmd>"` the way cmd.exe itself
// expects it. The command string must be inserted verbatim between the outer
// quotes: escaping it with argv rules (backslash before every quote, as
// windows.ComposeCommandLine and os/exec do) reaches cmd.exe as literal
// backslashes and breaks any command that quotes a path — which is nearly all
// of them.
func windowsShellCmdLine(cmdStr string) string {
	return windowsShell() + ` /d /s /c "` + cmdStr + `"`
}

// buildSandboxCmd prepares the self-re-exec command: the current binary is
// launched again in "__sandbox-child" mode, which then spawns
// `cmd.exe /d /s /c <CmdStr>` under the restricted-token engine, and every
// command already runs in its own job object (see startSandboxed).
//
// Fail-closed: any error here aborts the tool call; it never falls back to
// running the command unsandboxed.
func buildSandboxCmd(ctx context.Context, cfg sandboxConfig) (sandboxResult, error) {
	shell := windowsShell()

	if !defaultWindowsSandbox {
		// Escape hatch: run the command directly, without the
		// sandbox helper. Only reachable by editing this constant.
		cmd := exec.CommandContext(ctx, shell, "/d", "/s", "/c", cfg.CmdStr)
		cmd.Dir = cfg.RestrictedRoot
		cmd.Env = append(filteredEnv(cfg.DenyEnv), cfg.SecretEnvPairs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return sandboxResult{Cmd: cmd, Cleanup: func() {}}, nil
	}

	// Fase D: when the dedicated-user setup is in place,
	// run through the runner — CreateProcessWithLogonW starts fyc.exe as the
	// sandbox user, which then mints the restricted token from its *own* token
	// — instead of the self-re-exec below. When the setup is missing,
	// buildRunnerSandboxCmd returns ok=false and we keep the current engine.
	if res, ok, err := buildRunnerSandboxCmd(ctx, cfg); ok || err != nil {
		return res, err
	}

	exe, err := os.Executable()
	if err != nil {
		return sandboxResult{}, fmt.Errorf("sandbox: cannot resolve own executable: %w", err)
	}

	enc, err := marshalSandboxSpec(sandboxSpec{
		Cwd:            cfg.RestrictedRoot,
		AllowedRW:      cfg.AllowedRW,
		AllowedRO:      cfg.AllowedRO,
		DenyEnv:        cfg.DenyEnv,
		SecretEnvPairs: cfg.SecretEnvPairs,
		CmdStr:         cfg.CmdStr,
		Network:        defaultWindowsSandboxNetwork,
	})
	if err != nil {
		return sandboxResult{}, fmt.Errorf("sandbox: cannot encode spec: %w", err)
	}

	cmd := exec.CommandContext(ctx, exe, sandboxChildModeArg)
	cmd.Dir = cfg.RestrictedRoot
	cmd.Env = append(filteredEnv(cfg.DenyEnv), sandboxSpecEnv+"="+enc)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	return sandboxResult{Cmd: cmd, Cleanup: func() {}}, nil
}

// MaybeRunAsSandboxChild must be called FIRST in main(). On the sandbox child it
// never returns: it runs the requested command and exits with its status. On the
// normal process it is a no-op.
func MaybeRunAsSandboxChild() {
	enc := os.Getenv(sandboxSpecEnv)
	if enc == "" {
		return
	}
	os.Exit(runSandboxChild(enc))
}

// runSandboxChild decodes the spec and runs `cmd.exe /d /s /c <CmdStr>` with a
// filtered env, propagating the exit code to the parent.
func runSandboxChild(enc string) int {
	spec, err := unmarshalSandboxSpec(enc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox child: "+err.Error())
		return 126
	}

	// The child's own env is already filtered by the parent; re-filter defensively
	// and drop the spec variable so it does not leak into the grandchild.
	fresh := make([]string, 0, len(spec.DenyEnv)+1)
	fresh = append(fresh, spec.DenyEnv...)
	fresh = append(fresh, sandboxSpecEnv)

	if !defaultWindowsSandbox {
		return runSandboxChildUnsandboxed(spec, fresh)
	}

	// The command runs under the restricted-token engine, the only engine left
	// Fail-closed: any error is reported to the parent and
	// nothing runs unsandboxed.
	env := append(filteredEnv(fresh), spec.SecretEnvPairs...)
	code, err := runInRestrictedToken(spec, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox child: "+err.Error())
		return win32ExitError
	}
	return code
}

// runSandboxChildUnsandboxed is the escape hatch used when
// defaultWindowsSandbox is false at compile time: a plain child process with a
// filtered environment (no sandbox helper).
func runSandboxChildUnsandboxed(spec sandboxSpec, fresh []string) int {
	cmd := exec.Command(windowsShell())
	if spec.Cwd != "" {
		cmd.Dir = spec.Cwd
	}
	cmd.Env = append(filteredEnv(fresh), spec.SecretEnvPairs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CmdLine: windowsShellCmdLine(spec.CmdStr)}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "sandbox child: "+err.Error())
		return 126
	}
	return 0
}
