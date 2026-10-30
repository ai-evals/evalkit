# Changelog

Changes to evalkit are recorded by vertical slice. Each completed slice receives an installable GitHub release and an updated Homebrew tap formula.

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
