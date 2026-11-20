package ui

import (
	"strings"
	"sync"

	"fuckyourcli/types"
)

// CaptureSink collects the text a command prints and forwards everything else
// to the sink the chat is using. A command run while a turn is generating must
// not write into the transcript — the model owns it — so its output becomes the
// transient notice above the input, while its interactive prompts and its
// status-bar updates still reach the running chat.
type CaptureSink struct {
	inner types.OutputSink

	mu  sync.Mutex
	buf strings.Builder
}

func NewCaptureSink(inner types.OutputSink) *CaptureSink {
	return &CaptureSink{inner: inner}
}

// Text is what the command printed, without the trailing blank lines (the
// commands write a newline after every line, and the notice has no room for a
// run of empty rows).
func (c *CaptureSink) Text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimRight(c.buf.String(), "\n")
}

func (c *CaptureSink) write(text string) {
	c.mu.Lock()
	c.buf.WriteString(text)
	c.mu.Unlock()
}

func (c *CaptureSink) WriteText(token string) { c.write(token) }
func (c *CaptureSink) WriteNewline()          { c.write("\n") }

// WriteStats goes to the real sink: the provider/temperature/mode row describes
// the frame, not the change the command made.
func (c *CaptureSink) WriteStats(providerInfo, tokenStats string) {
	if c.inner != nil {
		c.inner.WriteStats(providerInfo, tokenStats)
	}
}

// SelectOption and ReadLine stay interactive: a live command may open a menu or
// ask for a line, and that prompt has to reach the user.
func (c *CaptureSink) SelectOption(title string, options []string) int {
	if c.inner == nil {
		return SelectorCanceled
	}
	return c.inner.SelectOption(title, options)
}

// SelectSearchableOption keeps the searchable model pickers working where the
// wrapped sink offers one.
func (c *CaptureSink) SelectSearchableOption(title string, options []string) int {
	if searchable, ok := c.inner.(interface{ SelectSearchableOption(string, []string) int }); ok {
		return searchable.SelectSearchableOption(title, options)
	}
	return c.SelectOption(title, options)
}

func (c *CaptureSink) ReadLine() (string, bool) {
	if c.inner == nil {
		return "", false
	}
	return c.inner.ReadLine()
}

// Everything below belongs to the streaming lifecycle and never to a command:
// it is dropped, so a command can neither finish, reset nor scroll the turn it
// runs beside.
func (c *CaptureSink) WriteReasoning(string)                 {}
func (c *CaptureSink) StartSpinner(string)                   {}
func (c *CaptureSink) StopSpinner()                          {}
func (c *CaptureSink) WriteDiffCraft(int, int)               {}
func (c *CaptureSink) FinishDiffCraft()                      {}
func (c *CaptureSink) WriteToolOutput(string, string)        {}
func (c *CaptureSink) WriteInterrupt()                       {}
func (c *CaptureSink) Reset()                                {}
func (c *CaptureSink) Finalize()                             {}
func (c *CaptureSink) StartSubagent(string, string)          {}
func (c *CaptureSink) UpdateSubagent(string, string)         {}
func (c *CaptureSink) FinishSubagent(string, string, string) {}
func (c *CaptureSink) StartSubagentBatch(int)                {}
func (c *CaptureSink) FinishSubagentBatch()                  {}
func (c *CaptureSink) StartSubagentPanel(interface{})        {}
