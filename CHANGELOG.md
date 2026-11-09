# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

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

