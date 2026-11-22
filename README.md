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
  - a local Chromium-based browser only for JavaScript rendering in `web_search`; HTTP search, static pages, JSON, raw text and text-based PDFs work without it (custom binaries: `browser_paths` or `FYC_BROWSER_PATH`)
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
fuckyourcli add ./plugin # install skills + MCP for this project
fuckyourcli --help      # show help
fuckyourcli --version   # show version and short binary SHA-256
fuckyourcli --hash      # print the full binary SHA-256
```

### Install plugins

`fuckyourcli add <source>` installs a portable [Agent Plugins 1.0.0](https://agent-plugins.org/) package containing root `plugin.json`, `skills/<name>/SKILL.md`, and optional `mcp.json`. Sources can be local directories, GitHub repositories (`owner/repo` or HTTPS URL), or HTTPS ZIP archives with `plugin.json` at their root.

```bash
fuckyourcli add ./my-plugin
fuckyourcli add owner/repo --subdir plugins/my-plugin --ref v1.0.0
fuckyourcli add owner/repo --global
fuckyourcli add ./my-plugin --replace
```

The default project scope installs into `.fyc/plugins/<name>` in the current directory; `--global` uses the config directory's `plugins/<name>`. An escaped destination, symlink, unsafe archive path, or oversized package is refused. Nothing is overwritten without `--replace`. The installer snapshots the package, previews its skills and MCP commands, and asks for approval (`--yes` explicitly approves installation without prompting).

Approval records live in the user config directory's `plugins.json`, not in the repository. Cloning someone else's `.fyc/plugins` therefore does not approve their MCP processes. Changed installed files are disabled until reviewed with `add --replace`; update the source and reinstall rather than editing the installed copy. Project plugins override global plugins of the same name; skills and MCP servers are namespaced as `<plugin>--<component>`. Remove the installed directory to stop loading a plugin.

No installation scripts or hooks run. **Local MCP commands run on the host outside the tool sandbox**, starting in the next chat or AG-UI session. Skill descriptions are discovered through the existing skills tools; full instructions are read only for matching tasks or explicit `/skills <plugin>--<skill>` invocations.

Portable MCP `stdio`, `streamable-http`, and `sse` transports are supported, including plugin-relative commands, `cwd`, `${PLUGIN_ROOT}`, and `${PLUGIN_DATA}`. Use `${VARIABLE}` placeholders for credentials, never literal secrets in the package. Missing values can be entered with a masked prompt and are stored only in `auth.json` (otherwise set them in the environment); `auth.json` takes precedence. With `--yes`, missing credentials are reported but never prompted for, and the plugin's MCP servers stay disabled until configured. Host-specific MCP extensions, legacy Codex/Claude manifests, hooks, and marketplace catalogs are not supported by this installer.

### MCP OAuth

HTTP MCP servers requiring OAuth need an explicit login after installation. For example, a generic `my-plugin` package exposing a `my-server` MCP component uses the name `my-plugin--my-server`:

```bash
fuckyourcli mcp status
fuckyourcli mcp login my-plugin--my-server
fuckyourcli mcp login my-plugin--my-server --no-browser # open the printed URL manually
fuckyourcli mcp logout my-plugin--my-server
```

Run these commands from the project containing the plugin, then start a new chat. Login discovers the server's OAuth metadata, registers a public client when supported, and uses PKCE with a state-validated loopback callback. A preregistered client can be supplied with `--client-id ID`; confidential clients use `--client-secret-env VARIABLE`, never a secret on the command line. Use `--callback-port PORT` if that client requires a fixed `http://127.0.0.1:PORT/callback` redirect URI. `--scopes "scope1 scope2"` overrides the advertised scopes. The browser and fyc must be able to reach the same local callback; login times out after five minutes or cancels with Ctrl+C.

Client registration, access tokens, and refresh tokens are stored only in `auth.json`, keyed by server name and endpoint. Both HTTP transports renew expired tokens without prompting, persist refresh-token rotation under a per-server lock, and suggest `mcp login` if authentication is required. An explicit `Authorization` header takes precedence: remove it before using OAuth. OAuth discovery/token requests require HTTPS except on the configured loopback origin, refuse redirects, and block private-network destinations. OAuth client-ID metadata documents are not currently supported; servers must offer dynamic registration or accept a preregistered client.

A failed server no longer hides the others: startup and tool discovery preserve healthy servers and report individual errors. Logout deletes local credentials only; revoke them at the issuer if needed.

Keyboard shortcuts inside the chat:

| Shortcut | Action |
| --- | --- |
| `Ctrl+C` | Stop the current generation (also on macOS, where it is `Ctrl`, not `Cmd`). |
| `Ctrl+O` | Switch between auto and manual mode (same as `/auto`). |
| `Ctrl+R` | Open a search bar for prompt history (case-insensitive), with a filtered result list. ↑/↓ selects a result, Enter recalls without sending, Esc or `Ctrl+C` cancels. In alt-screen mode, the mouse wheel scrolls results and clicking one recalls it. |

Handy commands inside the chat:

| Command | Purpose |
| --- | --- |
| `/help` | Show available commands. |
| `/auto [on\|off]` | Turn auto mode on or off: no confirmation prompts, so use it only in trusted directories. |
| `/model`, `/provider` | Switch model or provider; `/provider` offers to reuse its configured model. Model lists have a fixed search bar in both REPL and alternate-screen TUI (type to filter, arrows to navigate, Enter to select, Esc to cancel). |
| `/tools` | Enable or disable built-in tools. |
| `/sessions` | Manage saved sessions (switch, new, rename, delete) via an interactive menu. |
| `/plan`, `/debug`, `/deep` | One-shot planning, debugging and deep-research modes. |
| `/tutorial <question>` | Answer a question about using fyc from its embedded documentation and no agent tools; use `/mode tutorial` for follow-ups. |
| `/undo` | Rewind to before a turn: conversation, transcript, files and anchors go back together (see [docs](docs/DOCS.md#rewinding-a-turn)). |
| `/compact` | Summarize the conversation to free up context. |
| `/usage` | Show token usage for all time, this month and this session (tokens only, no cost). |
| `/retry` | Repeat the failed provider request without rerunning completed tools. |

See the [full command list](docs/DOCS.md#usage) for the rest.

### Attach files in chat

Use `@test.py` to include a UTF-8 text file or `@photo.png` to attach an image (PNG, JPEG or GIF; requires an image-capable model). Quote paths containing spaces: `@"my dir/test.py"`.

Type `@` to browse workspace paths: ↑/↓ selects, Tab or Enter inserts, Esc dismisses. Suggestions browse one directory at a time (up to 200 matches among its first 4096 entries). The prompt remains editable, including on multiple lines. Email addresses, URLs, code blocks and escaped `\@mentions` remain literal.

Attachments respect workspace confinement and ignore files; symlinks and directories cannot be attached. Limits: 1024 characters per path, 8 distinct files, 256 KiB per text file, 512 KiB total text and an estimated 32,000-token attachment budget. Images reuse `read_media` limits (12 MiB/file, 50 million pixels, automatic downscaling), with 24 MiB total base64 data and the current `max_media` image-count limit. Included files are listed before sending; an invalid or oversized attachment rejects the prompt without truncating it.

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
