//go:build windows

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsSandboxProcess is the Windows process plumbing:
//
//   - combined stdout/stderr through a single anonymous pipe;
//   - a job object per command, so Close()/kill() tears down the whole tree
//     (cmd.exe plus any git/go/npm grandchildren) and KILL_ON_JOB_CLOSE makes
//     the tree die if fyc itself disappears.
type windowsSandboxProcess struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	r   *os.File
	job windows.Handle
}

func (p *windowsSandboxProcess) Wait() error { return p.cmd.Wait() }

func (p *windowsSandboxProcess) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *windowsSandboxProcess) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *windowsSandboxProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	if p.r != nil {
		if err := p.r.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		// Keep the read pointer immutable while the output goroutine unwinds.
	}
	// Dropping the last handle to a KILL_ON_JOB_CLOSE job terminates the whole
	// tree; this is what makes kill(pid) clean up grandchildren.
	if p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
	return firstErr
}

// startSandboxed starts the self-re-exec helper and wires the job object. The
// helper is created suspended (CREATE_SUSPENDED) so it cannot spawn cmd.exe — a
// grandchild — before it has been assigned to the job.
func startSandboxed(result sandboxResult) (sandboxProcess, error) {
	if result.spawn != nil {
		// Dedicated-user engine: the runner is launched
		// with CreateProcessWithLogonW, which os/exec cannot express, so it
		// supplies its own process wrapper.
		return result.spawn()
	}
	cmd := result.Cmd

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close() // the parent never writes: the child owns the write end

	proc := &windowsSandboxProcess{cmd: cmd, r: pr}

	job, err := createKillJob()
	if err != nil {
		proc.Close()
		killProcess(cmd)
		return nil, err
	}
	proc.job = job

	if err := assignPIDToJob(job, uint32(cmd.Process.Pid)); err != nil {
		proc.Close()
		killProcess(cmd)
		return nil, err
	}
	if err := resumeMainThread(uint32(cmd.Process.Pid)); err != nil {
		proc.Close()
		killProcess(cmd)
		return nil, err
	}
	return proc, nil
}

// createKillJob creates a job configured to kill every member when the last
// handle to it is closed (our process wrapper) and to die on an unhandled
// exception rather than hang around.
func createKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("sandbox: CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
			windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("sandbox: SetInformationJobObject: %w", err)
	}
	return job, nil
}

func assignPIDToJob(job windows.Handle, pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("sandbox: OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("sandbox: AssignProcessToJobObject: %w", err)
	}
	return nil
}

// resumeMainThread finds the suspended process's main thread and resumes it.
// os/exec discards the thread handle returned by CreateProcess, so it is looked
// up from a toolhelp snapshot by owning PID.
func resumeMainThread(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("sandbox: snapshot threads: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Thread32First(snap, &entry); err != nil {
		return fmt.Errorf("sandbox: Thread32First: %w", err)
	}
	for {
		if entry.OwnerProcessID == pid {
			h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return fmt.Errorf("sandbox: OpenThread: %w", err)
			}
			_, err = windows.ResumeThread(h)
			windows.CloseHandle(h)
			if err != nil {
				return fmt.Errorf("sandbox: ResumeThread: %w", err)
			}
			return nil
		}
		if err := windows.Thread32Next(snap, &entry); err != nil {
			break
		}
	}
	return fmt.Errorf("sandbox: no thread found for pid %d", pid)
}

func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
