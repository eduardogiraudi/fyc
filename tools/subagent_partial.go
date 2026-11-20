package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// subagentWrapUpTimeout bounds the extra request that reports partial work. It
// runs on its own deadline: the sub-agent's budget is already spent.
const subagentWrapUpTimeout = 90 * time.Second

// maxDigestSteps bounds the steps listed when no model could summarize them.
const maxDigestSteps = 40

const subagentWrapUpPrompt = "Your budget is exhausted (%s). Do not call any tool. " +
	"Reply now with the structured result in the required format, with Status: partial. " +
	"Report what you completed (including files read or changed), what remains undone, and any findings " +
	"the main agent needs. Use only what is already in this conversation."

// subagentStopped reports a sub-agent that ran out of iterations or time. A
// final request without tool use asks the model itself to summarize the work;
// if that fails too, the caller still gets a deterministic digest of the steps
// that ran, so a spent budget never discards what was already done.
//
// The tool definitions are still sent (the request must stay valid for
// providers that reject tool calls in the history without tool definitions);
// any tool call in the answer is ignored.
func subagentStopped(base context.Context, cfg config.Config, chain []config.CompactorChoice, start int, sysPrompt string, history []types.Message, toolDefs []types.Tool, sink types.OutputSink, reason string, progress []string, lastContent string) string {
	if base.Err() != nil {
		return "subagent interrupted"
	}
	ctx, cancel := context.WithTimeout(base, subagentWrapUpTimeout)
	defer cancel()

	wrap := make([]types.Message, 0, len(history)+1)
	wrap = append(wrap, history...)
	wrap = append(wrap, types.Message{Role: "user", Content: fmt.Sprintf(subagentWrapUpPrompt, reason)})

	sink.Reset()
	result, _, _, err := streamWithChain(ctx, cfg, chain, start, sysPrompt, wrap, toolDefs, sink)
	if err == nil && result != nil && !result.Interrupted && strings.TrimSpace(result.Content) != "" {
		return fmt.Sprintf("⚠ Sub-agent stopped early: %s. Partial result:\n\n%s", reason, result.Content)
	}
	why := "the wrap-up request returned nothing"
	if err != nil {
		why = "the wrap-up request failed: " + err.Error()
	}
	return fmt.Sprintf("⚠ Sub-agent stopped early: %s; %s.", reason, why) + subagentDigest(progress, lastContent)
}

// subagentDigest lists the steps a sub-agent completed and its last message.
// It is empty when there is nothing to report, so it can be appended to any
// failure text.
func subagentDigest(progress []string, lastContent string) string {
	var sb strings.Builder
	if len(progress) > 0 {
		sb.WriteString("Work done before stopping:\n")
		first := 0
		if len(progress) > maxDigestSteps {
			first = len(progress) - maxDigestSteps
			fmt.Fprintf(&sb, "- … %d earlier steps omitted\n", first)
		}
		for _, step := range progress[first:] {
			sb.WriteString("- " + step + "\n")
		}
	}
	if c := strings.TrimSpace(lastContent); c != "" {
		sb.WriteString("\nLast message from the sub-agent:\n" + c + "\n")
	}
	body := strings.TrimSpace(sb.String())
	if body == "" {
		return ""
	}
	return "\n\n" + body
}
