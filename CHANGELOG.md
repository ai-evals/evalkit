# Changelog

Changes to evalkit are recorded by vertical slice. Each completed slice receives an installable GitHub release and an updated Homebrew tap formula.

## [0.3.0] - 2026-10-30 — V3: Bounded scheduling, retries, deadlines

### Added
- Configuration version 3 with explicit bounded case concurrency (1–16) and scorer attempt budget (1–10).
- Deterministic result ordering independent of worker completion order; Run deadline applies to all work.
- Core-owned retry controller for typed transient scorer errors; no implicit target replays.
- Linux/macOS process-group termination on cancellation, with bounded cleanup uncertainty (not hard sandboxing).
- Acceptance fixtures for concurrent ordering, retry success/exhaustion, permanent failure, deadline and invalid concurrency.

### Refactored
- Extracted shared case evaluation and quality aggregation across V2 and V3 (no duplicate scoring logic).

## [0.2.0] - 2026-10-30 — V2: Dataset and assessment completeness

### Added
- Configuration schema version 2 with multiple cases and named required/optional scorers.
- Deterministic case ordering and explicit invalid-case accounting for duplicate IDs and missing case JSON.
- Aggregate quality based on complete required scoring coverage, not the optional scorer count.
- Version 2 artifact report and reader verification; v1 configuration and artifacts remain supported.
- Acceptance fixtures for full PASS, threshold FAIL, INDETERMINATE coverage, and invalid datasets.

### Refactored
- Reused V1 bounded command-target execution and result validation across case definitions.

## [0.1.0] - 2026-10-30 — V1: One complete evaluation

### Added
- `evalkit run` for one JSON case and one exact-match scorer.
- Versioned JSON input, immutable effective configuration digest, bounded command-target protocol, and explicit quality verdicts.
- Durable local evidence containing `run.json`, `report.json`, and a versioned integrity commit marker.
- CLI exit codes: PASS=0, FAIL=1, framework failure/CANCELLED=2, INDETERMINATE=3.
- Deterministic acceptance tests, GitHub Actions CI on Linux and macOS, and cross-platform Homebrew release packaging.

### Known limitations
- V1 has one case and one built-in scorer; parallel scheduling and retries are introduced in later slices.
- This local executor does **not** provide hardened network/filesystem sandboxing or guaranteed process-tree containment.
- Artifact evidence digests detect corruption but do not authenticate the artifact author.
