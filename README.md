# fuckyourcli

`fuckyourcli` is an interactive AI coding assistant for the terminal, written in Go. It starts in the current working directory, restricts built-in file operations to that workspace, streams Markdown responses, and gives the model controlled tools for code inspection, safe editing, command execution, web research, MCP integration, skills and optional sub-agents.

> 📖 Full reference: **[docs/DOCS.md](docs/DOCS.md)**

## Highlights

- **Interactive terminal UI** with history, slash-command autocomplete, diff previews and streaming Markdown rendering.
- **Works with any provider**: Google Gemini, OpenAI-compatible Chat Completions (Ollama, DeepSeek, local servers…), OpenAI Responses, Anthropic Messages, and OpenAI Codex via OAuth. Just set the base URL and API key.
- **Rich built-in toolset**: codebase snapshots, grep, anchored (hash-based) edits, file operations, command execution, web search/crawling, LSP queries, optional git tools and sub-agents. Every tool can be toggled with `/tools`.
- **Safe by default**: file access is limited to the workspace, changes are previewed and need approval, and shell commands run in a sandbox (`bwrap` on Linux, `sandbox-exec` on macOS, a dedicated local account on Windows).
- **MCP client** for `stdio`, `sse` and `streamableHttp` servers.
- **Sessions & context**: multiple persistent sessions per workspace, auto-compaction, `AGENTS.md` project instructions.
- **Extensible**: pre/post tool hooks, editable system prompt, skills, and an [AG-UI](https://docs.ag-ui.com/introduction) HTTP backend (`fuckyourcli serve`).
- **Optional computer use and media reading** for image-capable providers.

## Requirements

- At least one configured provider (a Gemini API key, an OpenAI-compatible / Responses / Anthropic endpoint, or Codex OAuth).
- Optional, depending on what you use:
  - `git` 2.23+ for the git tools
  - a local Chromium-based browser for `web_search` (if none is found the tool is hidden automatically; add non-canonical executables to `browser_paths` in the config, or point `FYC_BROWSER_PATH` at the binary)
  - `bwrap` / `bubblewrap` for the sandboxed `sh` on Linux
- Go `1.25.0`+ only if you build from source. A C toolchain (e.g. `gcc` or `clang`) is recommended on Linux/macOS for the default build to enable the full-fidelity symbol extractor.

## Install

Linux / macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/eduardogiraudi/fyc/master/setup.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/eduardogiraudi/fyc/master/setup.ps1 | iex
```

The scripts download the latest release, verify its SHA-256, install the binary and set up bundled skills (e.g. `make-skill`) and shell completion. On Windows they also run the one-time `fuckyourcli sandbox setup` (one UAC prompt).

From source:

```bash
./install.sh      # Linux 
./install_mac.sh  # MacOS
.\install.ps1     # Windows
```

All installer options and build details are in [docs/DOCS.md](docs/DOCS.md#install).

## Quick start

```bash
fuckyourcli config   # pick a provider (first run) and configure models
fuckyourcli          # start chatting in the current directory
```

On a fresh install, both commands open a first-run prompt to choose a provider.

## Usage

```bash
fuckyourcli             # start chat
fuckyourcli auto        # start chat with confirmation prompts disabled
fuckyourcli config      # configure providers, models and MCP servers
fuckyourcli serve       # start the AG-UI HTTP backend
fuckyourcli update      # download, verify and install the latest release
fuckyourcli models      # show the model catalog
fuckyourcli --help      # show help
fuckyourcli --version   # show version and short binary SHA-256
fuckyourcli --hash      # print the full binary SHA-256
```

Keyboard shortcuts inside the chat:

| Shortcut | Action |
| --- | --- |
| `Ctrl+C` | Stop the current generation (also on macOS, where it is `Ctrl`, not `Cmd`). |
| `Ctrl+O` | Switch between auto and manual mode (same as `/auto`). |

Handy commands inside the chat:

| Command | Purpose |
| --- | --- |
| `/help` | Show available commands. |
| `/auto [on\|off]` | Turn auto mode on or off: no confirmation prompts, so use it only in trusted directories. |
| `/model`, `/provider` | Switch model or provider. |
| `/tools` | Enable or disable built-in tools. |
| `/sessions` | Manage saved sessions (switch, new, rename, delete) via an interactive menu. |
| `/plan`, `/debug`, `/deep` | One-shot planning, debugging and deep-research modes. |
| `/tutorial <question>` | Answer a question about using fyc from its embedded documentation and no agent tools; use `/mode tutorial` for follow-ups. |
| `/compact` | Summarize the conversation to free up context. |

See the [full command list](docs/DOCS.md#usage) for the rest.

## Documentation

Everything else lives in [docs/DOCS.md](docs/DOCS.md):

[Configuration](docs/DOCS.md#configuration) · [Reasoning](docs/DOCS.md#reasoning--thinking) · [Auto-compaction](docs/DOCS.md#auto-compaction) · [Tool hooks](docs/DOCS.md#tool-hooks-prepost) · [System prompt](docs/DOCS.md#system-prompt-persona) · [AGENTS.md](docs/DOCS.md#project-instructions-agentsmd) · [Modes](docs/DOCS.md#modes) · [Built-in tools](docs/DOCS.md#built-in-tools) · [LSP servers](docs/DOCS.md#lsp-servers) · [Git tools](docs/DOCS.md#git-tools) · [MCP servers](docs/DOCS.md#mcp-servers) · [AG-UI backend](docs/DOCS.md#ag-ui-backend) · [Architecture](docs/DOCS.md#architecture) · [Security model](docs/DOCS.md#security-model)

## Security

`fuckyourcli` is designed to reduce accidental damage, not to be a perfect isolation boundary:

- Built-in file tools are restricted to the startup working directory, and `.git` is always blocked.
- File changes, shell commands and MCP calls require confirmation unless auto mode is on. Use auto mode only in trusted directories with trusted providers.
- Shell commands run in an OS-level sandbox.
- Tool hooks and the status-bar hook run **unsandboxed** on the host: only configure commands you trust.

Details in the [Security Model](docs/DOCS.md#security-model). To report a vulnerability, see the [Security Policy](SECURITY.md).

## Contributing

Contributions are welcome! Open an issue or submit a pull request on the [GitHub repository](https://github.com/eduardogiraudi/fyc). Build and test instructions are in [docs/DOCS.md](docs/DOCS.md#development).

Maintainers: use `./push` for an editable English Conventional Commit and
`./release.sh <version>` to review and approve only the new changelog section
before publishing. See the [commit and release workflow](docs/RELEASING.md).

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
