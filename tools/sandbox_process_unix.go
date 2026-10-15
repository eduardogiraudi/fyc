//go:build !windows

package tools

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// unixSandboxProcess preserves the historical PTY behaviour: the child gets a
// real terminal, so colours, interactive REPLs and isatty() keep working
// exactly as before the Windows work started.
type unixSandboxProcess struct {
	cmd *exec.Cmd
	f   *os.File
}

func (p *unixSandboxProcess) Wait() error { return p.cmd.Wait() }

func (p *unixSandboxProcess) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *unixSandboxProcess) Read(b []byte) (int, error) { return p.f.Read(b) }

func (p *unixSandboxProcess) Close() error { return p.f.Close() }

// startSandboxed launches the command built by buildSandboxCmd behind a PTY.
func startSandboxed(result sandboxResult) (sandboxProcess, error) {
	if result.spawn != nil {
		return result.spawn() // Windows-only hook; never set on Unix
	}
	f, err := pty.Start(result.Cmd)
	if err != nil {
		return nil, err
	}
	return &unixSandboxProcess{cmd: result.Cmd, f: f}, nil
}
