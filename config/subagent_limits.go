package config

import "time"

// Sub-agent budget defaults. A sub-agent that runs out of its budget still
// reports what it did (see tools/subagent_partial.go), so the limits are about
// cost and runaway loops, not about losing work.
//
// There is no ceiling on either budget: a hard task may legitimately need many
// iterations, and a wall-clock deadline is what actually stops an agent that
// goes nowhere. Only floors exist, so a confusing or negative number cannot turn
// into a sub-agent that never runs.
const (
	DefaultSubagentIterations     = 250
	DefaultSubagentTimeoutSeconds = 1200
	MinSubagentTimeoutSeconds     = 30
)

// ClampSubagentIterations applies the floor of an iteration budget. It has no
// upper bound: the caller asked for neither a cap nor a "maximum" setting.
func ClampSubagentIterations(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// SubagentDefaultIterationCount is the budget of a sub-agent spawned without
// max_iterations.
func (c Config) SubagentDefaultIterationCount() int {
	n := c.SubagentDefaultIterations
	if n <= 0 {
		n = DefaultSubagentIterations
	}
	return ClampSubagentIterations(n)
}

// SubagentTimeout is the wall-clock budget of one sub-agent.
func (c Config) SubagentTimeout() time.Duration {
	n := c.SubagentTimeoutSeconds
	if n <= 0 {
		n = DefaultSubagentTimeoutSeconds
	}
	return time.Duration(max(MinSubagentTimeoutSeconds, n)) * time.Second
}
