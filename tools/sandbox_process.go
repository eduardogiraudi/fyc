package tools

// sandboxProcess abstracts the running sandboxed child so exec_tool.go,
// sniff.go and tools.go stay platform-agnostic.
//
//   - Unix (linux/darwin): the command runs behind a PTY (pty.Start) and Read
//     returns the merged terminal stream (colours, REPLs, isatty work as before).
//   - Windows: a plain pipe pair carrying the child's merged stdout/stderr
//     (the restricted-token engine inherits the caller's console; no ConPTY).
type sandboxProcess interface {
	// Wait blocks until the child exits and releases its resources.
	Wait() error
	// Pid returns the child PID, or 0 if it was never started.
	Pid() int
	// Read returns the child's combined stdout/stderr stream.
	Read(p []byte) (int, error)
	// Close releases the PTY/pipe and, on Windows, the job object handle —
	// closing it terminates the whole process tree (KILL_ON_JOB_CLOSE).
	Close() error
}
