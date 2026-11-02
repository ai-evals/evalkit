# AGENTS.md

This file routes coding agents and human contributors who change evalkit. It follows [Context Onion RFC-001, Progressive Context Acquisition](https://contextonion.dev/rfcs/RFC-001-progressive-context-acquisition/), Level 2 (Progressive acquisition). [README.md](README.md) is the authoritative product and contract reference. This file points into it and states the local constraints.

## Repository

- evalkit is one Go module. [go.mod](go.mod) sets the language version and the toolchain. evalkit builds for Linux and macOS only, because [internal/engine/process_unix.go](internal/engine/process_unix.go) is the only process-group implementation.
- CI is [.github/workflows/ci.yml](.github/workflows/ci.yml), on Linux and macOS. The commands it runs are under Verification.

## System

- [README Reference](README.md#reference) is the source for commands, config and artifact fields, exit codes, and limits. [README Verdicts](README.md#verdicts) states the verdict rules, and [README guarantees](README.md#what-evalkit-guarantees-and-what-it-does-not) states what evalkit does not promise.
- The CLI and its one exit table, `exitCodes`, are in [cmd/evalkit/main.go](cmd/evalkit/main.go).
- A run goes through `runJSON`, `execute`, `publish`, and `VerifyArtifact` in [internal/engine/engine.go](internal/engine/engine.go). `evaluateCase`, `runCases`, and `qualityOfCases` in [internal/engine/v2.go](internal/engine/v2.go) own cases, scorers, and the verdict. `v3.go`, `v4.go`, `retry.go`, and `extensions.go` add execution policy and custom scorers.
- [internal/engine/retained.go](internal/engine/retained.go) owns retained output and diagnostic subjects.
- `inspect` and `compare` read through `loadVerified`, the one artifact reader, in [internal/engine/readmodel.go](internal/engine/readmodel.go), then `inspection.go`, `compare.go`, and `verdict.go`.
- The Go SDK in [sdk/](sdk/) wraps the engine. Its JSON vocabulary must equal the engine's.
- [internal/release/version.go](internal/release/version.go) holds the whole versioning policy, and [internal/cmd/release](internal/cmd/release/main.go) is its CLI. [CHANGELOG.md](CHANGELOG.md) is the release history.

## Conventions

- Merge pull requests by squash only. The PR title lands as the commit, so it must be a Conventional Commit header. CI's `plan` job fails any other title.
- The PR title decides the release, through `Change.Bump` in `internal/release/version.go`. `feat` is a minor release. `fix`, `perf`, and `revert` are a patch. `!` or a `BREAKING CHANGE` footer is a minor release while evalkit is 0.x. On 1.x a breaking change needs a `/vN` module path first, and the tool refuses without one.
- Title tooling, docs, and test changes `ci`, `chore`, `docs`, `test`, `build`, or `refactor`, so they release nothing.
- Never add a VERSION file. The version comes from tags and commits only.
- Never edit `Formula/evalkit.rb` or an existing CHANGELOG.md section by hand. The `release` job commits both as `chore(release): vX.Y.Z`. If it fails midway, re-run it or dispatch `CI` on `main`, and it finishes without duplicating work.
- Write tooling in Go. Keep shell to thin wrappers such as `run.sh`. Do not add Python.

## Boundaries

Do not change these casually. Each names where it is enforced.

- Verdict. `qualityOfCases` decides every v2 to v4 run and re-checks every artifact in `inspect` and `compare`. The v1 run path has its own inline rule in `runJSON`. To change verdict meaning, update the formal oracle and README Verdicts in the same PR. Enforced by formal-verify-verdict P1 to P4 and `TestV6VerdictVocabulary`.
- Scorer retry. `retryOperation` retries only a `RetryableScorerError`, and its limit counts executions. Enforced by the `TestV3AC02` to `TestV3AC04` tests in `internal/engine/v3_test.go` and formal P1.
- Targets are never retried. `evaluateCase` runs `execute` once per case, because a target may have side effects.
- Slot order. Workers in `runCases` write `results[i]` by dataset index, so artifacts keep config order at any concurrency. Enforced by formal P2 and `TestV3AC01ConcurrentCasesStableOrdering`.
- Cancellation reads no output. On cancellation `execute` returns before it reads stdout, because the copier may still be writing, and the run publishes nothing. Enforced by `TestV7AC09CancellationRetainsNothing` and `TestV3AC05DeadlineCancelsRun`.
- stderr is never read or retained, and diagnostic messages are static strings. Enforced by `TestV7AC03StderrNeverRetained`.
- The artifact schema is a one-way door. `decodeOne` rejects unknown fields, so any new field in `run.json`, `report.json`, or `commit.json` makes every older binary refuse new artifacts. Make new fields `omitempty`, and accept artifacts that lack them.
- Old artifacts replay byte for byte. `inspect` and `compare` must print exactly what v0.6.0 printed for the corpus in `cmd/evalkit/testdata/v0.6.0`. Enforced by `TestV7AC06V060ArtifactsReadByteIdentically`. Regenerate the corpus only with `generate.sh` and a v0.6.0 binary, which the script checks.
- Threat-model a change to what evalkit captures, stores, or displays, before merge. Model the diff, and include untrusted target output flowing into terminals and logs. Record the result in the PR, as #13 did. That review is why `replaceUnescapedControl` in `compare.go` replaces DEL and C1 controls in previews.

## Verification

Tests and checks enforce the contracts in README and in Boundaries. They are not where the rules are written.

- For every change, run `go test -race -count=1 ./...`, `go vet ./...`, and `gofmt -l cmd internal examples sdk`. The last must print nothing. CI runs the same three.
- `go vet -tags formalverify ./internal/engine/` vets the formal harness, which the build tag hides from plain `go vet` and CI.
- `.claude/skills/formal-verify-verdict/run.sh`, about 50 seconds, must exit 0 before you merge a change to code its [SKILL.md](.claude/skills/formal-verify-verdict/SKILL.md) lists under "Covered code". It also applies when you change the meaning of a scorer status, case state, threshold, attempt budget, retained output, or diagnostic subject.
- Move the sync commit in that SKILL.md only after the squash merge, to the squash SHA on `main`, and only after you read the diff of the covered files since the old SHA. Do it in a `chore(verify)` follow-up, as #14 did.
- For a change to `cmd/`, `internal/engine`, `examples/`, or the config or artifact format, prove it end to end with the [verify-evalkit skill](.claude/skills/verify-evalkit/SKILL.md). It drives a freshly built binary and records stdout, stderr, exit codes, and artifacts.
- For release tooling, `go run ./internal/cmd/release plan --pr-title "TITLE" --build DIR` runs what CI's `plan` job runs.

## Context acquisition

For behavioral or cross-component changes:

1. Understand the affected workflow before you change code. Start at the System route, which is a README section and then the engine file that owns it.
2. Locate the relevant source, its tests beside it, the example configs in `examples/`, and the callers in `cmd/evalkit` and `sdk/`.
3. Resolve the Boundaries the change touches.
4. Expand further only while material uncertainty remains. Stop once you can name the files, the constraints, and the verification.

For a demonstrably local change, such as docs only or release tooling that touches no artifact behavior, go from the task to the files to Verification.

Preserve the outcome the task asks for. Do not redefine the task around the first implementation path you find.
