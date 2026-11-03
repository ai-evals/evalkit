# AGENTS.md

This file routes coding agents and human contributors who change evalkit. It follows [Context Onion RFC-001, Progressive Context Acquisition](https://contextonion.dev/rfcs/RFC-001-progressive-context-acquisition/), Level 2 (Progressive acquisition). [README.md](README.md) is the authoritative product and contract reference. This file points into it and states the local constraints.

## Repository

- evalkit is one Go module. [go.mod](go.mod) sets the language version and the toolchain. evalkit builds for Linux and macOS only, because [internal/engine/process_unix.go](internal/engine/process_unix.go) is the only process-group implementation.
- CI is [.github/workflows/ci.yml](.github/workflows/ci.yml), on Linux and macOS. The commands it runs are under Verification.

## System

- [README Reference](README.md#reference) is the source for commands, config and artifact fields, exit codes, limits, and the Go SDK API. [README Verdicts](README.md#verdicts) states the verdict rules, and [README guarantees](README.md#what-evalkit-guarantees-and-what-it-does-not) states what evalkit does not promise.
- The CLI and its one exit table, `exitCodes`, are in [cmd/evalkit/main.go](cmd/evalkit/main.go).
- A run goes through `runJSON`, `execute`, `publish`, and `VerifyArtifact` in [internal/engine/engine.go](internal/engine/engine.go). `evaluateCase`, `runCases`, and `qualityOfCases` in [internal/engine/v2.go](internal/engine/v2.go) own cases, scorers, and the verdict. `v3.go`, `v4.go`, `retry.go`, and `extensions.go` add execution policy and custom scorers.
- [internal/engine/retained.go](internal/engine/retained.go) owns retained output and diagnostic subjects.
- `inspect` and `compare` read through `loadVerified`, the one artifact reader, in [internal/engine/readmodel.go](internal/engine/readmodel.go), then `inspection.go`, `compare.go`, and `verdict.go`.
- The Go SDK in [sdk/](sdk/) wraps the engine. Its JSON vocabulary must equal the engine's.
- [internal/release/version.go](internal/release/version.go) holds the versioning rules, and `Publish` in [internal/release/publish.go](internal/release/publish.go) is the release job. [internal/cmd/release](internal/cmd/release/main.go) is their CLI, and [CHANGELOG.md](CHANGELOG.md) is the release history.

## Conventions

- Merge pull requests by squash only. The PR title lands as the commit, so it must be a Conventional Commit header, and CI's `plan` job fails any other title.
- The PR title decides the release, and tooling, docs, and test types such as `chore`, `ci`, `docs`, and `test` release nothing. `Change.Bump` and `Version.Next` in [internal/release/version.go](internal/release/version.go) are the rules.
- Never add a VERSION file. The version comes from tags and commits only.
- Never edit `Formula/evalkit.rb` or an existing CHANGELOG.md section by hand. The `release` job commits both as `chore(release): vX.Y.Z`, and a re-run or a dispatch of `CI` on `main` finishes a release that stopped midway.
- Write tooling in Go. Keep shell to thin wrappers such as `run.sh`. Do not add Python.

## Boundaries

Do not change these casually. Each names the code or test that holds it.

- Verdict. `qualityOfCases` decides every v2 to v4 verdict and `runJSON` decides v1, so change verdict meaning only together with the formal oracle and README Verdicts. Enforced by formal-verify-verdict P1 to P4 and `TestV6VerdictVocabulary`.
- Scorer retry. `retryOperation` retries only a `RetryableScorerError`, and its limit counts executions. Enforced by `TestV3AC02` to `TestV3AC04` in `internal/engine/v3_test.go` and formal P1.
- Targets are never retried. `evaluateCase` calls `execute` once per case.
- Slot order. Workers in `runCases` write `results[i]` by dataset index, so artifacts keep config order at any concurrency. Enforced by formal P2 and `TestV3AC01ConcurrentCasesStableOrdering`.
- Cancellation reads no output. On cancellation `execute` returns before it reads stdout, and the run publishes nothing. Enforced by `TestV7AC09CancellationRetainsNothing` and `TestV3AC05DeadlineCancelsRun`.
- stderr is never read or retained, and diagnostic messages are static strings. Enforced by `TestV7AC03StderrNeverRetained`.
- Artifact schema. `decodeOne` in `engine.go` rejects unknown fields, so make each new field in `run.json`, `report.json`, or `commit.json` `omitempty`, and accept artifacts that lack it.
- Old artifacts replay byte for byte. `inspect` and `compare` print what v0.6.0 printed for the corpus in `cmd/evalkit/testdata/v0.6.0`, which only its `generate.sh` and a v0.6.0 binary may regenerate. Enforced by `TestV7AC06V060ArtifactsReadByteIdentically`.
- Releases are draft-first. `ensurePublished` creates no tag until `verifyAssets` has checked every archive and `SHA256SUMS`, so the tag and the assets appear together. Enforced by `TestPublishConvergesFromEveryCrashPoint` in `internal/release/publish_test.go`.
- No empty release commit. `syncMain` commits nothing when `Formula/evalkit.rb` and `CHANGELOG.md` already match. Enforced by `TestRerunAfterSyncIsNoop`.
- Threat-model a change to what evalkit captures, stores, or displays before merge, including untrusted target output that reaches terminals and logs, and record it in the PR, as #13 did for `replaceUnescapedControl` in `compare.go`.

## Verification

Each check below is evidence that a contract in README or in Boundaries holds. The contract itself lives in README and in the code that Boundaries cites.

- For every change, run `go test -race -count=1 ./...`, `go vet ./...`, and `gofmt -l cmd internal examples sdk`. The last must print nothing. CI runs the same three.
- `go vet -tags formalverify ./internal/engine/` vets the formal harness, which the build tag hides from plain `go vet` and CI.
- `.claude/skills/formal-verify-verdict/run.sh`, about two and a half minutes, must exit 0 before you merge a change to code in the Covered code list under Coverage record and sync in its [SKILL.md](.claude/skills/formal-verify-verdict/SKILL.md). It also applies when you change the meaning of a scorer status, case state, threshold, attempt budget, command scorer protocol, retained output, or diagnostic subject.
- Move the sync commit in that SKILL.md only after the squash merge, to the squash SHA on `main`, and only after you read the diff of the covered files since the old SHA. Do it in a `chore(verify)` follow-up, as #14 did.
- For a change to `cmd/`, `internal/engine`, `examples/`, or the config or artifact format, prove it end to end with the [verify-evalkit skill](.claude/skills/verify-evalkit/SKILL.md). It drives a freshly built binary and records stdout, stderr, exit codes, and artifacts.
- `go run ./internal/cmd/release plan` is read-only and prints the release that HEAD would make, from the commits since the last release tag. `--pr-title TITLE` adds only a check that the title is a Conventional Commit header, and `--build DIR` also cross-builds the archives into `DIR`. On pull requests, CI's `plan` job runs it with both flags and writes the plan to the job summary.

## Context acquisition

For behavioral or cross-component changes:

1. Understand the affected workflow before you change code. Start at the System route, which is a README section and then the engine file that owns it.
2. Locate the relevant source, its tests beside it, the example configs in `examples/`, and the callers in `cmd/evalkit` and `sdk/`.
3. Resolve the Boundaries the change touches.
4. Expand further only while material uncertainty remains. Stop once you can name the files, the constraints, and the verification.

For a demonstrably local change, such as docs only or release tooling that touches no artifact behavior, go from the task to the files to Verification.

Preserve the outcome the task asks for. Do not redefine the task around the first implementation path you find.
