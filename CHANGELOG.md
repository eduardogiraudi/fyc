# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

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

