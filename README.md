# evalkit — evalkit walking skeleton

Outside-in implementation of the first vertical slice of the evalkit specification.

**V1 scope:** one JSON case, one direct command target, one required exact-match scorer, deterministic quality verdict, and locally committed JSON run evidence. This is a development skeleton, not a release-ready or security-isolated runner.

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

- Strict versioned JSON configuration, exactly one case and one exact-match scorer.
- Command executed **without a shell**, with a controlled temporary working directory, a reduced environment (PATH only), JSON request on stdin, and one JSON result on stdout.
- Command output bounded to 1 MiB; no raw target stderr leaked into public diagnostics.
- Exact-match compares JSON values structurally. Correct result scores 1, mismatch scores 0. A malformed target result makes coverage incomplete and the completed quality INDETERMINATE.
- Required run/report evidence is staged, hashed, synced and published via a directory rename with `commit.json` written last in staging. Basic reader verifies integrity and report consistency.
- Output is never intentionally overwritten by a normal repeated run.

## Deliberately out of scope

Multiple cases/scorers, custom Go scorer API, historical comparison, durable retry scheduler, enforceable process-tree isolation, network/filesystem sandboxing, external providers, arbitrary extension loading, and sophisticated transaction recovery remain later slices. This local executor **must not be used** where those security controls are required.

Known hardening work before production readiness: atomic exclusive artifact-path reservation under adversarial races, crash/fault injection around rename and directory fsync, cleanup uncertainty accounting, and stronger termination of process descendants. The v1 tests verify the nominal and deterministic failure paths, not these stronger environmental guarantees.

## Homebrew releases (macOS and Linux)

Each accepted vertical slice is published as a verified `v0.N.0` GitHub release with
cross-compiled Linux/macOS amd64/arm64 CLI archives and corresponding SHA256SUMS.
The repository also acts as its own Homebrew tap (a separate tap repository is not required):

```sh
brew tap ai-evals/evalkit https://github.com/ai-evals/evalkit.git
brew install ai-evals/evalkit/evalkit
# or: brew upgrade ai-evals/evalkit/evalkit
evalkit version
```

Releases are generated **only after the `CI` workflow passes on `main`**. The publishing
workflow tags the tested commit, builds release archives, creates the GitHub release,
and updates `Formula/evalkit.rb` using actual release archive checksums.
Do not assume a new version is installable until the release workflow finishes.

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
