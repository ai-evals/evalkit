---
name: formal-verify-verdict
description: Bounded-exhaustive verification of evalkit's PASS / FAIL / INDETERMINATE quality verdict, covering the producer (evaluateCase, retryOperation, runCases, qualityOfCases, publish) and the inspector (VerifyArtifact, verifyMeasurementAccounting). Use it before you merge any change to internal/engine/v2.go, v3.go, v4.go, retry.go, extensions.go, inspection.go, or the verdict and publish parts of engine.go, or when you change the meaning of a scorer status, case state, required scorer, threshold or attempt budget.
---

# Formal verification: the quality verdict

**Property.** evalkit says PASS only when every case was scored by every required scorer and each score meets its threshold. It says FAIL only when that coverage is complete and some required score is below its threshold. Every other case is INDETERMINATE. The inspector accepts a stored verdict only if it follows from the artifact's own contract.

**Harm if it breaks.** A team ships a model on a PASS that had missing or failed required evidence, or trusts an inspected artifact whose verdict the run could never have produced.

## Tool and why

Go has no verifier that the team can install and that handles this code (generics, `reflect.DeepEqual`, real processes, the filesystem). So this check runs the real code on every input in a bounded domain, chosen so that every branch of the verdict logic sees each of its distinct cases. It does not test a hand-written model. The oracles in the test come from the README contract and never call the code under test. The check needs only `go` and `/bin/sh`, so it installs nothing.

## Run it

```sh
.claude/skills/formal-verify-verdict/run.sh          # P1-P4, about 50 s
.claude/skills/formal-verify-verdict/run.sh P3       # pure aggregation only, about 0.1 s
.claude/skills/formal-verify-verdict/run.sh 'P1|P2'  # real-pipeline checks only
```

- Exit 0: P1 to P3 pass, and P4 reports only the lines in `known-findings.txt`.
- Exit 1: the script prints `NEW VIOLATIONS:` followed by each counterexample.
- Exit 2: the test did not build or run.

The harness is `internal/engine/formal_verdict_test.go`. It is behind the build tag `formalverify`, so plain `go test ./...` and CI never run it.

## The properties

| ID | What it checks | Code it drives |
| --- | --- | --- |
| P1 | Every single-case run gives the contract verdict, case state, per-scorer status, score and attempt count. The published artifact passes `VerifyArtifact` with the same verdict. | `RunJSONWithScorers` → `runV4` → `runCases` → `evaluateCase` → `retryOperation` → `qualityOfCases` → `publish` → `VerifyArtifact` |
| P1 (budget) | A custom scorer runs exactly as many times as `attempts` records, and never more than `scorer_max_attempts`. | `retryOperation` |
| P2 | Runs that hold every behaviour pair, a failing target and a duplicate-ID pair keep dataset order, map each case correctly, and give the right verdict at concurrency 1 and 16. | `runCases` worker pool |
| P3 | `qualityOfCases` agrees with the contract on every abstract combination of case state × scorer status × score × required × threshold. | `qualityOfCases`, the one verdict function shared by the producer and the inspector |
| P4 | The test takes real artifacts, rewrites their measurement contract (required flags, thresholds) and stored verdict, and re-hashes them consistently. `VerifyArtifact` must reject every rewrite where the verdict contradicts the contract, there is no required scorer, or a threshold is outside [0,1]. | `VerifyArtifact`, `verifyMeasurementAccounting` |

## Bounds and assumptions

- Scorers: exactly 2 per run in P1 and P2. Each is `custom` or `exact_match`, and each is required or optional with at least one required, which config validation enforces. Thresholds are {0.5, 1}. With scores {0, 0.5, 1}, that covers below, equal and above.
- Custom scorer behaviours: `scored0`, `scored0.5`, `scored1`, `uneval` (valid UNEVALUABLE), `invalid` (score 1.5), `perm` (non-retryable error), `transOK` (retryable error, then success), `transForever`.
- `exact_match` behaviours: match and mismatch. Two exact scorers always agree, so a split pair is not a reachable input.
- `scorer_max_attempts` is {1, 2}. Concurrency is 1 for P1 and {1, 16} for P2.
- P3 bounds: up to 3 cases × 1 scorer, and up to 2 cases × 2 scorers. Thresholds are {0, 0.5, 1}. Statuses are SUCCEEDED with a score in {0, 0.5, 1} or with no score, ERRORED, UNEVALUABLE and SKIPPED. Case states are SCORED, ERRORED and INVALID.
- P4: 4 base artifacts × 4 required subsets × thresholds {-0.5, 0, 0.5, 1, 1.5}² × 3 verdicts, for 1200 rewrites.
- The target is `/bin/sh -c` echoing its stdin request. A case ID that starts with `tfail` makes it exit non-zero.
- Not covered: the V1 single-case path in `runJSON`, which has its own inline verdict code. Also not covered: cancellation and deadlines, crash or fsync faults in `publish`, concurrent writers to one artifact dir, more than 2 scorers or 3 cases, scores between the grid points, and NaN or infinite scores. JSON cannot carry NaN or Inf, and `validCandidate` rejects them for custom scorers. A pass covers this domain only.

## Reading a failure: real bug or modeling error?

Each violation is one line that names the run shape, the case and its scorer behaviours, what the engine did, and what the contract says. For example:

`[kinds=[custom custom] required=[false true] ...] case k3: state SCORED, contract says ERRORED (scorers [scored0 uneval])`

That reads: a required scorer returned UNEVALUABLE, yet the case was marked SCORED.

1. If your change was **not meant** to change the verdict semantics, the violation is a real bug. Fix the product code. Do not touch the oracle.
2. If your change **intentionally** changes the contract, for example a new scorer status, a new behaviour, or a new rule for optional scorers, update the oracle functions `oracleScorer`, `oracleCase`, `oracleVerdict` and `absOracle`, and the domains at the top of the test. Update this skill's property text and the README in the same change. The oracle must still state the contract in its own words. Never copy the engine's code into it.
3. A `run did not complete` or `rejected by VerifyArtifact` line in P1 or P2 usually means a config the harness builds is no longer valid. That is a harness problem. Fix `buildRun`.
4. Before you trust a change to the harness, break the code it guards and watch the check fail. See "Proof it can fail" below.

## Current findings (P4, recorded in `known-findings.txt`)

The current code breaks P4. These are findings, not modeling errors. Do not weaken P4 to make it pass.

- **F1: PASS without required coverage is accepted.** If the stored measurement marks no scorer as required, `verifyMeasurementAccounting` recomputes the verdict as PASS and accepts it. Steps: take a real artifact, set `required: false` on every `measurement.scorers` entry, re-hash `commit.json`, and run `evalkit inspect`. The result is PASS with `REPORTED_DEFINITION_CONSISTENT`. evalkit itself refuses to run such a config (`at least one required scorer is needed`).
- **F2: thresholds outside [0,1] are accepted.** A measurement threshold of -0.5 turns a score of 0 into PASS, and 1.5 turns a score of 1 into FAIL. The V2+ inspector path never range-checks thresholds. The legacy V1 path does: `invalid required scorer contract`.
- The INDETERMINATE lines in `known-findings.txt` are the same two gaps on artifacts whose verdict happens to be INDETERMINATE.

Impact: the inspector does not authenticate authors (`AuthenticatedAuthor: false`), so anyone who can rewrite and re-hash an artifact can also rewrite its scores. These gaps matter because a buggy writer, or an edited artifact, gets "definition consistent" stamped on a contract evalkit could never have produced. The fix would be for `verifyMeasurementAccounting` to apply the same rules as `validateV2WithCustom`: at least one required scorer, and every threshold in [0,1]. After the fix, delete the matching lines from `known-findings.txt`.

## Proof it can fail

Each check below was run against a scratch break of real code, caught the break, and then the scratch change was reverted.

| Scratch break | Caught by |
| --- | --- |
| `qualityOfCases`: `< sc.Threshold` changed to `<=` | P1, P3 (`said FAIL, contract says PASS`) |
| `qualityOfCases`: case state check removed | P3 (`said PASS, contract says INDETERMINATE`) |
| `evaluateCase`: a required UNEVALUABLE no longer sets `requiredError` | P1 (`state SCORED, contract says ERRORED`) |
| `retryOperation`: one execution allowed past the limit | P1 (`executed 2 times ... budget 1`) |
| `verifyMeasurementAccounting`: verdict recompute removed | P4, as new violations (`verdict contradicts contract`) |
| `runCases`: worker writes into the wrong result slot | P2 (`dataset order lost`) |

## Coverage record and sync

Last confirmed at commit `8fcb0258370f04d542f1831dbd5d3c833805d67c` (v0.5.0).

Covered code:

- `internal/engine/v2.go`: `validateV2WithCustom` (the required/threshold rules that the oracle assumes), `evaluateCase`, `qualityOfCases`, `runCases`
- `internal/engine/v4.go`: `runV4`
- `internal/engine/v3.go`: `runV3`. It shares `runCases`, but P1 and P2 drive V4 configs only.
- `internal/engine/retry.go`: `retryOperation`, `retryScorer`, `retryAssessment`
- `internal/engine/extensions.go`: `validCandidate`
- `internal/engine/inspection.go`: `measurementForCases`, `verifyMeasurementAccounting`, `validObservedScore`
- `internal/engine/engine.go`: `exactMatchScore`, `publish`, `VerifyArtifact`

To see whether covered code changed since the sync commit:

```sh
git diff --stat 8fcb0258370f04d542f1831dbd5d3c833805d67c -- internal/engine/v2.go internal/engine/v3.go internal/engine/v4.go internal/engine/retry.go internal/engine/extensions.go internal/engine/inspection.go internal/engine/engine.go
```

If that diff is non-empty, read it against the properties above, update the oracle or the domains if the contract changed, and run `run.sh`. Move the sync commit forward only after you have confirmed that the harness still describes the code at the new commit. "The run passes" is not enough on its own: for example, a new scorer status that the domain never produces would also pass.
