# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

## [1.7.0] - 2026-11-23

### Added

- ui: show a context window gauge under the provider row (`b4cc298`).
- config: add a configurable models_url for the provider model list (`b4cc298`).
- undo: add /undo to rewind a whole turn (`9021384`).
- slash: add /init to generate or refresh AGENTS.md (`23a3f06`).
- cd: allow extra workspace roots, session-only or saved (`894aac5`).

### Fixed

- resolve symlinks when comparing workspace paths on macOS (`e0798d8`).

## [1.6.0] - 2026-11-20

### Added

- add /usage command (`0c3822b`).
- subagents: add named model profiles with fallback chain (`9bfb3d2`).
- subagents: configure the budget with arrow-key pickers and no ceiling (`af4030b`).
- config: enable the alt-screen chat UI by default (`405577b`).
- ui: hint ctrl+r history search in start-up tips (`54fc2a3`).
- chat: run display/behaviour commands while the model generates (`6bb5009`).
- chat: select and copy the input bar text in the alt-screen TUI (`6bb5009`).
- tui: expandable reasoning trace, full patch diff, prompt-free input copy (`1de562b`).

### Fixed

- config: prune auth.json secrets of removed providers (`9d23ba1`).
- ui: draw in-progress output in the alt-screen transcript instead of pinning it above the input (`0c3bf44`).
- tui: the input bar copy no longer carries the ❯ prompt and the alignment padding (`1de562b`).
- tools: keep sub-agent roots inside the workspace (`ee1fbe1`).
- mcp: keep MCP sessions alive past their setup context (`9d0c54d`).

## [1.5.0] - 2026-11-19

### Added

- session: replay history when resuming a chat (`a963d05`).
- compaction: trigger on the provider's measured context size, local estimate as fallback (`51b0189`).
- ui: add opt-in alt-screen chat TUI, off by default (`59ae8b8`).
- ui: make the mode badge and the scroll hint buttons in alt screen (`b514683`).
- ui: resend failed streams after visible output in alt screen (`15caf4c`).
- ui: show colored line counts for anchored edits (`25ffef5`).
- add history search bar (`571251e`).
- chat: add @file mentions with autocomplete and bounded text and image attachments (`1258eee`).
- ignore: let scoped .fycignore rules replace inherited .gitignore rules (`07757cf`).
- ui: add hover and mouse controls to hosted menus, confirmations, settings and viewers (`7338a24`).
- ui: add fixed search bar to model selectors in REPL and alt-screen TUI (`43c8ef3`).
- plugins: add `add` command for Agent Plugins and MCP OAuth login (`1dc4b46`).
- ui: add mouse navigation for input popups and retry lost HTTP/2 streams (`d03171a`).
- ui: add hover highlight to slash/@ suggestion and history search popups (`14a54f8`).
- ui: expand tool output in place (`e258218`).

### Fixed

- sh: report nonzero exit codes neutrally (`98ff1c9`).
- ui: prevent premature markdown breaks and remove padding (`b28168f`).
- codex: avoid false quota detection in response content (`6a569b5`).
- history: preserve multiline prompts as single entries (`f9ed6f6`).
- ignore: stop applying .dockerignore rules to workspace tools (`07757cf`).
- provider: offer to reuse the configured model and preserve configuration on cancellation (`43c8ef3`).

## [1.4.0] - 2026-11-17

### Added

- compaction: render a role-tagged transcript and merge earlier summaries (`3dd014d`).
- providers: resend sampling requests that fail before any visible output (`34ad3ce`).
- providers: make retries resilient to overload and transient stream failures (`ce654a1`).
- tools: harden anchored edits and conflict recovery (`189239d`).

### Fixed

- tokens: count CJK, kana, hangul and emoji as one token per character (`3dd014d`).
- providers: retry in-band rate limits with the HTTP 429 budget (`aca0694`).
- codex: retry lost HTTP/2 streams (`83a408e`).
- compaction: resend compaction requests on in-band 429 and mid-stream drops (`944c51b`).

## [1.3.0] - 2026-11-09

### Added

- add /retry to repeat a failed provider request (`2883bfe`).
- config: make max_media configurable via JSON and UI, defaulting to 10 (`2008591`).
- crawl: add HTTP-first search, structured results and PDF extraction (`a6116e9`).
- sandbox: expose GPU device nodes in the Linux sandbox, default on with sandbox.gpu opt-out (`d967909`).

### Fixed

- honor turn cancellation during configured delay (`3ca2d84`).
- ui: make the diff viewer responsive on large diffs by caching wrapped rows and highlighting instead of recomputing them on every keystroke (`a01506c`).
- context: count individual images when pruning media (`2008591`).
- crawl: harden network access and isolate shared-fetch cancellation (`a6116e9`).

## [1.2.0] - 2026-10-20

### Added

- compaction: add dedicated compaction model and configurable failure policy (`d9e2538`).

### Fixed

- logger: queue local root-lock waiters before the inter-process timeout (`097564a`).

## [1.1.1] - 2026-10-20

### Fixed

- grep: searching a single file now looks only at that exact file instead of widening to its folder (`868de47`).

## [1.1.0] - 2026-10-19

### Added

- tools: cap read/grep/get_anchors output with paging and truncation notes (`f04b5bd`).

## [1.0.2] - 2026-10-19

### Fixed

- Windows installer no longer aborts before installing: PowerShell 5.1 misparsed the ExecutionPolicy notice in `setup.ps1` (`75207ac`).

## [1.0.1] - 2026-10-19

### Added

- anthropic: enable prompt caching with automatic fallback (`ba363a8`).

### Changed

- compaction: prune superseded snapshot results in summarizer transcript (`faa8631`).

## [1.0.0] - 2026-10-15

### Added

- initial stable release (`952fd2b`).

