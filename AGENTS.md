# AGENTS.md

fuckyourcli (fyc) is a Go CLI coding agent. Single Go module `fuckyourcli`, Go 1.25.

## Build & test
- The sandbox masks `.git`: always pass `-buildvcs=false` to Go commands.
  The `sh` tool injects it automatically; use `GOFLAGS=-buildvcs=false go doc ...` for documentation (go doc does not accept the flag directly), and add it yourself in editor and CI runs.
- Build: `go build -buildvcs=false ./...`
- Test: `go test -buildvcs=false ./...`
- Vet: `go vet -buildvcs=false ./...`
- Fallback extractor (no cgo): `CGO_ENABLED=0 go test -buildvcs=false ./tools/`

## Layout
- `main.go`, `helpers.go`, `globals.go`, `slash.go`, `session*.go` — CLI/TUI core (package main)
- `config/` — persisted configuration (`config.json`, schema-versioned; migrations in `persistence.go`)
- `config/secrets.go` — secret placeholders (`${VAR}` / `${VAR:-default}`) and the `auth.json` store: resolved at load, secrets migrated out of `config.json` on save; also drops the defensive `.gitignore`/`.fycignore` into the config dir
- `tools/` — tool implementations and filesystem scanners (ignore, skills, agentsmd)
- `reasoning/` — provider-independent thinking intent plus per-transport wire translation (no I/O; see `reasoning/intent.go` for the model)
- `models/` — models.dev catalog, a standalone JSON file at `store.ConfigDir()/models.json` (never embedded; refresh with `./update_catalog.sh` or `fuckyourcli models update`); informational, consumed by `/reasoning`
- `codexversion/` — upstream Codex compatibility version, not fyc's release version; `VERSION` is embedded for offline/plain builds and refreshed before release tags. Build-time HTTP resolution is separate from Go compilation; never require Node or an installed Codex.
- `providers/`, `mcp/`, `ui/`, `lsp/`, `modes/`, `store/`, `types/`, `agui/`

## Conventions
- AGENTS.md: add only guidance that is genuinely useful across tasks (non-obvious invariants, build quirks, layout); never task-type-scoped instructions or things discoverable from the code.
- Comments in English, kept minimal (explain WHY, not WHAT).
- Changing the `config.json` shape: bump `config.CurrentSchemaVersion` and add a migration in `config/persistence.go`.
- `store.LoadVersioned` decodes into a fresh value; pre-seeding its destination cannot supply defaults for missing JSON fields. Use document-level `UnmarshalJSON` for defaults that must also apply to current-schema files.
- Secrets/tokens: never store a value in `config.json`; keep a `${VAR}` placeholder and the value in `auth.json` (resolution order: `auth.json` then env). Add new secret fields to `config/secrets.go` (capture/expand/collapse), not to the read sites. When `auth.json` is unreadable, the collapse runs in `frozen` mode: keep the on-disk expressions, write nothing to the store (never drop an unresolved `${VAR}` or emit a placeholder whose value was not stored).
- Thinking/reasoning: user intent lives in `config.Reasoning` (see `config/reasoning.go`), providers translate it via `reasoning.Lookup`/`Translate`; never hard-code a provider's reasoning fields in a provider file.
- Anchored filesystem operations acquire `opMu` before the state `mu`; never hold `opMu` during a permission prompt (HITL can release the global tool mutex). Validate exact disk bytes before preview and again before writing; a conflict must require an explicit re-read, never silently refresh the snapshot.
- `CHANGELOG.md` is updated automatically by the CI workflow. Do not edit it manually unless the user explicitly requests a changelog change.
- Release notes have a single source of truth: the reviewed, dated version section in `CHANGELOG.md`. Release CI extracts it with `cmd/release-notes`; `changelog.disable` in GoReleaser also discards explicit `--release-notes` files, so it must stay unset. Commits can contain multiple typed entries in their body: preserve them when changing commit/release tooling, and scope breaking-change footers to their entry.
- Conversation logging is context-scoped: preserve the turn/run context through provider and tool calls so resumed chats, subagents and compaction stay correlated. Debug dumps are opt-in and must never include opaque reasoning replay state or provider error text.
- Provider streams use a request-scoped inactivity budget, renewed by response body bytes (not HTTP/2 pings). Keep the parent turn context separate so an idle timeout is reported as an error, not mistaken for a user interrupt; never replay a partially consumed stream. Clean EOF alone does not prove completion: require a protocol terminal signal except for valid, non-empty text-only Chat Completions streams (compatibility fallback; never tool calls). Token-limit terminal events are not transport errors, even when only reasoning was emitted. Distinguish explicit safety blocks from transport truncation.
