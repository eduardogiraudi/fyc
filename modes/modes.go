package modes

import (
	"strings"

	"fuckyourcli/types"
)

type Mode struct {
	Name         string
	Prefix       string
	Description  string
	Instructions string
	// ToolsDisabled hides every tool while the mode is active.
	ToolsDisabled bool
	// DeniedTools lists tools hidden while the mode is active.
	DeniedTools []string
	// AllowedTools, when non-empty, is the exclusive allowlist of the mode:
	// every tool outside it is hidden for the duration of the mode.
	AllowedTools []string
}

// ExposesTool reports whether the mode exposes the named tool. A mode only
// narrows the tool set: it never turns an unavailable tool back on.
func (m *Mode) ExposesTool(name string) bool {
	if m == nil {
		return true
	}
	if m.ToolsDisabled {
		return false
	}
	for _, n := range m.DeniedTools {
		if n == name {
			return false
		}
	}
	if len(m.AllowedTools) == 0 {
		return true
	}
	for _, n := range m.AllowedTools {
		if n == name {
			return true
		}
	}
	return false
}

// FilterTools narrows all to the tools exposed by the mode. It only ever
// removes tools, so a tool the user disabled is never re-enabled by a mode. A
// nil mode (plain chat) returns the input untouched.
func (m *Mode) FilterTools(all []types.Tool) []types.Tool {
	if m == nil || (!m.ToolsDisabled && len(m.DeniedTools) == 0 && len(m.AllowedTools) == 0) {
		return all
	}
	out := make([]types.Tool, 0, len(all))
	for _, t := range all {
		if m.ExposesTool(t.Function.Name) {
			out = append(out, t)
		}
	}
	return out
}

// GetInstructions returns the mode instructions, optionally extending them
// with sub-agent research protocol when sub-agents are enabled.
func (m *Mode) GetInstructions(subEnabled bool) string {
	if !subEnabled || m.Name != "Deep Research" {
		return m.Instructions
	}
	return m.Instructions + "\n\n## Sub-Agent Research Protocol\nUse `spawn_subagents` to delegate all research:\n\n1. **Spawn sub-agents** with only `web_search` as their tool (`\"tools\": [\"web_search\"]`). Each sub-agent must have a unique `id`.\n2. **Different queries**: Each sub-agent must receive a different search query to cover all angles of the topic.\n3. **Crawl sub-links**: Sub-agents must use `web_search` with specific URLs crawled from previous results to dive deeper into the sources.\n4. **Mandatory Gap Analysis**: After receiving the first round of results, list at least 3 missing details, specific data points, or unverified claims that require further investigation.\n5. **Forced Iteration (Round 2+)**: You are strictly required to run AT LEAST 2 sequential rounds of research. Use the Gap Analysis to spawn a new wave of sub-agents with refined, deeper queries. Single-round execution is prohibited.\n6. **Synthesize**: Compile all findings from ALL rounds into the final comprehensive structured report."
}

type Command struct {
	Name        string
	Description string
}

func GetCommands(webSearchEnabled ...bool) []Command {
	allowWebSearch := true
	if len(webSearchEnabled) > 0 {
		allowWebSearch = webSearchEnabled[0]
	}
	cmds := []Command{
		{Name: "/help", Description: "Show available commands"},
		{Name: "/auto", Description: "Enable autonomous execution"},
		{Name: "/airplane", Description: "Toggle web_search and web-dependent commands"},
		{Name: "/clear", Description: "Clear chat context (keeps system messages)"},
		{Name: "/compact", Description: "Compact chat context into a structured summary"},
		{Name: "/delay", Description: "Set delay between AI responses (e.g., /delay 1.5)"},
		{Name: "/temp", Description: "Set temperature for AI responses (e.g., /temp 0.8)"},
		{Name: "/reasoning", Description: "Manage thinking/reasoning for the active model (interactive)"},
		{Name: "/sub", Description: "Enable/disable sub-agent tools (/sub on|off)"},
		{Name: "/mode", Description: "Switch between modes or list available modes"},
		{Name: "/model", Description: "Switch AI model for current provider"},
		{Name: "/provider", Description: "Switch AI provider and model"},
		{Name: "/exit", Description: "Exit the application"},
		{Name: "/tools", Description: "Enable/disable AI tools"},
		{Name: "/hooks", Description: "Configure pre/post tool execution hooks"},
		{Name: "/sysprompt", Description: "Edit the base system prompt (persona)"},
		{Name: "/skills", Description: "Inject a skill by name"},
		{Name: "/sessions", Description: "Manage chat sessions (switch, new, rename, delete) via an interactive menu"},
	}
	// Add modes if not already covered by explicit commands
	for _, m := range Modes {
		if !allowWebSearch && m.Name == "Deep Research" {
			continue
		}
		found := false
		for _, c := range cmds {
			if c.Name == m.Prefix {
				found = true
				break
			}
		}
		if !found {
			cmds = append(cmds, Command{Name: m.Prefix, Description: m.Description})
		}
	}
	return cmds
}

// workspaceMutatingTools are the tools that create, edit or delete files in the
// workspace. Plan and Debug modes deny them so the model can investigate and
// propose but never apply changes. sh stays available on purpose: it runs in a
// sandbox and is useful to reproduce or inspect a bug.
var workspaceMutatingTools = []string{
	"replace_anchors", "write", "delete", "make_dir", "copy", "move", "rename",
}

var PlanMode = Mode{
	Name:        "Plan",
	Prefix:      "/plan",
	Description: "Create a detailed action plan before implementing changes.",
	DeniedTools: workspaceMutatingTools,
	Instructions: `You are in PLAN MODE. 
Your goal is to create a comprehensive and perfect action plan for the user's request.
Follow these steps:
1.  **Analyze**: Understand the request and explore the codebase if necessary using available tools.
2.  **Search**: If you are unsure about any technology or implementation detail, use the 'web_search' tool to find documentation.
3.  **Plan**: Propose a step-by-step plan. Be specific about files to create or modify.
4.  **Confirm**: DO NOT implement any changes yet UNLESS the user has already approved a plan in the conversation history. If you are proposing a plan, at the end of your response, you MUST ask the user for confirmation: "Do you want me to proceed with the implementation?" (or similar).

If the user has already approved the plan, proceed with the implementation using the appropriate tools.`,
}

func (m Mode) RequiresWebSearch() bool {
	return m.Name == "Deep Research"
}

var DeepResearchMode = Mode{
	Name:         "Deep Research",
	Prefix:       "/deep",
	Description:  "Conduct in-depth research using multi-level exploration and source verification.",
	AllowedTools: []string{"web_search", "spawn_subagents"},
	Instructions: `You are in DEEP RESEARCH MODE. Your goal is to conduct comprehensive, multi-step research on the user's topic.

## Research Process
1. **Plan** — Start by outlining a research strategy. List key questions and potential search queries.
2. **Search** — Use web_search multiple times with different queries to cover all angles. Do not stop after one search.
3. **Deep Dive** — When search results contain promising URLs, crawl them with web_search to extract detailed information.
4. **Cross-Verify** — Check key facts across at least 3 independent sources. If sources disagree, note the discrepancy.
5. **Synthesize** — Compile findings into a structured report with sections, citations, and source annotations.

## Output Format
Always end with a **Sources** section listing every URL consulted with a brief note on relevance.

## Important
- Do not stop after a single search. Iterate until the topic is thoroughly covered.
- If you encounter contradictory information, explicitly mention it.
- Be transparent about your research process.`,
}

var DebugMode = Mode{
	Name:        "Debug",
	Prefix:      "/debug",
	Description: "Analyze the codebase to find and explain a bug.",
	DeniedTools: workspaceMutatingTools,
	Instructions: `You are in DEBUG MODE.
Your goal is to find the cause of a bug and propose a solution.
1.  **Investigate**: Use tools to read code, check logs, or run tests.
2.  **Explain**: Clearly explain why the bug is happening.
3.  **Propose**: Show the code changes needed to fix it.
4.  **Confirm**: Do not apply the fix yet. Ask the user: "Do you want me to apply this fix?"`,
}

var TutorialMode = Mode{
	Name:          "Tutorial",
	Prefix:        "/tutorial",
	Description:   "Answer questions about using fyc.",
	ToolsDisabled: true,
	Instructions: `You are in TUTORIAL MODE for fuckyourcli (fyc).
Answer questions about using fyc from the product documentation provided below.
Give practical, current instructions with relevant commands and examples. Be concise and do not invent features. If the documentation does not answer the question, say so plainly. If the user wants something done rather than explained, show them how to do it in fyc and mention they can leave tutorial mode to have you do it.

## fyc documentation
` + tutorialDocs,
}

var Modes = []Mode{
	PlanMode,
	DebugMode,
	DeepResearchMode,
	TutorialMode,
}

func DetectMode(input string) (*Mode, string) {
	trimmed := strings.TrimSpace(input)
	for _, mode := range Modes {
		if strings.HasPrefix(trimmed, mode.Prefix) {
			content := strings.TrimSpace(strings.TrimPrefix(trimmed, mode.Prefix))
			return &mode, content
		}
	}
	return nil, input
}

// GetMode resolves a mode by its name ("Deep Research") or by its prefix stem
// ("deep"), so "/mode deep" and "/mode deep research" both work.
func GetMode(name string) *Mode {
	key := strings.ToLower(strings.TrimSpace(name))
	for _, mode := range Modes {
		if strings.ToLower(mode.Name) == key {
			return &mode
		}
		if strings.TrimPrefix(strings.ToLower(mode.Prefix), "/") == key {
			return &mode
		}
	}
	return nil
}
