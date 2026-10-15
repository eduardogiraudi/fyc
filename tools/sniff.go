package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// sniff limits: conservative defaults so a persistent server log can never be
// dumped wholesale into the model context. Both ceilings are always enforced:
// even a caller that asks for a wide window is capped by the byte limit.
const (
	sniffDefaultLines = 50
	sniffMaxLines     = 500
	sniffDefaultBytes = 16384
	sniffMinBytes     = 1024
	sniffMaxBytes     = 65536
)

// reSniffANSI matches ANSI/VT escape sequences (CSI, OSC, simple escapes and
// charset designators) that would only add noise to a log read by the model.
var reSniffANSI = regexp.MustCompile("\x1b(?:\\[[0-9;?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|[()#][0-9A-Za-z]|[=>])")

// setExited records the process exit state. Called by the waiter goroutine once
// the persistent process ends; guarded by the process mutex because sniff reads
// it concurrently.
func (p *persistentProcess) setExited(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exited = true
	if err != nil {
		p.exitErr = err.Error()
		return
	}
	p.exitErr = ""
}

// exitState returns the exit state under the process mutex.
func (p *persistentProcess) exitState() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited, p.exitErr
}

// normalizeSniffOutput strips escape sequences and normalizes line endings
// (a PTY usually emits CRLF).
func normalizeSniffOutput(s string) string {
	s = reSniffANSI.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func clampSniffInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// captureTail returns the trailing maxBytes of s, restarting at the first whole
// line so the returned text never begins mid-line.
func captureTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := len(s) - maxBytes
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 {
		cut += i + 1
	}
	return "[... earlier output truncated ...]\n" + s[cut:]
}

// humanBytes formats a byte count compactly.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// truncateOneLine collapses newlines and cuts s to at most n runes.
func truncateOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// executeSniffTool handles the "sniff" case.
// Usage: result := executeSniffTool(args)
func executeSniffTool(args map[string]any) string {
	pidF, ok := args["pid"].(float64)
	if !ok {
		return "error: pid argument required as number"
	}
	intPid := int(pidF)

	persistentMu.Lock()
	pp, exists := persistentProcesses[intPid]
	persistentMu.Unlock()
	if !exists {
		return fmt.Sprintf("error: no persistent process found with PID %d", intPid)
	}

	lastN := sniffDefaultLines
	if v, ok := args["last_n"].(float64); ok {
		lastN = int(v)
	}
	lastN = clampSniffInt(lastN, 1, sniffMaxLines)

	maxBytes := sniffDefaultBytes
	if v, ok := args["max_bytes"].(float64); ok {
		maxBytes = int(v)
	}
	maxBytes = clampSniffInt(maxBytes, sniffMinBytes, sniffMaxBytes)

	var re *regexp.Regexp
	if pat, ok := args["pattern"].(string); ok && strings.TrimSpace(pat) != "" {
		compiled, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Sprintf("error: invalid pattern: %v", err)
		}
		re = compiled
	}

	return pp.sniff(lastN, maxBytes, re)
}

// sniff renders the tail window of a persistent process: at most lastN lines of
// the buffered output (optionally only the ones matching re), hard-capped to
// maxBytes. It never returns the whole buffer.
func (p *persistentProcess) sniff(lastN, maxBytes int, re *regexp.Regexp) string {
	if p.Buf == nil {
		return fmt.Sprintf("error: process %d has no output buffer", p.Pid)
	}
	rawBytes, total := p.Buf.Snapshot()
	raw := string(rawBytes)
	if p.Redact != nil {
		raw = p.Redact(raw)
	}
	raw = normalizeSniffOutput(raw)

	// When the buffer trimmed, its first line is likely partial: drop it.
	if total > int64(len(rawBytes)) {
		if i := strings.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		} else {
			raw = ""
		}
	}

	lines := strings.Split(raw, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	var selected []string
	if re != nil {
		for i, ln := range lines {
			if re.MatchString(ln) {
				selected = append(selected, fmt.Sprintf("%d| %s", i+1, ln))
			}
		}
	} else {
		selected = lines
	}

	shown := selected
	if len(shown) > lastN {
		shown = shown[len(shown)-lastN:]
	}
	text := strings.Join(shown, "\n")
	if len(text) > maxBytes {
		text = captureTail(text, maxBytes)
	}

	state := "running"
	if exited, exitErr := p.exitState(); exited {
		if exitErr != "" {
			state = "exited (" + exitErr + ")"
		} else {
			state = "exited"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[sniff] pid %d | %s | cmd: %s\n", p.Pid, state, truncateOneLine(p.Command, 80))
	fmt.Fprintf(&b, "buffer: %s kept of %s written (%s dropped) | lines: %d shown of %d",
		humanBytes(int64(len(rawBytes))), humanBytes(total), humanBytes(total-int64(len(rawBytes))),
		len(shown), len(lines))
	if re != nil {
		fmt.Fprintf(&b, " (%d matched)", len(selected))
	}
	fmt.Fprintf(&b, " | limits: %d lines / %d bytes\n---\n", lastN, maxBytes)

	if strings.TrimSpace(text) == "" {
		b.WriteString("(no output yet)")
	} else {
		b.WriteString(text)
	}
	if omitted := len(selected) - len(shown); omitted > 0 {
		fmt.Fprintf(&b, "\n... [%d earlier matching line(s) omitted]", omitted)
	}
	return b.String()
}
