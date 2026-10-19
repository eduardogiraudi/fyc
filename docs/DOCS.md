# fuckyourcli

`fuckyourcli` is an interactive AI coding assistant for the terminal, written in Go. This document is the full reference; for a quick overview and install one-liners see the [README](../README.md).

The Go module and default binary name are both `fuckyourcli`; application-owned directories use `fyc`.

## Contents

- Setup: [Requirements](#requirements) · [Build](#build) · [Install](#install) · [Configuration](#configuration) · [OpenAI Codex (OAuth)](#openai-codex-oauth)
- Using it: [Usage](#usage) · [Reasoning / thinking](#reasoning--thinking) · [Prompt caching](#prompt-caching) · [Auto-compaction](#auto-compaction) · [Modes](#modes)
- Customisation: [Tool hooks](#tool-hooks-prepost) · [Status bar hook](#status-bar-hook) · [System prompt](#system-prompt-persona) · [Project instructions (AGENTS.md)](#project-instructions-agentsmd)
- Tools & integrations: [Built-in tools](#built-in-tools) · [LSP servers](#lsp-servers) · [Git tools](#git-tools) · [MCP servers](#mcp-servers) · [AG-UI backend](#ag-ui-backend)
- Internals: [Runtime files](#runtime-files) · [Architecture](#architecture) · [Security model](#security-model) ([Windows sandbox](#windows-sandbox)) · [Development](#development)

## Requirements

- Go `1.25.0` or newer, matching `go.mod`.
- A C toolchain for the default build: the `context_snapshot` symbol extractor links `tree-sitter` through cgo on Linux and macOS. Builds where cgo is unavailable — every Windows build, and anything compiled with `CGO_ENABLED=0` — fall back to a dependency-free extractor: full fidelity for Go files, regex approximation for the other languages (see [Build](#build)).
- At least one configured provider:
  - a Google Gemini API key,
  - an OpenAI-compatible Chat Completions endpoint, for example Ollama at `http://localhost:11434/v1`,
  - an OpenAI Responses API-compatible endpoint,
  - an Anthropic Messages API-compatible endpoint,
  - OpenAI Codex OAuth authentication.
- For the git tools: `git` 2.23 or newer on `PATH`. The workspace must be inside a repository, except for `git_init` and `git_clone`, which stay available to bootstrap one.
- For `web_search`: a supported local browser. The crawler probes `FYC_BROWSER_PATH` first, then the `browser_paths` list from `config.json`, then the built-in locations (Chrome, Chromium, Brave, Edge, Opera, Vivaldi, Arc). **When none is found, the tool is hidden from the model** and a notice is printed at startup; availability is re-checked at startup and every 30 seconds. Set `FYC_BROWSER_PATH` to force a specific browser binary (useful for non-standard installs and tests). Add non-canonical executables to `browser_paths` (config UI → **CLI Settings → Browser Paths (web_search)**, or edit `config.json` directly). Set `FYC_CRAWL_MAX_CHARS` to change the per-page truncation budget (default `24000`, clamped to `2000`–`200000`). Set `FYC_CRAWL_LOCALE` (default `en-US`) and `FYC_CRAWL_TIMEZONE` (default `UTC`) to change the locale and IANA timezone the crawler's browser reports; malformed values fall back to the defaults. The User-Agent and client hints are derived from the version of the browser that is actually launched, so they match what the page can read from JavaScript.
- For sandboxed `sh` on Linux: `bwrap` / `bubblewrap` available on `PATH`.
- For the optional `computer` tool (disabled by default): Linux/X11, Linux/Wayland (through the XDG desktop portals, with a one-time compositor consent dialog), Windows, or macOS with Accessibility + Screen Recording permissions granted, plus a provider that accepts images in tool results (Anthropic, Gemini 3, or a Responses-compatible endpoint).
- On Windows, the sandboxed `sh` needs a one-time elevated `fuckyourcli sandbox setup` (without it, `sh` falls back to the older restricted-token engine). Command syntax and limitations (including the unavailable system TLS stack) are in [**Windows sandbox**](#windows-sandbox).
- For the optional `read_media` tool (disabled by default): a provider that accepts images in tool results (Anthropic, Gemini 3, or a Responses-compatible endpoint). No extra system dependency.
- For system-wide installation into `/usr/local/bin`: `sudo`.

## Build

```bash
./build.sh
```

The script reads the fyc `version` file and resolves the latest stable Codex compatibility version over HTTP (npm registry, then GitHub Releases). Both are injected through `-ldflags`; no Node, `npx`, or installed Codex is required. If HTTP fails, the tracked `codexversion/VERSION` is used. Build scripts do not modify that file; `release.sh` refreshes it atomically before committing/tagging, keeping it unchanged on network failure. Release CI also resolves the version without modifying tracked files. The binary is written to:

```text
build/fuckyourcli
```

Manual equivalent:

```bash
CODEX_VERSION=$(go run -buildvcs=false ./cmd/update-codex-version)
go build -buildvcs=false -ldflags="-X main.version=$(tr -d '[:space:]' < version) -X fuckyourcli/codexversion.BuildVersion=$CODEX_VERSION" -o build/fuckyourcli .
```

A plain `go build` embeds `codexversion/VERSION`, so the installed binary never needs a version file alongside it. For a pinned/reproducible build, resolve with `go run -buildvcs=false ./cmd/update-codex-version -offline`. Build/release resolution rejects missing or malformed fallback files; tests also validate the tracked embedded version. Running GoReleaser directly without `CODEX_VERSION` uses the embedded fallback.

Build matrix (the symbol extractor used by `context_snapshot` depends on cgo):

| GOOS | GOARCH | cgo | Symbol extractor |
| --- | --- | --- | --- |
| linux | amd64, arm64 | yes | tree-sitter (18 languages) + Go AST |
| darwin | amd64, arm64 | yes | tree-sitter + Go AST |
| windows | amd64, arm64 | no | Go AST + regex fallback |

`tree-sitter` is cgo-only and has no Windows grammar support, so the pure-Go fallback is selected automatically whenever cgo is unavailable. The fallback keeps full Go fidelity (`go/parser`) and approximates the remaining languages line by line; its tests live behind the same build tag and run with:

```bash
CGO_ENABLED=0 go test ./tools/
```

Cross-compilation checks (no C toolchain required for the fallback variants):

```bash
GOOS=windows GOARCH=amd64 go build -buildvcs=false ./...   # fallback
GOOS=windows GOARCH=arm64 go build -buildvcs=false ./...   # fallback
CGO_ENABLED=0 go build -buildvcs=false ./...               # fallback (any OS)
GOOS=darwin  GOARCH=arm64 go build -buildvcs=false ./...   # cgo off during cross builds → fallback
```

To keep the tree-sitter extractor in a cross-compiled binary, provide a matching C toolchain (`CGO_ENABLED=1 CC=<target>-gcc`); that path is currently wired for Linux and macOS only.

## Install

### Prebuilt binary (no toolchain needed)

Linux / macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/eduardogiraudi/fyc/master/setup.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/eduardogiraudi/fyc/master/setup.ps1 | iex
```

`setup.sh` and `setup.ps1` resolve the latest GitHub release, download the archive for the current OS/architecture, verify its SHA-256 against the release `checksums.txt`, extract it and install the binary — no Go toolchain and no build step. The default install directory is `/usr/local/bin` (Linux/macOS, using `sudo` only when it is not writable — the same target as `install.sh`) and `%LOCALAPPDATA%\Programs\fyc` (Windows); on Windows it is appended to the user `PATH`, on Linux/macOS the script prints the line to add to your shell profile when the directory is not in `PATH` yet. Pass `--user` to install into `~/.local/bin` instead, without `sudo`.

Options: `-v`/`--version <tag>` for a specific release, `-d`/`--dir <path>` for the install directory, `--system` to force the system-wide `/usr/local/bin` (the default; `-SystemInstall` installs into `%ProgramFiles%` and needs an elevated shell), `--user` for the per-user `~/.local/bin` without `sudo`, `-f`/`--force` to reinstall, `-n`/`--dry-run` to only print what would happen. When `setup.ps1` is piped into `iex` the same settings come from the environment: `FYC_VERSION`, `FYC_INSTALL_DIR`, `FYC_SYSTEM_INSTALL=1`, `FYC_FORCE=1`.

Both scripts then do the same post-install configuration as the source installers: the bundled skills (e.g. `make-skill`) are copied to `<config dir>/skills` (see [Runtime Files](#runtime-files)), the shell completion is installed (bash on Linux, zsh on macOS — registered in `~/.zshrc` — and PowerShell on Windows, activated in `$PROFILE`), and on Windows `fuckyourcli sandbox setup` runs once (one UAC prompt). On Linux, Ubuntu 24+ also gets the AppArmor profile for `bwrap` (required by the sandboxed `sh`; it needs `sudo`). Optional steps can be skipped with `--no-skills` / `--no-completion` and, on Windows, `-NoSkills` / `-NoCompletion` / `-NoPathUpdate` / `-NoProfileUpdate` / `-SkipSandboxSetup`. All installers honour `FYC_CONFIG_DIR`; Linux installers also honour an absolute `XDG_CONFIG_HOME`.

### From source (Linux / macOS)

```bash
./install.sh      # Linux
./install_mac.sh  # macOS
```

The installer builds as the current user, then uses `sudo` to copy the binary to `/usr/local/bin/fuckyourcli`. It also copies bundled skills (e.g. `make-skill`) to:

```text
Linux: ~/.config/fyc/skills
macOS: ~/Library/Application Support/fyc/skills
```

On Ubuntu 24+ it attempts to create an AppArmor profile for `bwrap`.

### Windows (from source)

```powershell
.\install.ps1
```

It builds as the current user (no administrator rights) and installs to `%LOCALAPPDATA%\Programs\fyc`, adding it to the user `PATH`. It also copies bundled skills (e.g. `make-skill`) to `%APPDATA%\fyc\skills` (or `<FYC_CONFIG_DIR>\skills`), writes a PowerShell completion script to `<install dir>\completions\fuckyourcli.ps1`, and wires it into your `$PROFILE` (creating the file when needed) so tab completion works out of the box. Because a `$PROFILE` is itself subject to the execution policy, when the *persistent* policy is `Restricted`/`AllSigned` the installer **asks** whether to set `RemoteSigned` at the current-user scope — stating clearly that the change is user-wide — and only does so if you accept (declining leaves the policy untouched and the completion simply stays unloaded; the `Bypass` used to launch the installer is intentionally ignored, since it would mask the persisted value). Overrides: `-InstallDir <path>` (or `$env:FYC_INSTALL_DIR`), `-SkipBuild`, `-NoPathUpdate`, `-NoProfileUpdate`. At the end it also runs `fuckyourcli sandbox setup` once (skip with `-SkipSandboxSetup`) to create the dedicated sandbox account and the `%ProgramData%` runner copy; the first run raises UAC. If script execution is blocked:

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

Note: on Windows the `sh` tool runs commands through `cmd.exe`, so command syntax follows `cmd`, not POSIX.

After installation:

```bash
fuckyourcli
```

For local development without installing:

```bash
./build/fuckyourcli
```

## Configuration

Start the interactive configuration UI:

```bash
fuckyourcli config
```

On a fresh install (no `config.json` yet, or one without any provider) **both `fuckyourcli` and `fuckyourcli config` open the same first-run prompt: pick a provider** — there is no built-in default model — and then ask whether to **start chatting**, open the **configuration** menu, or exit. Once a provider is configured, `fuckyourcli` goes straight to chat and `fuckyourcli config` opens the menu below.

Configuration is stored in the platform configuration directory:

```text
Linux:   ~/.config/fyc/config.json
macOS:   ~/Library/Application Support/fyc/config.json
Windows: %APPDATA%\fyc\config.json
```

`FYC_CONFIG_DIR` overrides the directory for all configuration files, fyc skills, preferences and persona. On Linux, `XDG_CONFIG_HOME` also overrides the default configuration root.

The configuration file and its directory are created with restricted permissions (0600 and 0700 respectively) to protect API keys and tokens.

The startup banner defaults to a compact **FYC** logo with the initial information on its right. Set `"compact_banner": false` in `config.json` to restore the full banner and the information below it. Existing configurations are migrated automatically; an explicit `false` is preserved.

### Secrets and `${VAR}` placeholders

Credentials are never stored in `config.json` in plain text. Every secret field holds a `${VAR}` (or `${VAR:-default}`) placeholder, and the concrete value lives in a sibling `auth.json` (mode 0600) or in the process environment:

- **`config.json`** keeps `${VAR}` / `${VAR:-default}` placeholders for `gemini_api_key`, `codex_access_token`, `codex_refresh_token`, every provider `api_key`, and — for MCP servers — the `endpoint`, each `args` entry, and every `headers`/`env` value.
- **`auth.json`** is a flat `{"VAR": "secret"}` store written next to `config.json` (override the location with `FYC_AUTH_FILE`).
- **Resolution precedence is `auth.json` first, then the environment**: the file is the source of truth and an env var is only a fallback when the file does not define that variable. The default of `${VAR:-default}` applies when the name is unset anywhere (or resolves to the empty string). Substituted text is never rescanned, and no shell is ever invoked. A `${VAR}` without a default that resolves to nothing is reported **once at startup**, naming the variable and the field it appears in.
- **Unreadable `auth.json`.** A store that cannot be parsed is reported once and **left untouched** — a save never overwrites it. In that state the save keeps the on-disk expressions and holds any changed or inline value directly in `config.json` (a *frozen* pass that writes nothing to `auth.json`), so no secret is silently lost; the value migrates into `auth.json` on the next load once the file is repaired.
- **Migration is automatic.** On the first load after the update, any secret still inline is moved to `auth.json` under a generated name (`FYC_GEMINI_API_KEY`, `FYC_OPENAI_<NAME>_API_KEY`, `FYC_MCP_<SERVER>_<KEY>`, …) and `config.json` is rewritten with the matching placeholder. A name chosen by hand is preserved verbatim as long as the field is not edited. A value that came from the environment is **never** copied into `auth.json` (the one exception is a rotated Codex token, which must be persisted).
- **Defensive ignore files.** On first use a `.gitignore` and a `.fycignore` are dropped into the config directory (only when they don't exist yet) listing `auth.json`, `auth.json.bak` and `config.json.bak`, so a `~/.config` copied to a git remote never leaks a secret.

The configuration flow can manage:

- active provider and model
- Gemini API key and model
- one or more OpenAI-compatible Chat Completions providers with name, base URL, optional API key, and default model
- one or more OpenAI Responses API-compatible providers
- one or more Anthropic Messages API providers
- OpenAI Codex OAuth tokens and model
- global temperature
- reasoning / thinking settings
- MCP server definitions, including enable/disable state
- built-in tool enable/disable state (`tool_states`)
- git permissions (`git.allow_write`, `git.allow_network`, `git.allow_destructive`, `git.allow_force_push`, `git.default_remote`), under **Git Permissions**
- language-server discovery overrides (`lsp.search_paths`, `lsp.binaries`, `lsp.disabled`). JSON only — see [**LSP servers**](#lsp-servers)
- extra browser executables for `web_search` (`browser_paths`), under **CLI Settings → Browser Paths (web_search)**
- optional delay between requests
- auto-compaction settings (enable/disable + estimated-token threshold), under **CLI Settings**
- pre/post tool-execution hooks (enable/disable + command), under **CLI Settings → Hooks**
- status-bar hook (per provider: on/off, command, include API keys/tokens, refresh interval, refresh on turn), under **CLI Settings → Status Bar Hook**
- base system prompt (persona), stored in `SYSTEM_PROMPT.txt` with `${VAR}` placeholders, under **CLI Settings**

Example OpenAI-compatible local provider:

```json
{
  "compact_banner": true,
  "provider": "openai_compatible",
  "model": "gemma4:31b-cloud",
  "openai_compatible_providers": [
    {
      "name": "Ollama",
      "base_url": "http://localhost:11434/v1",
      "model": "gemma4:31b-cloud"
    }
  ],
  "temperature": 0.1,
  "reasoning": {"mode": "effort", "effort": "high"},
  "compaction_enabled": true,
  "compaction_threshold": 32000,
  "compaction_mid_turn": false,
  "agents_md_enabled": true,
  "agents_md_max_bytes": 32768,
  "agents_md_fallback_filenames": ["CLAUDE.md", "GEMINI.md"],
  "hooks": {
    "pre_tool": {
      "enabled": true,
      "command": "echo \"$FYC_TOOL_NAME $FYC_TOOL_ARGS\" >> ~/.cache/fyc-hooks.log"
    },
    "post_tool": {
      "enabled": false,
      "command": ""
    },
    "status_bar": {
      "enabled": true,
      "command": "curl -s http://localhost:11434/api/show -d '{\"model\":\"gemma4:31b-cloud\"}' | jq -r '[.model_info | to_entries[] | select(.key | endswith(\".context_length\")) | .value][0] | \"ctx max: \\(.)\"'",
      "refresh_on_turn": true,
      "include_secrets": false
    }
  },
  "git": {
    "allow_write": false,
    "allow_network": false,
    "allow_destructive": false,
    "allow_force_push": false,
    "default_remote": "origin"
  },
  "lsp": {
    "search_paths": ["~/.local/bin"],
    "binaries": {"gopls": "/usr/local/go/bin/gopls"},
    "disabled": ["jdtls"]
  },
  "browser_paths": ["/opt/chromium/chrome"],
  "tool_states": {
    "web_search": true,
    "get_anchors": true,
    "git_status": true,
    "git_commit": false,
    "replace_anchors": true,
    "spawn_subagents": false,
    "lsp_active_servers": true,
    "lsp_hover": true,
    "lsp_definition": true,
    "lsp_references": true,
    "lsp_diagnostics": true,
    "lsp_workspace_symbols": true
  }
}
```

## OpenAI Codex (OAuth)

Codex authentication uses the OpenAI device flow and stores `codex_token_expires_at` in `config.json`; the `codex_access_token` and `codex_refresh_token` are kept as `${VAR}` placeholders backed by `auth.json` (see [**Secrets and `${VAR}` placeholders**](#secrets-and-var-placeholders)).

- **Mid-session refresh:** the token is checked before every request — main agent and each sub-agent — and refreshed proactively when it is expired or within 60 s of expiry, then persisted atomically. Refreshes are serialized, because Codex refresh tokens are single-use and parallel sub-agents would otherwise invalidate each other.
- **Reactive retry:** if the Responses API still answers `401` (a turn outliving the token), the provider refreshes once and retries instead of aborting the turn; a failed refresh is reported together with the original `401`.
- A refresh response that omits `refresh_token` never wipes the stored one.
- **Compatibility version:** model discovery resolves the latest stable Codex version using HTTP only, with a four-second total deadline. A successful lookup is cached for 24 hours; failures retry after five minutes. Offline/error fallback uses the last valid cached version, then the version injected at build time, then the embedded `codexversion/VERSION`; a version older than the build fallback is never selected. Cache failures are non-fatal. This version is shared by the model request's `client_version` query and `codex-cli/` User-Agent, and is distinct from fyc's release version.

## Usage

```bash
fuckyourcli             # start chat
fuckyourcli auto        # start chat with confirmation prompts disabled for this session
fuckyourcli config      # configure providers, models, and MCP servers
fuckyourcli --help      # show help
fuckyourcli --version   # show version and short binary SHA-256
fuckyourcli --hash      # print the full binary SHA-256
fuckyourcli serve       # start the AG-UI HTTP backend
fuckyourcli sandbox     # Windows: setup|status|reset the dedicated-user command sandbox
fuckyourcli status      # check for a newer public release
fuckyourcli update      # download, verify (SHA-256) and install the latest release
fuckyourcli models      # show the model catalog; 'models update' refreshes it from models.dev
```

`--hash` prints only the running executable's SHA-256: 64 lowercase hexadecimal characters followed by a newline, suitable for copying or scripts. It does not load configuration, start chat, or show a banner. If the executable cannot be read, it reports an error on stderr and exits with status 1 instead of printing a checksum.

The startup banner and `--version` keep their existing 16-character `sha:`; this is a binary hash, not a Git commit. Different builds from the same commit can have different hashes. Release `checksums.txt` covers the downloaded archives, so those checksums are not the value reported by `--hash` for the extracted binary.

Inside the chat:

| Command | Purpose |
| --- | --- |
| `/help` | Show available commands. |
| `/auto [on\|off]` | Toggle autonomous execution for the current session. |
| `Ctrl+C` | Stop the current generation (on macOS it is `Ctrl`, not `Cmd`). |
| `Ctrl+O` | Toggle autonomous execution without typing a command. |
| `/airplane [on\|off]` | Toggle `web_search` and web-dependent commands. Equivalent to disabling/enabling `web_search` in tool settings. |
| `/clear` | Clear chat context while keeping system messages. |
| `/delay <seconds>` | Persistently set delay before model requests. |
| `/temp [value]` | Show or set global temperature. |
| `/reasoning` | Auto wizard for thinking/reasoning: it resolves the active model's profile and lists only what that model can express — Default, Off, Adaptive (when supported) and one row per graded level (Minimal → Max), each showing the concrete request fields it sends. Custom effort, `extra_body` and the per-provider wire fields live under **Advanced**. Warns when the model is not recognised (the default sends nothing). |
| `/sub on\|off` | Temporarily enable or disable the `spawn_subagents` tool for the current session. Disabled by default. |
| `/tools` | Open the Enable/Disable Tools menu from chat, then return to chat. |
| `/hooks` | Open the pre/post tool-execution hooks editor from chat. |
| `/sysprompt` | Edit the base system prompt (persona) from chat. |
| `/compact` | Summarize the conversation into a structured handoff and replace the history with it. |
| `/model` | Switch model for the current provider. |
| `/provider` | Switch provider and model. |
| `/mode [name]` | List modes or activate a persistent mode. Use `/mode chat` to return to normal chat. |
| `/skills <name> <prompt>` | Inject a local skill into a one-off prompt. |
| `/sessions` | Manage saved chat sessions for this workspace through an interactive menu: switch, new chat, rename, and delete (multi-select with a confirmation step). |
| `/plan ...` | One-shot planning mode. |
| `/debug ...` | One-shot debugging mode. |
| `/deep ...` | One-shot deep research mode. Hidden/disabled when `web_search` is disabled, including when no supported browser is installed. |
| `/tutorial <question>` | Answer a question about using fyc with embedded documentation and no agent tools. Use `/mode tutorial` for a persistent tutorial conversation. |
| `exit` or `/exit` | Exit and save chat history. |

## Reasoning / thinking

`/reasoning` is a wizard over the **resolved profile** of the active model. It prints the detection (family, confidence, whether the models.dev catalog knows the model), the wire controls that profile exposes, and the exact fields the current selection sends now; then it offers one list: Default, Off, Adaptive when the model supports it, plus a row per graded level (`Minimal` → `Max`). A level is translated to whatever the model actually speaks — `reasoning_effort`, `output_config.effort`, a token budget or an adaptive toggle — and the row shows the concrete field it produces. Custom effort, `extra_body` and the raw per-provider wire fields are grouped under **Advanced**.

- A **local host** (`localhost`, `127.0.0.1`, …) is treated as a tolerant local inference server: it selects the *Local* profile even when the model name matches a known family (e.g. Ollama serving `qwen3:8b`), so the request sends `reasoning_effort` / `reasoning.effort` instead of a vendor-specific field the endpoint may ignore, and never asks for `reasoning.encrypted_content`.
- When a local endpoint sits in front of a proxy (LiteLLM, a gateway) that expects the *upstream* vendor's fields, the automatic choice is not enough: declare the placement per provider from `/reasoning → Advanced → Wire fields`. That override wins over both the built-in table and the catalog.

## Prompt caching

Anthropic requests ask for prompt caching in **two layers**. The **static prefix** — tool schemas and system prompt — is pinned with explicit `cache_control` breakpoints on the last tool and on the last system block, so it stays cached even when the tail diverges (a pruned screenshot image, a mode switch, compaction). The **growing conversation** is left to the server: a top-level `cache_control` enables automatic caching, where the API places the breakpoint on the last cacheable block and moves it forward as the conversation grows. Either way the marked prefix is written once and read at a fraction of the input price (and with less prefill latency) on every later turn.

- **Always on, no configuration.** Every Anthropic provider gets the breakpoints, custom `base_url` included. The request carries at most three of the API's four allowed breakpoints (last tool, last system block, automatic tail), and the `system` prompt is sent as a block list with one block per system message so its tail can be marked. A thinking block, an empty text block and a `tool_result` with no content are never marked. The breakpoint only pays off above the model's minimum cacheable length (1024 tokens on most current models, 4096 on Haiku 4.5); below it the request simply stays uncached, with no error.
- **Graceful three-level fallback.** An Anthropic-compatible endpoint with an older schema may refuse the field. Each refusal is remembered for the rest of the process and steps the request down one level: automatic + static breakpoints → explicit breakpoints only (the conversation tail is pinned explicitly instead, for a gateway with no top-level field) → no `cache_control` at all. The body is rebuilt and retried at each step, and any `cache_control` typed through `/reasoning → Advanced → Extra body` is dropped too, so a refusal never costs the turn. A refusal that does not name the field is reported as a normal API error. `FYC_ANTHROPIC_NO_CACHE=1` opts out up front.
- **Visible in the stats.** The token line shows `cache hit:` (input tokens read from the cache) and `cache write:` (tokens written by this request); conversation logs record the write count as `cache_write_tokens`.
- Compaction rewrites the history, which invalidates the cached conversation prefix; the next turn writes it again.

## Auto-compaction

Long sessions can grow past the model's context window. Auto-compaction summarizes the conversation into a structured handoff. By default it runs only before the first model request of each user turn, preserving the historical replacement format (system prompt, summary and acknowledgement).

Configure it under **CLI Settings → Auto Compaction**:

- **Auto Compaction:** toggles `compaction_enabled` (default `false`).
- **Compaction threshold:** adjusted with ◀/▶ (step `1000` tokens, range `1000`–`1000000`), persisted as `compaction_threshold` (default `32000`).
- **Mid-turn compaction:** separate opt-in switch, `compaction_mid_turn` (default `false`, including migrated configurations). Requires **Auto Compaction** to be enabled. Checks before subsequent model requests too, only after every result in a tool batch has been recorded; never while streaming, executing tools or awaiting an AG-UI interrupt resume.
- **Back** saves all settings; `q`/`esc` discards changes.

With mid-turn compaction enabled, automatic compaction keeps permanent system/developer instructions, the latest user request verbatim, active mode instructions and a recent suffix of complete exchanges. The recent suffix has a soft budget of one quarter of the configured threshold, capped at `8000` estimated tokens; a tool-call/result group is never split. An oversized group can be summarized as a whole, so a long active turn need not be retained in full. The replacement has no synthetic acknowledgement and the active task continues immediately.

Before the summary is requested, the transcript handed to the compactor is pruned of duplicates: when a read-only snapshot tool (`read`, `get_anchors`, `ls`, `grep`, `context_snapshot`) was called again later with the same target, the earlier results become a one-line note and only the latest one is kept. Error results and short outputs are never replaced, and every result keeps its role, name and `tool_call_id`, so the call/result pairings stay valid. The pruning applies **only** to the summarization request: the live history and the saved session are untouched, so nothing is lost that compaction was not already replacing.

The context size is **always estimated locally** (`tokens.go`: characters/4 for text, area-based image cost, tool schemas included). Provider token counts are informational only. The threshold is an input-context safety margin, **not** the model's hard context limit: configure it low enough to reserve output tokens and provider framing overhead.

Compaction uses the active turn's cancellation context and commits history only after a nonempty summary with no tools, interruption, provider error or explicit unsuccessful finish reason. Compatible backends may omit `finish_reason` / `stop_reason`; missing metadata alone does not reject a summary, but explicit truncation or failure does. Failed or canceled summaries leave history untouched. Active mode instructions are restored before the next model request. In-turn candidates that do not reduce context are rejected; a target below 75% of the threshold and at most two attempts per user turn prevent immediate repeated compaction. Failures or insufficient summaries suspend automatic retries for the current context until its estimated size falls below the reset target.

`/compact` remains available manually and keeps the historical full-history replacement format; it does not enable mid-turn compaction.

## Tool Hooks (pre/post)

Tool hooks are user-configured shell commands executed around **every** tool call (built-in tools, MCP tools, and sub-agent tool calls).

Configure them from the config UI under **CLI Settings → Hooks**, or from chat with `/hooks`. Both hooks have an `enabled` switch and a `command` string.

- **Pre-tool hook** runs before the tool. If it exits with a non-zero status (or times out), the tool is **not executed** and its combined output is returned to the model as an error, so the model can react. On success its output is discarded.
- **Post-tool hook** runs after the tool, for side effects only (logging, notifications, dashboards…). Its exit status and output are deliberately ignored: they are neither added to the model context nor written to the CLI output.

Contract passed to each hook:

- Environment: `FYC_HOOK` (`pre_tool`/`post_tool`), `FYC_TOOL_NAME`, `FYC_TOOL_ARGS` (raw JSON arguments). The parent environment is inherited.
- stdin: a JSON payload `{"hook":"pre_tool","tool":"sh","arguments":{...}}`; the post hook additionally includes `"result"` with the tool output.
- Working directory: the project root (the restricted root).
- Timeout: 30 s per invocation; captured output is capped at 10 KB.

Hooks run through the platform shell (`sh -c` on Unix, `cmd /C` on Windows) **outside** the `sh` tool sandbox to give the user total control: treat them as code you trust. Hooks are completely inaccessible to the agent, as is the `config.json` that contains them; they cannot be reached or modified from the agent's process. Disable a hook (or leave its command empty) to make it a no-op.

## Status bar hook

The status-bar hook renders a custom line under the token stats, so you can keep an eye on your provider balance ("credits: $4.82") or any other number — customised **per provider**.

Configure it from the config UI under **CLI Settings → Status Bar Hook**: pick a provider (or **All providers (default)**), then edit its hook. The editor exposes:

- **Enabled:** the on/off switch for the selected hook.
- **Command:** the shell command to run.
- **Include API keys/tokens:** when on, the active API key/token is exported to the command as `FYC_API_KEY`. Off by default — a secret is never handed to a command unless you ask.
- **Refresh interval (s):** background refresh period; `0` disables it.
- **Refresh on every turn:** re-run the command after each assistant turn.
- **← Back:** applies the changes and returns to the provider selector.

Contract passed to the command:

- Environment: `FYC_HOOK=status_bar`, `FYC_PROVIDER` (human-readable), `FYC_PROVIDER_KEY` (the key used to select the hook), `FYC_PROVIDER_TYPE`, `FYC_MODEL`, `FYC_BASE_URL`, plus `FYC_API_KEY` when **Include API keys/tokens** is on. The parent environment is inherited.
- stdin: a JSON payload with the same provider information (never the secret).
- Working directory: the project root (the restricted root).
- Timeout: 8 s per invocation; only the **first non-empty stdout line** is shown (capped at 200 runes).

Error handling is deliberate: a failing command (non-zero exit), a timeout or an empty result is **silent**. The last successful value stays on screen; when there is no previous value (e.g. the first turn fails) nothing is shown. Switching provider drops the remembered value, so a value produced for one provider is never attributed to another. Errors are never rendered in the bar — a command that wants feedback can log to a file itself.

JSON fields: `hooks.status_bar` (the hook that applies to every provider without an override, i.e. **All providers (default)**) and `hooks.status_by_provider` (per-provider overrides, keyed by the active provider name or, as a fallback, its type). Each hook is `{"enabled", "command", "interval_seconds", "refresh_on_turn", "include_secrets"}`; the `enabled` flag of the selected entry is its on/off switch. There is no slash command: configure it from `fuckyourcli config` or by editing `config.json` directly.

## System prompt (persona)

The top of the system prompt — the "persona" preamble (identity line, current date/working directory/OS, and the user-preferences header) — is user-editable. Everything else stays dynamic: the tool-derived `<rules>` block is still rebuilt from the enabled tools, and preferences are still loaded from disk.

Edit it from the config UI under **CLI Settings → System Prompt (persona)**, or from chat with `/sysprompt`. The stored template lives at `SYSTEM_PROMPT.txt` (see [**Runtime Files**](#runtime-files)); when the file does not exist the built-in default is used, so behaviour is unchanged out of the box. The submenu offers **Edit in &lt;editor&gt;** — the label names the editor that will actually be launched (honours `FYC_EDITOR`, `$VISUAL`, `$EDITOR`, then falls back to `nano`/`vim`/`vi`, and shows `$EDITOR` only when nothing resolves) — **Preview rendered prompt**, and **Reset to default**.

Placeholders use the `${VAR}` form:

- **Only environment variables are substituted — no shell invoked.** The value of `VAR` is written verbatim and the substituted text is never re-processed.
- Built-in variables provided by the CLI: `${FYC_TODAY}` (formatted current date), `${FYC_CWD}` (working directory), `${FYC_OS}`, `${FYC_SHELL}` (the OS-specific shell note — `cmd.exe`/PowerShell/Schannel on Windows, the tmpfs sandbox elsewhere) and `${FYC_PREFERENCES}` (the contents of `PREFERENCES.txt`).
- Any other `${VAR}` is read from the process environment; **if the variable is unset the placeholder becomes an empty string**.
- **This template never reads `auth.json`.** The persona lookup is the process environment only; the secret store is deliberately kept out of it, so a `${VAR}` here can never pull a credential out of `auth.json`.
- Example: `You are a graphic designer. Today is ${FYC_TODAY}.`

The default template is:

```text
You are an agentic CLI assistant. You operate directly on the filesystem inside a restricted sandbox. You act, verify, and report — you do not explain unless asked.

Today: ${FYC_TODAY} | Working directory: ${FYC_CWD} (writes restricted here) | OS: ${FYC_OS} | ${FYC_SHELL}

User Preferences:
${FYC_PREFERENCES}
```

## Project instructions (AGENTS.md)

`fuckyourcli` reads repository instruction files and injects them into the system prompt as a single `<project_context>` block, so a repo's conventions reach the model automatically.

- **Chain, repo root → cwd:** it walks from the repository root (the first ancestor containing a VCS marker, `.git`/`.hg`) down to the working directory, taking **at most one file per level**. Shallow files come first, deeper ones are appended. When no VCS marker exists only the working directory is scanned, so directories above the project are never injected.
- **Precedence per directory:** `AGENTS.override.md` > `AGENTS.md` > the fallbacks (`CLAUDE.md`, then `GEMINI.md` by default). Empty files are skipped and the next candidate is tried.
- **Byte budget:** the combined content is capped (default `32768` bytes). The file crossing the budget is truncated and marked `(truncated)`; deeper levels are dropped entirely.
- **Rendering:** the chain becomes one `<project_context>` block whose header states it is *context only* — the user's prompt and the dynamic `<rules>` block keep precedence. The block is omitted when no instruction file is found, leaving the prompt unchanged.

Enabled by default. It is not exposed in the config UI; the JSON fields are `agents_md_enabled`, `agents_md_max_bytes`, and `agents_md_fallback_filenames`.

## Modes

`fuckyourcli` includes dedicated prompt modes in `modes/`:

- **Plan** (`/plan`): inspect the project and propose an implementation plan before changing files.
- **Debug** (`/debug`): investigate a bug, explain the root cause, and propose a fix.
- **Deep Research** (`/deep`): perform multi-step web research with source verification. It requires `web_search`; when `web_search` is disabled through `/airplane` or `/tools` — or when no supported browser is installed — `/deep` is hidden/blocked. When sub-agents are enabled, this mode adds a stricter multi-round sub-agent research protocol.
- **Tutorial** (`/tutorial`): answer questions about using fyc from its embedded product documentation. It exposes no tools, so it cannot inspect, execute, or modify the workspace.

Modes can be used one-shot with their prefix or made persistent with `/mode`. For example, `/tutorial How do I configure reasoning?` answers one question in tutorial mode; `/mode tutorial` keeps it active for follow-up questions, and `/mode chat` returns to normal chat.

Each mode also carries its **own tool set**, applied on top of the tools enabled in the settings:

- **Plan** and **Debug** hide the tools that mutate the workspace (`write`, `replace_anchors`, `delete`, `make_dir`, `copy`, `move`, `rename`); read-only tools and `sh` stay available. These modes are not truly read-only because `sh` is kept as a multi-purpose tool for advanced investigation and planning — for instance, to run an inline Python script to verify the schema of a large JSON — before proposing formal changes. Note that any tool (including `sh`) can be disabled via `/tools`, and in any conflict between a mode's tool set and the user's configuration, the user's setting always wins.
- **Deep Research** exposes only `web_search` and `spawn_subagents`.
- **Tutorial** exposes no tools and is conversational only.

The mode tool set is **idempotent with respect to the settings**: it only ever *narrows* the configured tools. A tool disabled in `tool_states` (or through `/tools`, `/sub`, `/airplane`) is **never re-enabled** by a mode — the user setting always wins. Leaving a persistent mode (`/mode chat`) or reaching the end of a one-shot turn restores the normal tool set.

## Built-in Tools

The model receives built-in tools declared in `globals.go` and executed by `tools.ExecuteTool`. MCP tools are appended at startup after configured servers initialize.

Main groups:

- **Web**: `web_search` drives an installed local browser through Playwright. It searches Startpage by default, with `duckduckgo` and `brave` alternatives, or crawls a provided URL. When a page is behind the Anubis anti-bot challenge (used by Startpage and Brave), the crawler waits for the client-side proof-of-work to complete and then extracts the real content instead of the "Verifying your request…" interstitial. **Long pages are truncated** so a single crawl cannot blow up the model context: the result is returned one window at a time (JSON fields `page`, `pages`, `total_chars`, `truncated`, `next_page`), and the rest is read by calling `web_search` again with another `page` (sliding window). The optional `max_chars` parameter overrides the per-page budget (default `24000`). Page 1 of a multi-page document also carries an `outline` (headings with the page they start on; `outline_truncated` when it was cut). The optional `find` parameter searches the whole document case-insensitively and returns the matching `pages` with short snippets and no content, so the model can jump straight to the right `page`. A `page` outside `1..pages` is an explicit error, never a silent fallback to the last page. The converted markdown is also cleaned to save tokens: tracking query parameters (`utm_*`, `fbclid`, `gclid`…), empty links, `data:` URIs and very long URLs are removed or shortened. Requests to well-known ad, analytics and session-replay hosts are blocked (never for the site being crawled), and cookie-consent banners are accepted automatically (banners inside cross-origin iframes are not reached).
- **Project inspection**: `context_snapshot`, `ls`, `read`, `grep`.
- **Output caps**: the tools that can read an unbounded amount of content are bounded so a single answer cannot swamp the model context. `read` and `get_anchors` return at most 2000 lines / 100 KB per call (`offset` is the 1-based first line, `limit` the max lines; the file is still cached whole for anchored edits), `grep` returns at most 200 matches / 64 KB (`head_limit` up to 1000), on top of the existing caps of `sh` (32 KB), `sniff` (16 KiB), git (200 KB), `context_snapshot` (30 KB) and `web_search` (paged). Every cut is announced on the **first line** of the result with the totals (`[read: lines 1-2000 of 51234 …]`, `[grep: showing 200 of 1832 matches …]`), so a truncated answer is never mistaken for a complete one.
- **Editing/filesystem**: `get_anchors` (read file with anchors), `replace_anchors` (apply changes via anchors), `write`, `make_dir`, `delete`, `copy`, `rename`, `move`.
- **Anchored Editing Workflow**: The system prioritizes `get_anchors` to establish line anchors (`Word:hash`). `replace_anchors` then uses these anchors to preview and apply changes, guaranteeing that the AI operates on the correct, current version of the file.
- **Execution**: `sh` returns as soon as a command finishes, with its output and exit status. With `persistence:false` (default), it waits for completion, cancellation or a timeout: `timeout_seconds` defaults to `120` and accepts integers from `1` to `300`; timeout/cancellation terminates the process and returns an explicit error with the collected output, never a background PID. With `persistence:true`, the timeout parameter is ignored: a command still running after 10 seconds is registered and kept alive across turns for `sniff`/`kill`; a command that finishes earlier returns immediately without registration. A persistent process keeps writing into a bounded in-memory tail buffer (last 512 KiB), and `sniff` reads it back as a log tail: it **never returns the whole log** — `last_n` (default `50`, max `500` lines), `max_bytes` (default `16 KiB`, max `64 KiB`) and an optional `pattern` regex (keeps only matching lines, each prefixed with its line number) are always enforced together.
- **MCP**: `list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource`, plus dynamically loaded MCP tools named as `<server>__<tool>`.
- **Skills**: `skills_discovery`, `read_skill`. The tool searches for skills in the following order (project-level takes priority): `<config dir>/skills` (see [Runtime Files](#runtime-files)), `~/.agents/skills`, `~/.claude/skills` (global), `[root]/.claude/skills`, `[root]/.fyc/skills`, and `[root]/.agents/skills`.
  - Skills in `<config dir>/skills` are intended for fyc-specific workflows, whereas those in shared directories (like `~/.agents/skills` or `~/.claude/skills`) are meant to be harness-agnostic and shared across different AI agents. Shared directories use the user home (`%USERPROFILE%` on Windows); they do not require `HOME` to be set on Windows.
- **Interaction**: `questionary`.
- **Preferences**: `read_user_preferences`, `update_user_preferences`.
- **LSP**: `lsp_active_servers`, `lsp_hover`, `lsp_definition`, `lsp_references`, `lsp_diagnostics`, `lsp_workspace_symbols`. These are runtime-auto-disabled when the workspace has no compatible language server available; a 30-second heartbeat keeps rescanning and re-enables them if supported servers/languages appear. See [**LSP servers**](#lsp-servers) for discovery and the `lsp.*` configuration.
- **Git**: `git_status`, `git_diff`, `git_log`, `git_show`, `git_blame`, `git_ls_files`, `git_config`, `git_add`, `git_commit`, `git_switch`, `git_restore`, `git_stash`, `git_init`, `git_branch`, `git_remote`, `git_fetch`, `git_pull`, `git_push`, `git_clone`, `git_reset`, `git_merge`, `git_rebase`, `git_cherry_pick`, `git_revert`, `git_tag`. Only the read-only ones are enabled by default; see **Git tools** below.
- **Delegation**: `spawn_subagents`, disabled by default and exposed only when enabled persistently through `/tools`/config or temporarily with `/sub on`.
- **Computer use**: `computer` (screenshot, mouse, keyboard on the real desktop), implemented natively in Go on Linux, Windows and macOS (no external tools, no cgo), disabled by default; on Linux it works on X11 as well as on Wayland through the XDG desktop portals. It is hidden automatically when the active provider cannot receive images inside tool results (OpenAI Chat Completions, Gemini < 3) and a notice explains how to switch provider. Sub-agents never receive it.
- **Media**: `read_media` reads an image file (PNG, JPEG, GIF) from the workspace and attaches it to the tool result so the model can see it; large images are downscaled automatically. Disabled by default; hidden automatically when the active provider cannot receive images inside tool results (OpenAI Chat Completions, Gemini < 3).

Tool state is persisted in `tool_states` inside `config.json` and can be managed via the config UI or directly from chat using `/tools`. Missing entries use defaults: enabled for every built-in tool except `spawn_subagents`, `computer`, `read_media` and every git tool that can mutate the repository (only read-only git tools are enabled). Disabling `get_anchors` also disables `replace_anchors`. Disabling `web_search` activates the same restrictions as `/airplane on` and removes web-search instructions from the crafted system prompt.

LSP tools have an additional runtime availability layer: even if enabled in `tool_states`, they are hidden from the model when no relevant LSP server is discovered for the current workspace. The app rescans every 30 seconds and exposes them automatically when compatible servers/languages become available. If all LSP tools are manually disabled in `/tools`/config, this heartbeat stops.

`web_search` has its own runtime availability layer: even if enabled in `tool_states`, it is hidden from the model whenever no supported browser is found (the lookup follows `FYC_BROWSER_PATH`, then `browser_paths`, then the built-in locations). Availability is re-checked at startup and every 30 seconds, so installing a browser or adding its path makes the tool reappear; while it is hidden, a startup notice and the crafted system prompt explain the state.

The system prompt is crafted dynamically from enabled tools: web, skills, and anchored-edit rules are included only when the related tools are available. User preferences are still loaded into the prompt when present, and the `read_user_preferences`/`update_user_preferences` guidance (including the "pre-loaded, do not call it early" note) is emitted only while that tool is enabled. The base "persona" preamble is user-editable (see **System prompt (persona)**).

Most file mutations, command execution, MCP tool calls, and other sensitive actions ask for confirmation unless auto mode is active.

## LSP servers

When a workspace contains a supported language, the LSP tools start the matching language server and expose it to the model. Servers are discovered automatically, and the resolution order is **user configuration first** — anything you set in `config.json` wins over the auto-detected sources:

1. `lsp.binaries` — an explicit executable per server name.
2. `lsp.search_paths` — extra directories scanned for the server binary.
3. the system `PATH`.
4. Neovim Mason (`~/.local/share/nvim/mason`, `%LOCALAPPDATA%\nvim-data\mason`).
5. VS Code extensions.
6. Zed's languages cache (Linux `$XDG_DATA_HOME/zed/languages`, macOS `~/Library/Application Support/Zed/languages`, Windows `%LOCALAPPDATA%\Zed\languages`).

`lsp.disabled` lists server names that must never be started (e.g. a heavy server you do not want in this project). Names are canonical (the key of the table in `lsp/discovery.go`, e.g. `gopls`, `rust-analyzer`, `pyright`, `tsserver`, `jdtls`).

Example:

```json
"lsp": {
  "search_paths": ["~/.local/bin"],
  "binaries": {"gopls": "/usr/local/go/bin/gopls"},
  "disabled": ["jdtls"]
}
```

A leading `~` in `search_paths` and `binaries` is expanded to the user home. These fields are **not** exposed in the config UI yet: edit `config.json` directly. A bad path is ignored rather than fatal, so a typo simply falls through to the next source. On Windows the launcher extension is optional: a `binaries` entry written without `.exe`/`.cmd`/`.bat` is resolved like the search paths.

## Git tools

`fuckyourcli` ships dedicated `git_*` tools instead of shelling out through `sh` (the command sandbox masks `.git`), so the model gets real version control with explicit, per-tier permissions.

### Permission tiers

| Tier | Tools | Default | Permission |
| --- | --- | --- | --- |
| Read-only | `git_status`, `git_diff`, `git_log`, `git_show`, `git_blame`, `git_ls_files`, `git_config` | enabled | none (no prompt) |
| Local write | `git_add`, `git_commit`, `git_switch`, `git_restore`, `git_stash`, `git_init`, `git_branch`, `git_remote` | disabled | `git.allow_write` |
| Network | `git_fetch`, `git_pull`, `git_push`, `git_clone` | disabled | `git.allow_network` |
| Destructive | `git_reset`, `git_merge`, `git_rebase`, `git_cherry_pick`, `git_revert`, `git_tag` | disabled | `git.allow_destructive` |

- **Every tool that can mutate the repository starts disabled**, because a tool that is not in the model's tool list cannot be reached through `/auto` either. Enable the ones you want from `/tools` (or the config UI); the read-only group is already on.
- When a mutating tool is enabled but its tier is not yet granted, the app asks with **Allow / Deny / Allow always / Deny with suggestion**; "Allow always" persists the tier in `git.allow_*`. Destructive prompts put **Cancel** first, so a bare Enter refuses the operation. Every tier prompt also offers **See details**, listing the tier, the action and the exact `git` command (argv) that will run, plus the workspace root.
- `git reset --hard` additionally asks for a typed confirmation (`RESET`) outside auto mode because it discards working-tree changes.
- **Force push:** `git_push` accepts `force_with_lease` only when `git.allow_force_push` is `true`. Plain `--force` is never offered, and this rule holds even in auto mode.
- Mixed tools report the tier of the single action: `git_branch`, `git_remote`, `git_stash` and `git_tag` with `action=list` are read-only (no prompt), while create/delete/rename need the write tier (`git_tag` create/delete and a forced branch delete need the destructive one).
- The remote used by `git_fetch`/`git_pull`/`git_push` defaults to `git.default_remote` (fallback `origin`).

### Execution model

- `git` is invoked directly with an argv (no shell), from the workspace root, with a sanitized environment: `GIT_TERMINAL_PROMPT=0` (never blocks asking for credentials), no pager, no editor (`GIT_EDITOR=true`, `GIT_SEQUENCE_EDITOR=:`), `GIT_MERGE_AUTOEDIT=no`, `LC_ALL=C`, and `GIT_DIR`/`GIT_WORK_TREE`/`GIT_CONFIG_*` stripped so the parent environment cannot redirect git elsewhere.
- Timeouts: 30 s for local operations, 5 minutes for network ones. Output is capped (200 KB / 2000 lines) and credentials are redacted (URL userinfo, `token=`/`password=` pairs, `Authorization` headers).
- Paths supplied by the model must resolve inside the workspace (traversal, absolute paths outside it and `.git` internals are rejected) and are always passed after `--`; revisions, branch names and `stash@{n}` references are validated against a conservative character set, so an argument can never be mistaken for an option.
- Commit messages are piped through stdin (`git commit --file -`): no shell, no temporary files.
- Git tools are hidden when `git` is not installed. Outside a repository only `git_init` and `git_clone` stay visible, so the model can bootstrap one. Availability is refreshed at startup, on `/tools` and every 30 seconds.
- **Sub-agents never receive git tools:** `tools/subagents_policy.go` filters them out of the tool list handed to a sub-agent and blocks them again at dispatch time, so repository-wide operations always stay under the main agent's permission prompts.

## MCP Servers

MCP servers are configured through:

```bash
fuckyourcli config
```

Supported transports:

- `stdio`: command, arguments, and environment variables
- `sse`: HTTP SSE endpoint with optional headers
- `streamableHttp`: streamable HTTP endpoint with optional headers

Configured MCP servers can be enabled/disabled from the config UI. MCP resource tools can also be hidden from the AI through the global Enable/Disable Tools menu. On startup, the app initializes enabled MCP servers, loads their tools, prefixes each tool name with the server name, and retries once on EOF / client-closing errors by reconnecting the session.

## Runtime Files

`<config dir>` is `store.ConfigDir()`: `~/.config/fyc` on Linux, `~/Library/Application Support/fyc` on macOS, and `%APPDATA%\fyc` on Windows. `FYC_CONFIG_DIR` overrides it on every platform; Linux also honours an absolute `XDG_CONFIG_HOME`. Configuration, secrets, the model catalog, fyc skills, preferences and the persona all use this directory.

| File | Purpose |
| --- | --- |
| `<config dir>/config.json` | Persistent app configuration, versioned (`schema` field). Secrets are stored as `${VAR}` placeholders. |
| `<config dir>/auth.json` | Secret store: `{"VAR": "secret"}` values backing the `${VAR}` placeholders in `config.json` (0600). |
| `<config dir>/.gitignore` · `.fycignore` | Auto-created (only when absent) ignore files listing `auth.json` and the `.bak` copies, so a backup of the configuration directory never commits a secret. |
| `<config dir>/PREFERENCES.txt` | Global user preferences loaded into the system prompt. |
| `<config dir>/SYSTEM_PROMPT.txt` | User-editable base system prompt (persona) with `${VAR}` placeholders. |
| `<config dir>/skills/`, `~/.agents/skills/`, `~/.claude/skills/`, `[root]/.claude/skills/`, `[root]/.fyc/skills/`, `[root]/.agents/skills/` | Installed skills. |
| `<config dir>/models.json` | The models.dev catalog. `fuckyourcli models update` overwrites the installed copy; `./update_catalog.sh` refreshes the bundled `models/catalog.json` for releases. The installers copy the bundled catalog here. Missing/empty is fine: `/reasoning` simply shows no catalog line. |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/codex-version.json` | Stable upstream Codex compatibility version and last successful check; uses the platform cache directory (`FYC_CACHE_DIR` override), with atomic writes and `.bak` recovery. |
| `~/.fyc_history` | Prompt history, capped at the most recent 500 entries with consecutive duplicates collapsed. |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/<cwd-hash>/index.json` | Per-workspace session index: one entry per chat (id, title, timestamps, message count). |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/<cwd-hash>/sessions/<id>.json` | One versioned file per chat session; the most recent 50 are kept. |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/<cwd-hash>/blobs/<sha256>` | Content-addressed image payloads referenced by messages (garbage-collected when unreferenced). |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/<cwd-hash>/chat.json` | Legacy single-file chat history: migrated into `sessions/` on first read, then renamed `chat.json.migrated`. |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/logs/<session-id>/events.jsonl` | One diagnostic stream per conversation, shared by its main agent, sub-agents and compaction requests. Resuming appends to the same stream. |
| `${XDG_CACHE_HOME:-~/.cache}/fyc/logs/<session-id>/responses/<request-id>.json` | Normalized response dumps, only when `FYC_LOG_DEBUG=1`; not raw HTTP payloads. |
| `%ProgramData%\fyc\runner\fuckyourcli.exe` *(Windows)* | Read+execute copy of the binary the sandbox runs as its runner; installed by `fuckyourcli sandbox setup`, removed by `fuckyourcli sandbox reset`. |
| `%USERPROFILE%\.fyc\.sandbox\` *(Windows)* | Sandbox setup marker (`setup.json`) and setup log; `%USERPROFILE%\.fyc\.sandbox-secrets\sandbox_users.json` holds the DPAPI-protected credentials. |

Application cache directories also use `fyc`: `~/.cache/fyc` on Linux, `~/Library/Caches/fyc` on macOS and `%LOCALAPPDATA%\fyc` on Windows. `FYC_CACHE_DIR` overrides the cache directory on every platform; Linux also honours an absolute `XDG_CACHE_HOME`. Shared skills and prompt history remain home-relative.

Versioned session/configuration state is written atomically (temp file + `fsync` + `rename`), with `.bak` recovery. Diagnostic events are appended under an advisory lock and flushed; an unfinished last line left by a crash is discarded before the next append.

### Conversation logs

Every provider round trip emits `request_started` and `request_finished` or `request_failed`, correlated by a unique `request_id`. HTTP attempt events carry status codes, retry delays and a request-scoped attempt number that stays continuous across Codex authentication refreshes. Records include `session_id`, provider type/name, model, agent role (`main`, `subagent`, `compaction`), duration, tokens, finish reason and interruption status. Errors are classified: original provider errors can contain authenticated URLs or response bodies, so their text is never persisted.

The session identity is allocated before the first request. A resumed TUI conversation keeps that identity; a new chat gets a new directory. AG-UI uses a stable, filesystem-safe identity derived from the workspace and thread ID, including after server restarts. Sub-agents inherit their parent's conversation and add their own agent ID.

By default no prompt, response text, reasoning, tool arguments, HTTP headers or URLs are written to diagnostic logs. For troubleshooting, start with `FYC_LOG_DEBUG=1 fuckyourcli` (PowerShell: `$env:FYC_LOG_DEBUG='1'`). Debug dumps contain normalized response text/reasoning/tool calls, not requests or HTTP frames; opaque reasoning state/signatures are excluded. Active provider credentials and sensitive argument keys are redacted, but **arbitrary secrets in free text cannot be reliably detected**: review dumps before sharing them. Log directories/files are created with owner-only permissions (0700/0600 on Unix).

Retention prunes entire inactive conversation directories, oldest first: 30 days since the last activity or 100 MiB total. Scans run after provider requests, at most once every five minutes per log root; `logs/.retention` stores the timestamp shared across processes and restarts. The size limit can temporarily be exceeded between scans or while provider requests are active; live requests are protected across processes. Deleting/pruning a saved TUI session also removes its log set (best effort; an active set is preserved). Logging failures are reported without aborting provider calls; root-lock contention waits are capped at two seconds per acquisition. Historical flat `response_*.json` files are left untouched: their conversation cannot be reconstructed reliably, and they are not counted by the new retention policy; remove them manually if no longer needed.

## AG-UI backend

`fuckyourcli serve` exposes the agent over HTTP as an [AG-UI](https://docs.ag-ui.com/introduction) backend: a `POST` with a `RunAgentInput` body returns a `text/event-stream` of AG-UI events (`RUN_STARTED → TEXT_MESSAGE_* → TOOL_CALL_* → TOOL_CALL_RESULT → RUN_FINISHED`/`RUN_ERROR`). The provider/tool loop, the system prompt, and the tool set are the same as the interactive CLI — only the output sink changes (SSE instead of the terminal UI). 

```bash
fuckyourcli serve --addr 127.0.0.1:8787 --token <secret>
# or, preferred (the token does not show up in `ps`):
FYC_AGUI_TOKEN=<secret> fuckyourcli serve
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--addr` | `127.0.0.1:8787` | Listen address. Bind to `localhost` unless you have a proxy/TLS in front. |
| `--token` | — (`FYC_AGUI_TOKEN`) | Mandatory bearer token, compared in constant time. Required. |
| `--permissions` | `deny` | Headless approval policy for tools that would prompt: `deny` refuses, `auto` approves. |
| `--cors` | none | Allowed CORS origin (`*` allows any). |
| `--max-threads` | `64` | Maximum number of in-memory threads. |
| `--hitl` | `true` | Human-in-the-loop: a tool that would prompt asks the client instead of being denied. `--hitl=false` restores the deterministic `--permissions` policy. |
| `--interrupts` | `false` | Use the **level-2** lifecycle (standard AG-UI interrupts) instead of the level-1 round trip: the run ends with `RUN_FINISHED` carrying `outcome:{type:"interrupt"}`, and the client answers by starting a new run with `RunAgentInput.resume`. Requires `--hitl=true`. |
| `--interaction-timeout` | `5m` | Level 1: how long a parked interaction waits for the client answer before it is denied. Level 2: the per-interrupt TTL (`expiresAt`); a resume past it fails with `RUN_ERROR`. |
| `--state-dir` | none | Persist AG-UI threads (conversation + open interrupts) under this directory, one versioned JSON file per thread. Empty keeps the historical in-memory-only behaviour. |

Endpoints:

- `POST /` — run a thread. Body is a `RunAgentInput` (`threadId`, `runId`, `messages`; with `--interrupts` also `resume`; `tools`/`state`/`context`/`forwardedProps` are accepted and ignored). The response is an SSE stream of AG-UI events. `threadId`/`runId` are generated when omitted.
- `POST /runs/{runId}/interactions/{interactionId}` — answer a pending tool approval/question (`{"choice":0,"text":"","answers":[],"remember":false}`). Responds `204`; the parked tool resumes immediately. See **Human-in-the-loop** below.
- `GET /healthz` — liveness probe.

Behaviour notes:

- The **fyc system prompt is authoritative**: `system`/`developer` messages from the client are ignored. On the first run of a thread the conversation is seeded from `messages`; afterwards only the latest user message is appended, so fyc's in-memory history is never duplicated.
- `questionary` is hidden from the model in server sessions, and `computer`/`spawn_subagents` stay disabled by default. With `--hitl=false` nothing that requires a confirmation is executed under the default `--permissions deny`; with the default `--hitl=true` the client is asked first (see below).
- Tool hooks, if configured, run on **every** HTTP request's tool calls.
- Every event carries the ids the AG-UI schemas require: `messageId` on text and reasoning events (`REASONING_START`/`REASONING_END` included) and on `TOOL_CALL_RESULT`, `toolCallId` on the `TOOL_CALL_*` events, `id` + `content` on every `MESSAGES_SNAPSHOT` message. A client that validates the stream with `@ag-ui/core` rejects the whole run when one of them is missing.
- Threads live in memory by default; pass `--state-dir <dir>` to persist each thread (conversation, `seeded` flag, open interrupts and "remember" grants) as one versioned, atomically-written JSON file per thread. With a state directory, a thread suspended by an interrupt can be resumed after a `fuckyourcli serve` restart: its open interrupts are reloaded on the next run of that thread, and interrupts whose TTL elapsed while the process was down are dropped.

Human-in-the-loop (level 1, `--hitl`, default on):

- A tool that would open a prompt (`write`, `replace_anchors`, `sh`, `delete`, `copy`, `rename`, `move`, `make_dir`, MCP calls, git tiers, `computer`) parks and the server emits `CUSTOM {"name":"fyc.interaction_required","value":{interactionId, runId, threadId, kind, title, options, allowIndex, denyIndex, suggestionIndex, seeChanges, diff, diffStats, diffFilename, seeDetails, detailsTitle, details, expect, questions, timeoutMs}}` on that run's SSE stream. `title` is always the complete, authoritative prompt; `seeDetails`/`detailsTitle`/`details` are an **additive** payload a client may show on demand (the full command, the sandbox envelope, the exact `git` invocation) — clients that ignore them behave exactly as before.
- The client answers with `POST /runs/{runId}/interactions/{interactionId}`. `kind` is `approval` (answer `choice`, `remember`), `typed_confirm` (answer `text`) or `question` (answer `answers`), and a `deny with suggestion` answer travels back as `text`.
- `remember: true` (or picking the `Allow always` option) auto-approves later requests of the same kind for that thread; for file tools the memory is keyed on the target path. **Nothing is written to `config.json`**: the server never persists permissions, and per-thread memory is dropped with the thread.
- If nobody answers within `--interaction-timeout`, or the client disconnects, the interaction is denied (the tool returns "denied", exactly like `--permissions deny`); the server also emits `CUSTOM {"name":"fyc.interaction_resolved","value":{"interactionId":…,"status":"timeout|canceled|server_shutdown"}}`.
- While an interaction is parked the global tool mutex is released, so other threads keep making progress.

Human-in-the-loop (level 2, `--interrupts`):

- The run is **suspended** instead of parked: the server emits `MESSAGES_SNAPSHOT` (the conversation state at the boundary, as the protocol requires) and then `RUN_FINISHED` with `outcome:{"type":"interrupt","interrupts":[{"id","reason","message","toolCallId","responseSchema","expiresAt","metadata":{"fyc":…}}]}`, where `metadata.fyc` carries exactly the level-1 payload (kind, title, options, indices, diff, seeDetails/details, questions, …) so a level-1 rendering can be reused.
- `reason` is `tool_call` (an approval bound to a tool call: `toolCallId` is set), `input_required` (questionary) or `confirmation` (typed confirmations, and approvals without a tool call).
- The client answers with a **new run on the same thread** carrying `resume:[{"interruptId","status":"resolved"|"cancelled","payload":{…}}]`. The resumed run replays the tool calls the interrupted run left pending, emits only their `TOOL_CALL_RESULT` (never a second `TOOL_CALL_START`/`ARGS`/`END`, per the protocol's audit rule) and then continues until `RUN_FINISHED` with `outcome:{"type":"success"}`.
- `payload` shapes (declared by each interrupt's `responseSchema`): `{"approved":true|false}` for approvals, plus optional `choice` (index into `metadata.fyc.options`), `text` (denial suggestion) and `remember`; `{"text":"RESET"}` for typed confirmations; `{"answers":[…]}` for questionary. `status:"cancelled"` means the user abandoned: approvals and typed confirmations are denied, a questionary reports the error to the model.
- Contract rules enforced by the server: a thread with open interrupts accepts **only** runs that carry `resume`; every open interrupt must be addressed (a partial resume is a `RUN_ERROR`); `interruptId` must belong to that thread; a resume past `expiresAt` is a `RUN_ERROR`; replaying the same `(interruptId, status, payload)` is a no-op (idempotency).
- Interrupted tool calls are **replayed**, not restored mid-stack: the replayed request is matched to its answer by tool-call id (with the request identity as a fallback for providers that do not expose ids), so it never asks twice and never runs twice. A tool call that raises a second decision (e.g. a git tier followed by a typed confirmation) interrupts again after the first answer was applied.
- Interrupt records and "remember" grants live in memory per thread by default; with `--state-dir` they are persisted with the thread, so an open interrupt survives a restart until its TTL (`--interaction-timeout`) elapses, after which it is dropped.

Example:

```bash
curl -N http://127.0.0.1:8787/ \
  -H 'Authorization: Bearer <secret>' -H 'Content-Type: application/json' \
  -d '{"threadId":"t1","runId":"r1","messages":[{"role":"user","content":"list the files"}]}'
```

## Architecture

| Path | Role |
| --- | --- |
| `main.go` | Startup, CLI argument handling, interactive loop, slash commands, modes, `/tools`, `/airplane`, `/sub`, signal handling. |
| `globals.go` | Shared globals, restricted root initialization, tool enablement helpers, LSP/git/`web_search` runtime heartbeat and auto-hide logic, tool declarations. |
| `helpers.go` | Startup session selector, dynamic system prompt assembly (editable persona + dynamic rules), UI helpers, command suggestions, generation lifecycle helpers. |
| `store/` | Persistence foundation: single source of on-disk paths, atomic writes (temp + `fsync` + `rename`), versioned JSON envelopes with migrations and `.bak` recovery, advisory file locks (`flock`/`LockFileEx`). |
| `sessionstore.go` | Per-workspace multi-session chat store: `index.json` + `sessions/<id>.json` + `blobs/<sha256>`, per-turn autosave, image blobs, legacy `chat.json` migration, session pruning and orphan-blob GC. |
| `sessionui.go` | Session manager behind `/sessions`, plus the startup selector: switch, new, rename, bulk delete (multi-select) and relative-time labels. |
| `statusbar.go` | Status-bar hook wiring: the per-turn and periodic refresh, and pushing the line to the status bar through the optional `types.StatusLineSink`. |
| `updater/`, `update_cmd.go` | `fuckyourcli status` / `fuckyourcli update`: public release lookup and asset download through the GitHub API without authentication, SHA-256 verification against `checksums.txt`, and in-place binary replacement. |
| `streaming.go` | Provider dispatch and streaming/tool-call loops. |
| `tokens.go` | Local token estimation (text, messages, images, tool schemas) used for the compaction threshold. |
| `compaction.go` | `/compact` implementation (`compactContext`) and the auto-compaction trigger with its anti-loop guard (`maybeAutoCompact`). |
| `config/` | Persistent config, tool state, provider/model switching, provider setup, MCP setup. |
| `config/git.go` | Git tool registry (names, tiers, default-disabled set) and the git permission model. |
| `config/secrets.go` | Secret placeholders (`${VAR}` / `${VAR:-default}`) and the `auth.json` store: resolution at load, migration of inline secrets on save, generated (collision-free) variable names, the defensive `.gitignore`/`.fycignore` next to the config, and a *frozen* collapse (writes nothing to `auth.json`) when the store is unreadable or unwritable. |
| `config/system_prompt.go` | Editable base system prompt: `SYSTEM_PROMPT.txt` storage and `${VAR}` rendering (environment variables only, no shell). |
| `config/agentsmd.go` | AGENTS.md settings: enable flag, byte budget, and fallback filenames (with defaults). |
| `config/lsp.go` | LSP discovery overrides: search paths, per-server binaries, and the disabled list (accessed via `config.Get().LSP`). |
| `config/browser.go` | `web_search` browser overrides: the `browser_paths` list (setter/getter) probed by the crawler before the built-in locations; edited under **CLI Settings → Browser Paths**. |
| `lsp/` | Language-server discovery (PATH, Mason, VS Code, Zed — Linux/macOS/Windows) and the LSP client/manager used by the `lsp_*` tools. |
| `tools/agentsmd.go` | Resolves the AGENTS.md instruction chain (root→cwd, per-level precedence, byte budget) and renders the `<project_context>` block injected into the system prompt. |
| `providers/` | Gemini, OpenAI-compatible Chat Completions, OpenAI Responses-compatible, Anthropic, and Codex provider clients. |
| `reasoning/` | Provider-independent thinking intent and its translation into per-transport wire fields (built-in tables + host/name heuristics), plus the wizard helpers that decide which modes a resolved profile can express and preview the resulting request fields. |
| `models/` | models.dev catalog, stored as a standalone JSON file (`<config dir>/models.json`, never embedded); installed copy refreshed with `fuckyourcli models update`, bundled copy with `./update_catalog.sh`; informational, shown by `/reasoning`. |
| `tools/` | Tool execution, pre/post tool hooks, path validation, ignore handling, sandboxing, anchored edits, grep, skills, sub-agents, LSP, file operations. |
| `tools/status_hook.go` | Status-bar hook runner: runs the configured command, keeps the last useful value on failure, and opts secrets in through `FYC_API_KEY`. |
| `tools/git_*.go` | Git tools: executor (argv/env/timeout/redaction), repository detection, pathspec/ref validation, read/write/network/destructive tools, permission prompts, sub-agent policy (`subagents_policy.go`). |
| `tools/sandbox_users_windows.go`, `tools/sandbox_runner_windows.go` | Windows dedicated-user sandbox: the elevated setup (`fuckyourcli sandbox setup` — the `FycSandbox` account, DPAPI credentials, and the read+execute runner copy under `%ProgramData%`) and the runtime half (launcher + `__sandbox-runner` + named-pipe IPC). |
| `mcp/` | MCP manager, transports, tools, resources, reconnect handling. |
| `crawl/` | Browser-backed search and page extraction: sliding-window pagination with outline and in-document `find` (`paging.go`), markdown cleanup (`markdown.go`), browser discovery with the `FYC_BROWSER_PATH` → `browser_paths` → built-in priority plus `DetectBrowser`/`BrowserAvailable` (`crawl.go`), and the browser profile plus tracker blocking (`profile.go`). |
| `modes/` | Plan, Debug, Deep Research, and Tutorial mode definitions. |
| `ui/` | Bubble Tea input, hotkeys, selectors, styles, diff viewer, scrollable text viewer (`textviewer.go`), terminal rendering, Markdown streaming. |
| `agui/` | AG-UI backend: typed events + SSE (`events.go`, `sink.go`), input↔message mapping (`messages.go`), bearer-token auth/CORS (`auth.go`), HTTP/SSE server (`server.go`). |
| `agui/hitl.go` | Level-1 human-in-the-loop broker: parks a tool goroutine, emits `fyc.interaction_required`, waits for the client answer with a timeout/cancellation fallback, and remembers approvals per thread. |
| `agui/interrupts.go` | Level-2 human-in-the-loop: standard AG-UI interrupts. Records the interrupt, suspends the run (`InterruptError` as the run context's cancellation cause), applies `RunAgentInput.resume` answers (idempotency, expiry, thread ownership) and matches a replayed tool call to its answer. |
| `agui/threadstore.go`, `agui/interrupts_store.go` | Opt-in AG-UI thread persistence (`serve --state-dir`): `ThreadStore` interface, `MemoryStore` (default) and `FileStore` (one versioned file per thread); `Broker.SnapshotThread`/`RestoreThread` carry the open interrupts and "remember" grants across a restart. |
| `agui_serve.go` | `serve` command (`runServe`) and the `aguiRunner` that maps each HTTP run onto a per-thread `ChatSession` + `streamChat`. |
| `agui_interactor.go` | The `tools.Interactor` implementation that adapts tool prompts to the broker payloads and falls back to the deterministic headless policy. |
| `tools/interact.go` | The `Interactor` abstraction that routes tool prompts (approvals, typed confirmations, questionary) to the TUI or to the headless server policy. |
| `logger/` | Conversation-scoped provider diagnostics, opt-in normalized response dumps, redaction and whole-session retention. |
| `types/` | Shared tool, message, provider, stream, and MCP types. |
| `tests/`, `tools/*_test.go` | Test coverage. |

## Security Model

`fuckyourcli` is designed to reduce accidental damage while still allowing useful automation:

- Built-in file tools validate paths against the startup working directory.
- Traversal outside the restricted root is rejected.
- Ignore files (`.gitignore`, `.dockerignore`, `.fycignore`, `.ignore`) are respected for built-in reads/searches/edits; `.git` is always blocked.
- Tool availability is explicit and persisted in `tool_states`; disabled tools are not sent to the model.
- LSP tools are additionally hidden at runtime unless compatible language servers are discovered for the workspace.
- Git tools run `git` directly with an argv (never through a shell), a sanitized non-interactive environment (`GIT_TERMINAL_PROMPT=0`, no editor, no pager), a hard timeout, credential redaction on all output, and validation of every path/revision supplied by the model (workspace-bounded, `.git` internals unreachable).
- **Existing-file edits should use anchored reads and anchored diffs when those tools are enabled.** Unlike string replacement, which is prone to ambiguity (multiple occurrences) and fragility (slight context shifts, whitespace discrepancies, or padding issues), hash anchors provide a unique identifier for each line. They eliminate the risk of failure due to micro-inaccuracies in text reproduction or indentation changes, and they stay valid even when unrelated lines shift. Consequently, edits don't rely on line numbers or exact-text matching, and the model cites a short hash instead of reproducing the surrounding text (fewer tokens); this also means the mechanism naturally rejects edits against a file modified externally since the last read, reducing data corruption risk. Inspired by [Hash Anchored Edits](https://dirac.run/posts/hash-anchors-myers-diff-single-token).
- File changes are previewed and require approval unless auto mode is enabled. Approval prompts offer **See changes** for edits and commits (diff viewer) and **See details** for `sh` and the git tier prompts: a scrollable pane with the full command (never cut off by the terminal) plus the sandbox envelope or the exact `git` invocation being approved.
- Shell commands run from the restricted root with filtered environment variables.
- The sandboxed `sh` injects `GOFLAGS=-buildvcs=false` automatically — the sandbox masks `.git`, so Go's VCS stamping would otherwise fail — but only when the system Go is 1.18 or newer; it is skipped for older Go (which has no such flag).
- Values loaded from the project `.env` are redacted from tool output.
- `web_search` accepts cookie-consent banners automatically and blocks requests to known ad/analytics hosts; the browser runs outside the `sh` sandbox like the rest of the crawler. The tool is not exposed to the model at all when no supported browser is found.
- Linux command sandboxing uses `bwrap`; macOS uses `sandbox-exec`. Network egress control is currently unavailable on all platforms, including Linux and macOS, but is planned for future releases. On Windows the sandbox runs every command as a **dedicated local account** (`FycSandbox` in `FycSandboxUsers`) created by a one-time elevated `fuckyourcli sandbox setup`: a launcher starts a runner as that account (`CreateProcessWithLogonW`), which mints the `WRITE_RESTRICTED` restricted token (`CreateRestrictedToken`) and runs `cmd.exe` with a workspace-scoped allow-list. Reads of the real user's profile are denied by **identity** (no ACE is planted), and the runner is a read+execute copy of the binary under `%ProgramData%\fyc\runner` whose hash the setup records. The AppContainer engine was removed. See [**Windows sandbox**](#windows-sandbox) below for the full rationale and measurements.
- **Tool hooks run unsandboxed** on the host: only configure commands you trust.
- **The status-bar hook runs unsandboxed** too and is likewise inaccessible to the agent; the active API key/token reaches its command **only** when *Include API keys/tokens* is enabled for that hook; by default the secret is never exported.
- MCP tool calls require confirmation unless auto mode is enabled.
- **`fuckyourcli serve` requires a bearer token** (constant-time comparison), binds to `localhost` by default, and never grants anything on its own: with the default `--permissions deny` and no answer to a human-in-the-loop request (timeout, disconnect, or an interrupt that is never resumed) the tool is denied. The server never writes permission grants to `config.json` — "remember" decisions live in the broker, per thread. The restricted root is still the server's working directory.

Auto mode (`fuckyourcli auto`, `/auto on`, or `Ctrl+O`) disables confirmation prompts for the session. Use it only in trusted directories with trusted providers.

### Windows sandbox

The dedicated-user engine is implemented and active by default once the one-time setup has run (`fuckyourcli sandbox setup`, which raises UAC); without it `sh` keeps the older restricted-token engine. Measured state:

| | Status |
| --- | --- |
| `go build ./...` / `go test ./...` inside the sandbox | ✅ works |
| `dir`, `vol`, `>nul`, `type`, `ping`, console/colors | ✅ work |
| PowerShell (`powershell -NoProfile -Command "…"`) | ✅ works (runs as the dedicated account) |
| Writes outside the workspace | ✅ denied (`> C:\…` → Access denied; workspace/cache → ok) |
| Reads of the real user's profile (`.git`, `%APPDATA%\fyc`, DPAPI, other profiles) | ✅ denied by **identity** (default-deny) |
| System TLS (Schannel: `curl.exe`, `Invoke-WebRequest`, `git` HTTPS) | ❌ unavailable under the restricted token → use a client with its own TLS (Go, OpenSSL `curl`, Python) |
| Outbound network | ❌ not fail-closed yet — WFP + a local proxy is a future step |

What the sandbox isolates: **writes** (workspace-scoped allow-list) and **reads** (a foreign identity, so the real user's profile is default-deny), plus DPAPI/window/device scope the old restricted-token engine did not give. What it does **not** do yet: fail-closed network egress — planned as WFP + a local proxy on the same SID. The system TLS stack (Schannel) cannot work under the restricted token (a platform limitation), mitigated by using clients that bring their own TLS. 

Command syntax: `sh` runs commands through `cmd.exe` (no `;`, `printf`, `ls` or `uname` — chain with `&&`). PowerShell starts inside the sandbox but must be invoked explicitly (`powershell -NoProfile -Command "..."`).

## Development

Build a local check binary:

```bash
go build -buildvcs=false -o /tmp/fuckyourcli-check .
```

Run tests:

```bash
go test ./...
```
