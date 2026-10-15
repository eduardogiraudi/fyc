---
name: make-skill
description: Create new skills or improve existing ones. Use this whenever the user wants to create, write, edit, fix, extend, or turn a workflow into a skill, even if they only say "make this repeatable", "save this procedure for later", or "teach the agent how to do X". Covers what a skill is, how to write it, where to install it, and how to bundle scripts for deterministic work.
---

# Make Skill

A skill is a folder that teaches the agent how to do one specific kind of task well and repeatably. The agent sees only each skill's name and description at the start; the full instructions are read only when the description matches the task. Everything below follows from that.

## Process

1. **Understand the intent.** Ask what the skill should enable, when it should trigger (what the user would actually say), and what the output should look like. If the current conversation already contains a workflow the user wants to capture, extract the steps, tools, and corrections from it first and only ask about the gaps.
2. **Draft the skill.** Keep it short; add detail only where the agent would otherwise get it wrong.
3. **Try it.** Run 2-3 realistic prompts with the skill and look at what actually happens, not only the final output. Fix what the agent misread, skipped, or wasted time on.
4. **Install it** (see Installation) and tell the user where it went.

Stay flexible: if the user already has a draft, jump to step 3. If they want a quick version without testing, do that.

## Structure

```
skill-name/
├── SKILL.md        required: frontmatter + instructions
├── scripts/        optional: executable code for repeatable steps
├── references/     optional: longer docs, read only when needed
└── assets/         optional: templates and files used in the output
```

`SKILL.md` starts with YAML frontmatter containing `name` (matches the folder name, lowercase with hyphens) and `description`, followed by the instructions in Markdown.

## Writing the description

The description is the only thing the agent sees when deciding whether to use the skill, so it carries all the "when to use" information. State what the skill does and the situations that should trigger it, including phrasings where the user never names the topic. Agents tend to under-trigger skills, so err on the explicit side.

## Writing the instructions

- Use the imperative ("Read the config first", not "You should read the config").
- Explain why a step matters instead of shouting rules in capitals. An agent that understands the reason handles edge cases that a bare rule would not.
- Keep the body lean. If it grows long, move detail into `references/` and say exactly when to read each file.
- Generalize. Write for the whole class of tasks, not for the two examples you happen to have in front of you.
- Show the expected output format with a small example when the format matters.
- Remove anything that does not change the agent's behavior.

## Build tools, don't just describe them

Prose is the weakest way to get a deterministic result. When a step is mechanical (convert a file, validate a format, call an API, generate boilerplate), write a script in `scripts/` and have the skill call it. Agents run scripts reliably and cheaply, and a script behaves the same every time.

Use these signals to decide:

- The agent rewrote the same helper in several test runs: bundle that helper.
- A step has an exact, checkable result: script it, and have the script report errors clearly.
- A step needs judgment or adapts to context: keep it as instructions.

When creating a new skill, ask yourself at the draft stage which steps could become scripts, and propose them to the user. For skills with objectively checkable output, also consider writing a small test or check script so the skill can be verified quickly after every change.

## Improving an existing skill

Read the skill and, if available, how it was actually used. Change what caused real failures rather than adding rules for hypothetical ones. Prefer deleting or rewording to piling on. Keep the original name and folder unless the user asks otherwise.

## Installation

Prefer the universal `.agents` format: a plain folder with `SKILL.md` and its resources. It works across agents and needs no packaging.

**Always ask the user where to install it** before writing anything:

- **Per project:** `.agents/skills/<skill-name>/` inside the project, so the skill travels with the repository and can be committed.
- **Global:** `~/.agents/skills/<skill-name>/`, available in every project.

Example (global):

```bash
mkdir -p ~/.agents/skills/my-skill && cp -r ./my-skill/* ~/.agents/skills/my-skill/
```

After installing, confirm the final path and remind the user that the skill is picked up by its description, so a vague description means it will rarely fire.