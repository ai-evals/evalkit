# evalkit — evalkit walking skeleton

Outside-in implementation of the evalkit specification, one vertical slice per version.

**Scope through V7:** versioned JSON configurations with one or more cases and named scorers, one direct command target, bounded concurrency and scorer retries, a Go SDK for trusted custom scorers, a deterministic quality verdict, locally committed JSON run evidence that keeps each case's target stdout, and read-only inspection and comparison of published runs. Each version's section below states its limits. This is a development skeleton, not a release-ready or security-isolated runner.

## Run tests

```sh
go test ./...
go vet ./...
```

## Execute the sample

```sh
mkdir -p .local
go build -o "$(pwd)/.local/evalkit-example-target" ./examples/target
go build -o "$(pwd)/.local/evalkit" ./cmd/evalkit
# Set target.command in examples/evalkit.json to the absolute path printed below:
pwd
.local/evalkit run --config examples/evalkit.json --artifact-dir .local/first-run
```

The sample file includes a placeholder executable path, which **must** be replaced before running. Use a new artifact directory for each invocation.

The CLI prints one JSON Run outcome to stdout and returns 0 (PASS), 1 (FAIL), 3 (INDETERMINATE), or 2 (framework error/cancellation).

## V1 behavior

- Configuration version 1 is strict JSON with exactly one case and one exact-match scorer. Later versions have their own sections below.
- Command executed **without a shell**, with a controlled temporary working directory, a reduced environment (PATH only), JSON request on stdin, and one JSON result on stdout.
- Command output bounded to 1 MiB; no raw target stderr leaked into public diagnostics.
- Exact-match compares JSON values structurally. Correct result scores 1, mismatch scores 0. A malformed target result makes coverage incomplete and the completed quality INDETERMINATE.
- Required run/report evidence is staged, hashed, synced and published via a directory rename with `commit.json` written last in staging. Basic reader verifies integrity and report consistency.
- Output is never intentionally overwritten by a normal repeated run.

## Deliberately out of scope

A durable retry scheduler, enforceable process-tree isolation, network/filesystem sandboxing, external providers, arbitrary extension loading, and sophisticated transaction recovery remain later slices. This local executor **must not be used** where those security controls are required.

Known hardening work before production readiness: atomic exclusive artifact-path reservation under adversarial races, crash/fault injection around rename and directory fsync, cleanup uncertainty accounting, and stronger termination of process descendants. The v1 tests verify the nominal and deterministic failure paths, not these stronger environmental guarantees.

## Releases and Homebrew (macOS and Linux)

Releases are automatic. Every push to `main` that passes CI on Linux and macOS is
checked for [Conventional Commits](https://www.conventionalcommits.org/) since the last
`vX.Y.Z` tag, and the version is derived from them (there is no `VERSION` file):

| Commit type | Release |
| --- | --- |
| `fix:`, `perf:`, `revert:` | patch (`0.5.0` → `0.5.1`) |
| `feat:` | minor (`0.5.1` → `0.6.0`) |
| `!` after the type, or a `BREAKING CHANGE:` footer | minor while evalkit is `0.x`; from `1.x` it requires a `/vN` module path first |
| `chore:`, `docs:`, `ci:`, `test:`, `refactor:`, `build:`, `style:` | none |

Pull requests are squash-merged, so the **PR title** is the commit that lands and must be a
Conventional Commit header (`type(scope): description`); CI checks it and writes what merging
would publish to the job summary. Use `ci:`/`build:`/`chore:` for changes to the release
tooling itself, so they do not cut an evalkit release.

A release appears with its tag, cross-compiled Linux/macOS amd64/arm64 archives and
`SHA256SUMS` all at once (the tag is created when the complete draft release is published).
Then `Formula/evalkit.rb` and `CHANGELOG.md` are updated on `main` in one
`chore(release): vX.Y.Z` commit. Nothing is committed when they are already current.
If a release job fails midway, re-run it (or run the `CI` workflow on `main`); it finishes
the work without duplicating anything.

To see what the current checkout would publish (read-only, needs only git):

```sh
go run ./internal/cmd/release plan
go run ./internal/cmd/release plan --build /tmp/evalkit-dist   # also cross-build the archives
```

The repository also acts as its own Homebrew tap (a separate tap repository is not required):

```sh
brew tap ai-evals/evalkit https://github.com/ai-evals/evalkit.git
brew install ai-evals/evalkit/evalkit
# or: brew upgrade ai-evals/evalkit/evalkit
evalkit version
```

## V2 multi-case configuration

See `examples/evalkit-v2.json` for a complete multi-case sample.

Configuration version 2 accepts `cases` and named `scorers` arrays; `required: true`
scorers determine required coverage. Each case has `id`, `input`, and `expected` JSON.
Invalid cases are accounted for explicitly and yield INDETERMINATE when required
coverage cannot be completed. Version 1 remains compatible.

The V2 implementation is still serial; concurrency and attempt retry budgets are V3.

## V3 bounded execution

See `examples/evalkit-v3.json` for a complete V3 example.

Configuration version 3 adds `execution: {"concurrency": 3, "scorer_max_attempts": 2}`.
Case execution uses a bounded worker pool while preserving original dataset order in
artifacts. Scorer retry eligibility is strict and cannot retry target commands.
Run deadlines stop new case admission and request process-group termination.
A local process group is **not** a hostile-code sandbox and cannot prevent
processes from escaping the group or enforcing network/filesystem isolation.

## V4 public Go SDK

The Go package `github.com/ai-evals/evalkit/sdk` provides a small `Runner` API.
Register trusted in-process `Scorer` implementations with an exact ID and
implementation identity, then invoke `Runner.Run(ctx, sdk.Request{...})` with
version 4 JSON configuration. `sdk.Scored(float64)` and
`sdk.Unevaluable(reason)` are distinct results; operational errors are returned
through Go `error`. Invalid scores are rejected by the authoritative core.

Scorers are serialized by instance by default. Custom Go code is trusted
in-process code and **not** sandboxed or forcibly interruptible.
See `sdk/runner_test.go` for executable examples.

## V5 read-only historical inspection

```sh
evalkit inspect --artifact-dir /path/to/previous/run
```

The CLI returns typed JSON describing the recorded run, artifact self-consistency,
and whether retained measurement-accounting evidence could be checked. In the SDK,
use `sdk.NewInspector().Inspect(ctx, artifactDir)` without Run execution authority.

SHA256 checksums verify local **self-consistency**, not authenticated authorship.
Older v0.1-v0.4 artifacts may lack a self-contained measurement contract; their
measurement-accounting status is therefore `NOT_RETAINED`, not fabricated as verified.
The basic local reader rejects symlinked evidence and bounded file overflows,
but it is not a hardened hostile-concurrent-filesystem sandbox.

## V6 read-only comparison

```sh
evalkit compare --baseline /path/to/main-run --candidate /path/to/pr-run
```

`compare` reads two published artifacts and compares them cell by cell, one cell per
case and scorer. It prints one JSON document and exits like `run`: 0 SHIP (the
candidate is PASS), 1 ITERATE (FAIL), 3 INVESTIGATE (INDETERMINATE), and 2 when it
refuses. It executes nothing and writes nothing.

Each cell is PASSING (scorer SUCCEEDED and score at or above the threshold), FAILING
(SUCCEEDED and below it) or UNKNOWN (any other scorer status, or a case that is not
SCORED). From baseline to candidate a cell is IMPROVED (FAILING to PASSING), REGRESSED
(PASSING to FAILING), LOST_EVIDENCE (to UNKNOWN), GAINED_EVIDENCE (from UNKNOWN) or
UNCHANGED. `counts` covers every cell and always has all five keys. `changes` lists
only the cells whose state or score changed, in dataset order, so a score-only change
such as 0.6 to 0.9 is listed as UNCHANGED. `case_state` explains an UNKNOWN cell whose
own scorer SUCCEEDED: a required scorer failed on the same case.

The recommendation follows the candidate's quality alone. A required regression makes
the candidate FAIL or INDETERMINATE, so it never ships. Optional regressions are
listed but never block.

Real output from `examples/evalkit-v2.json`, where the candidate's target answers
`no` on `case-2` (formatted with `jq`, exit 1):

```json
{
  "schema_version": 1,
  "recommendation": "ITERATE",
  "baseline": {"run_id": "25b16eee8b31a989d34f4d5344b1c8d1", "quality": "PASS"},
  "candidate": {"run_id": "9c5b59cbcc967c4c7de701f57d4fe6cb", "quality": "FAIL"},
  "counts": {"UNCHANGED": 2, "IMPROVED": 0, "REGRESSED": 2, "LOST_EVIDENCE": 0, "GAINED_EVIDENCE": 0},
  "changes": [
    {"case_id": "case-2", "scorer_id": "exact-required", "required": true, "transition": "REGRESSED",
     "from": {"cell": "PASSING", "case_state": "SCORED", "status": "SUCCEEDED", "score": 1},
     "to": {"cell": "FAILING", "case_state": "SCORED", "status": "SUCCEEDED", "score": 0}},
    {"case_id": "case-2", "scorer_id": "exact-observational", "required": false, "transition": "REGRESSED",
     "from": {"cell": "PASSING", "case_state": "SCORED", "status": "SUCCEEDED", "score": 1},
     "to": {"cell": "FAILING", "case_state": "SCORED", "status": "SUCCEEDED", "score": 0}}
  ]
}
```

A refusal uses the `inspect` envelope with `scope: "COMPARISON"`. `input` names the
directory to look at, and `reason` says why two verified runs are not comparable:

```json
{"error":{"code":"COMPARISON_NOT_COMPARABLE","scope":"COMPARISON","reason":"SCORERS_DIFFER"},"schema_version":1}
```

| Code | `input` | `reason` | Cause |
| --- | --- | --- | --- |
| `ARTIFACT_VERIFICATION_FAILED` | `baseline` or `candidate` | | The directory is missing, tampered or not verifiable. Both inputs are verified before comparability is checked. |
| `COMPARISON_NOT_COMPARABLE` | `baseline` or `candidate` | `MEASUREMENT_NOT_RETAINED` | A v0.1-v0.4 artifact has no retained measurement. |
| `COMPARISON_NOT_COMPARABLE` | | `CASES_DIFFER` | The case IDs differ, or are the same IDs in a different order. |
| `COMPARISON_NOT_COMPARABLE` | | `SCORERS_DIFFER` | A scorer's ID, kind, threshold, `required` flag or custom implementation differs. |

In Go, `sdk.NewComparer().Compare(ctx, sdk.ComparisonRequest{BaselineDir: b, CandidateDir: c})`
returns a `Comparison` with `Recommendation`, `Baseline`, `Candidate`, `Count`, `Changes`
and `JSON`. `JSON()` is the bytes the CLI prints, without the trailing newline. A refusal
is a `*sdk.ReadError` with `Code`, `Scope`, `Input` and `Reason`. Cancellation returns
`context.Canceled` or `context.DeadlineExceeded` itself. `Inspector.Inspect` now
returns `*sdk.ReadError` too, with the same message as before.

Limits:

- The comparability key is the whole retained measurement contract, so a custom scorer's
  `implementation` is part of it. A scorer upgraded from `@1` to `@2` is a different
  instrument, and compare refuses it with `SCORERS_DIFFER`. Publish a new baseline.
- `config_digest` is neither part of the key nor in the output. It hashes the target,
  which is what a comparison varies.
- `inspect` and `compare` share one reader, which is stricter than V5's `inspect`. A
  re-hashed artifact holding a verdict or a scorer observation evalkit cannot produce
  is refused as `ARTIFACT_VERIFICATION_FAILED`. Artifacts evalkit published are
  unaffected.
- A status change inside UNKNOWN, such as ERRORED to SKIPPED, is UNCHANGED and not listed.
- There is no `--timeout`. Each side reads three files of at most 16 MiB each. From V7, `run` keeps `run.json` within that limit, so every COMPLETED run can be read back.
- As with V5, the checksums prove self-consistency, not who produced either artifact.

## V7 retained output and attributed diagnostics

`evalkit compare` now shows why a cell moved. Each run keeps what its target wrote to stdout for every case, and each CASE and SCORER diagnostic names the case it is about.

Every case whose target ran keeps its stdout in `run.json` at `cases[i].output`, or `case.output` for a version 1 run:

```json
"output": {"form": "JSON", "size": 17, "sha256": "863528c9b14f3ea195d472c71227d5a606cecb578932bdad916d7f01511e4671", "bytes": "eyJhbnN3ZXIiOiJ5ZXMifQo="}
```

- `form` is `JSON` (exactly one JSON value), `MALFORMED` (any other output), `OVERSIZED` (more than the 1 MiB target limit) or `EMPTY` (no bytes, including a target that never started).
- `size` and `sha256` cover every byte the target wrote, including bytes past the limit.
- `bytes` is the base64 of the bytes the run kept. When the run kept fewer than `size` bytes, `truncated` is `true` and `bytes` is a prefix.
- A run keeps at most 8 MiB of output, split evenly over its cases. Each case keeps up to 1 MiB or 8 MiB divided by the number of cases, whichever is smaller. That is the whole output for up to 8 cases, about 82 KiB per case at 100 cases, and about 8 KiB at 1000.
- Failed, malformed and oversized targets keep their output too. An INVALID case keeps none, because its target never ran. A cancelled run publishes nothing, and its stdout record holds no output.
- `retained_output` in `run.json` and in `inspect` says what the run kept: `RETAINED` (every output whole), `TRIMMED` (at least one output is a prefix) or `NONE` (no output, see the limits below).
- `report.json` is unchanged.

To read a whole output:

```sh
jq -r '.cases[] | select(.id=="case-2") | .output.bytes | @base64d' pr-run/run.json
```

Each CASE and SCORER diagnostic has a `subject`. SCORER diagnostics also name the `scorer_id`, and RUN diagnostics have none:

```json
{"code": "TARGET_EXECUTION_ERROR", "scope": "CASE", "message": "target process failed", "subject": {"case_index": 1, "case_id": "case-2"}}
```

`case_index` is the position in `cases` and in `measurement.case_ids`, so it tells apart duplicate and empty case IDs. Messages stay static. evalkit never copies target stderr or scorer error text.

In `compare`, the `from` and `to` of each listed change carry `output` and `diagnostics`. `output` has the form, size, SHA-256, `truncated`, and a `preview` of the first 1 KiB as text. `complete` is `true` when `preview` is every byte the target wrote. Equal `size` and `sha256` mean the target wrote the same thing, even when both sides are truncated. `diagnostics` are the case's CASE diagnostics plus the SCORER diagnostics of that change's scorer. Both fields are omitted for a side that kept nothing.

Real output for the V6 example above, where the candidate's target answers `no` on `case-2` (`jq '.changes[0]'`, exit 1):

```json
{
  "case_id": "case-2", "scorer_id": "exact-required", "required": true, "transition": "REGRESSED",
  "from": {"cell": "PASSING", "case_state": "SCORED", "status": "SUCCEEDED", "score": 1,
           "output": {"form": "JSON", "size": 17, "sha256": "863528c9b14f3ea195d472c71227d5a606cecb578932bdad916d7f01511e4671",
                      "truncated": false, "complete": true, "preview": "{\"answer\":\"yes\"}\n"}},
  "to":   {"cell": "FAILING", "case_state": "SCORED", "status": "SUCCEEDED", "score": 0,
           "output": {"form": "JSON", "size": 16, "sha256": "424d38bfd14d06dea6829f4745fd6a0e9a8591644d4fd1c836ae1045408d062b",
                      "truncated": false, "complete": true, "preview": "{\"answer\":\"no\"}\n"}}
}
```

Real output when the candidate's target prints `partial`, writes a marker to stderr and exits 1 (`jq '.changes[0].to'`, exit 3). The marker is in none of the artifact files, nor in the `run` stdout:

```json
{"cell": "UNKNOWN", "case_state": "ERRORED", "status": "ERRORED", "score": null,
 "output": {"form": "MALFORMED", "size": 7, "sha256": "9834a14ab9bcaa0f6a8da71073617eac8f004e596a3fa11d807b84631b825d9d",
            "truncated": false, "complete": true, "preview": "partial"},
 "diagnostics": [{"code": "TARGET_EXECUTION_ERROR", "scope": "CASE", "message": "target process failed"}]}
```

In Go, `sdk.Endpoint` gains `Output` and `Diagnostics`, `Change.OutputChanged()` reports whether the two runs' outputs differ (and whether both sides kept one), and `Inspection.RetainedOutput()` returns `retained_output`.

Limits:

- Artifacts from v0.6.0 and earlier verify, inspect and compare exactly as before. They have no `output`, `subject` or `retained_output`, so neither command shows them.
- evalkit v0.6.0 and earlier refuse V7 artifacts with `ARTIFACT_VERIFICATION_FAILED`, as v0.4 refused the `measurement` that v0.5 added.
- evalkit never reads target stderr, but it keeps stdout. Treat every artifact directory, and the `run` stdout in your CI logs, as holding your target's answers. Base64 is an encoding, not protection, and it hides those answers from secret scanners that search for plain text. Print only the result JSON on stdout, and send logs, tokens and other secrets to stderr, which evalkit never reads.
- If `run.json` would exceed the 16 MiB read limit, evalkit cuts every output to one common prefix length that fits, down to no bytes, which keeps form, size and SHA-256. If that does not fit either, the run keeps no output and records `retained_output: NONE`. If the run record does not fit even without outputs, the run fails with `ARTIFACT_COMMIT_FAILED` instead of publishing an artifact that `inspect` would refuse.
- A cell is listed only when its state or score changes. A change of output alone lists nothing.
