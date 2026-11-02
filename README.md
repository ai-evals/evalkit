# evalkit

evalkit evaluates a command-line target against a JSON dataset. For each case it starts your target once, sends the case input on stdin, scores the JSON answer the target prints, and publishes the run as a directory of checksummed JSON evidence. `evalkit inspect` reads a published run back, and `evalkit compare` compares two runs cell by cell, so you can judge a pull request against the run from `main`.

Every completed run has one verdict. PASS means every case was scored by every required scorer and every required score met its threshold. FAIL means that coverage was complete but at least one required score fell below its threshold. INDETERMINATE means some required evidence is missing, for example because the target crashed or a case was invalid. `evalkit run` exits 0 for PASS, 1 for FAIL, 3 for INDETERMINATE, and 2 when there is no verdict. `evalkit compare` turns the candidate run's verdict into a recommendation with the same exit codes. SHIP means the candidate passed, so merge it. ITERATE means a required score fell short, so fix the target and run again. INVESTIGATE means required evidence is missing, so find out why before you trust the run.

## Install

evalkit runs on macOS and Linux. Releases ship for `amd64` and `arm64`.

### Install with Homebrew

The repository is its own Homebrew tap:

```sh
brew tap ai-evals/evalkit https://github.com/ai-evals/evalkit.git
brew install ai-evals/evalkit/evalkit
evalkit version
```

```text
evalkit 0.7.0
```

To update, run `brew upgrade ai-evals/evalkit/evalkit`. Homebrew 7 and later load formulae only from trusted taps. If `brew install` says that tap trust is required, run `brew trust --formula ai-evals/evalkit/evalkit` and install again.

### Install from a release archive

Each [release](https://github.com/ai-evals/evalkit/releases) has four archives named `evalkit_vVERSION_OS_ARCH.tar.gz`, for `darwin` and `linux` on `amd64` and `arm64`, and a `SHA256SUMS` file. Each archive holds one file, the `evalkit` binary. For example, on an Apple silicon Mac:

```sh
curl -fsSLO https://github.com/ai-evals/evalkit/releases/download/v0.7.0/evalkit_v0.7.0_darwin_arm64.tar.gz
curl -fsSLO https://github.com/ai-evals/evalkit/releases/download/v0.7.0/SHA256SUMS
shasum -a 256 -c --ignore-missing SHA256SUMS
tar -xzf evalkit_v0.7.0_darwin_arm64.tar.gz
./evalkit version
```

```text
evalkit_v0.7.0_darwin_arm64.tar.gz: OK
evalkit 0.7.0
```

Move `evalkit` to a directory on your `PATH`.

## Quickstart

In this tutorial we build evalkit and an example target from source, publish a passing run, then change one of the target's answers and use `evalkit compare` to find the regression and its cause. You need git, jq, and Go 1.26 or later. Every file we create goes under `tour/` in the clone.

### 1. Build evalkit and the example target

Clone the repository and build both binaries:

```sh
git clone https://github.com/ai-evals/evalkit.git
cd evalkit
mkdir -p tour/runs
go build -o tour/evalkit ./cmd/evalkit
go build -o tour/example-target ./examples/target
tour/evalkit version
```

```text
evalkit dev
```

A binary built from source reports `dev`. The example target reads one JSON request from stdin and prints `{"answer":"yes"}`.

### 2. Write a config

A config names the cases, the target, and the scorers. `examples/evalkit-v3.json` has two cases, one required `exact_match` scorer, and an execution policy:

```json
{
  "version": 3,
  "cases": [
    {"id": "case-1", "input": {"question": "ready?"}, "expected": {"answer": "yes"}},
    {"id": "case-2", "input": {"question": "still ready?"}, "expected": {"answer": "yes"}}
  ],
  "target": {"command": "/ABSOLUTE/PATH/TO/evalkit-example-target", "args": []},
  "scorers": [{"id": "exact", "kind": "exact_match", "threshold": 1, "required": true}],
  "execution": {"concurrency": 2, "scorer_max_attempts": 2}
}
```

The target path must be absolute. Write a copy that points at the example target:

```sh
sed "s#/ABSOLUTE/PATH/TO/evalkit-example-target#$PWD/tour/example-target#" examples/evalkit-v3.json > tour/baseline.json
```

### 3. Run the baseline

Run the evaluation and keep its stdout:

```sh
tour/evalkit run --config tour/baseline.json --artifact-dir tour/runs/baseline > tour/baseline.out
echo "exit $?"
```

```text
exit 0
```

Exit 0 means PASS. The file holds the run record. Look at the verdict and the first case:

```sh
jq '{state, quality, retained_output, first_case: .cases[0]}' tour/baseline.out
```

```json
{
  "state": "COMPLETED",
  "quality": "PASS",
  "retained_output": "RETAINED",
  "first_case": {
    "id": "case-1",
    "state": "SCORED",
    "target_status": "SUCCEEDED",
    "scorers": [
      {
        "id": "exact",
        "status": "SUCCEEDED",
        "score": 1,
        "attempts": 1
      }
    ],
    "output": {
      "form": "JSON",
      "size": 17,
      "sha256": "863528c9b14f3ea195d472c71227d5a606cecb578932bdad916d7f01511e4671",
      "bytes": "eyJhbnN3ZXIiOiJ5ZXMifQo="
    }
  }
}
```

The case's scorer succeeded with a score of 1, and `output` keeps the 17 bytes the target printed, in base64. evalkit also published the run as a directory:

```sh
ls tour/runs/baseline
```

```text
commit.json
report.json
run.json
```

### 4. Inspect the published run

`evalkit inspect` reads the directory back and checks its checksums and its verdict. It runs nothing:

```sh
tour/evalkit inspect --artifact-dir tour/runs/baseline | jq .
```

```json
{
  "schema_version": 1,
  "run_id": "4e677c6153458c12f1542d0d6edf11fd",
  "state": "COMPLETED",
  "quality": "PASS",
  "config_digest": "d900529a3672a120a2db1bbe86663a53923b465013134692a3761c6e86623e46",
  "integrity": "SELF_CONSISTENT",
  "measurement_accounting": "REPORTED_DEFINITION_CONSISTENT",
  "retained_output": "RETAINED",
  "authenticated_author": false
}
```

Your `run_id` and `config_digest` are different.

### 5. Change one answer and run again

Now we stand in for a change to your target. This script answers `no` to `case-2` and `yes` to every other case:

```sh
cat > tour/candidate-target.sh <<'EOF'
#!/bin/sh
read -r request
case "$request" in
	*'"case_id":"case-2"'*) echo '{"answer":"no"}' ;;
	*) echo '{"answer":"yes"}' ;;
esac
EOF
chmod +x tour/candidate-target.sh
sed "s#/ABSOLUTE/PATH/TO/evalkit-example-target#$PWD/tour/candidate-target.sh#" examples/evalkit-v3.json > tour/candidate.json
tour/evalkit run --config tour/candidate.json --artifact-dir tour/runs/candidate > tour/candidate.out
echo "exit $?"
```

```text
exit 1
```

Exit 1 means FAIL.

### 6. Compare the two runs

Compare the candidate with the baseline:

```sh
tour/evalkit compare --baseline tour/runs/baseline --candidate tour/runs/candidate > tour/compare.json
echo "exit $?"
jq '{recommendation, counts, changes: [.changes[] | {case_id, scorer_id, transition}]}' tour/compare.json
```

```text
exit 1
```

```json
{
  "recommendation": "ITERATE",
  "counts": {
    "UNCHANGED": 1,
    "IMPROVED": 0,
    "REGRESSED": 1,
    "LOST_EVIDENCE": 0,
    "GAINED_EVIDENCE": 0
  },
  "changes": [
    {
      "case_id": "case-2",
      "scorer_id": "exact",
      "transition": "REGRESSED"
    }
  ]
}
```

The recommendation is ITERATE. One cell, `case-2` under the `exact` scorer, went from passing to failing. The other cell did not change, so `changes` does not list it.

### 7. Read why the case moved

Each listed change carries what the target printed on both sides. Compare the two previews:

```sh
jq '.changes[0] | {from: .from.output.preview, to: .to.output.preview}' tour/compare.json
```

```json
{
  "from": "{\"answer\":\"yes\"}\n",
  "to": "{\"answer\":\"no\"}\n"
}
```

The expected answer stayed the same and the target's answer changed, so the target regressed. A preview holds at most the first 1 KiB. To read the whole output, decode it from the candidate's `run.json`:

```sh
jq -r '.cases[] | select(.id == "case-2") | .output.bytes | @base64d' tour/runs/candidate/run.json
```

```text
{"answer":"no"}
```

To clean up, run `rm -rf tour`.

You have published, inspected, and compared two runs, and traced a regression to the answer that caused it. To run the same loop on every pull request, see [Gate a pull request on the main baseline](#gate-a-pull-request-on-the-main-baseline).

## How-to guides

### Write a config and pick a version

Pick the version by where your scorers run:

- To score answers by exact JSON equality from the CLI, use version 3. It runs up to 16 cases at a time.
- To score answers with your own Go code, use version 4 through the [Go SDK](#go-sdk). The CLI rejects a `custom` scorer with `CONFIG_INVALID`.
- Versions 1 and 2 still run. Version 1 holds one case and one scorer. Version 2 is version 3 without `execution`, and it runs one case at a time.

To write a version 3 config:

1. Copy `examples/evalkit-v3.json`.
2. Set `target.command` to an absolute path or to a program name on your `PATH`, and put the arguments in `target.args`. evalkit does not use a shell.
3. Add one entry to `cases` for each question. `input` is the JSON value your target receives. `expected` is the JSON value its answer must equal.
4. Add scorers, and mark at least one `"required": true`. Only required scorers decide the verdict. Optional scorers appear in `compare` output but never change the verdict.
5. Make your target read one line of JSON from stdin, such as `{"case_id":"case-2","input":{"question":"still ready?"}}`, print exactly one JSON value to stdout, and exit 0.
6. Run the config once. If it is invalid, `run` prints `"state":"FAILED"` with a `CONFIG_INVALID` diagnostic whose message names the problem, and exits 2.

`exact_match` compares numbers by their text, so write `expected` numbers the way your target prints them. To `exact_match`, `1` and `1.0` are different answers. `scorer_max_attempts` matters only for custom scorers, because `exact_match` never fails.

### Gate a pull request on the main baseline

This loop gives each pull request one exit code and a list of the cells that changed against `main`. The steps are plain shell, so they fit any CI job.

1. On every push to `main`, run the evaluation:

    ```sh
    evalkit run --config evalkit.json --artifact-dir main-run --timeout 15m > main-run.out
    ```

2. Store `main-run/` where pull-request jobs can fetch it, such as a CI artifact or a cache.
3. In the pull-request job, build the pull request's target, fetch `main-run/`, and run:

    ```sh
    evalkit run --config evalkit.json --artifact-dir pr-run --timeout 15m > pr-run.out || true
    status=0
    evalkit compare --baseline main-run --candidate pr-run > compare.json || status=$?
    jq . compare.json
    exit "$status"
    ```

4. Act on the job's exit code:
    - 0, SHIP. Merge.
    - 1, ITERATE. A required score is below its threshold. `changes` lists the cells that got worse against `main`.
    - 3, INVESTIGATE. Required evidence is missing. Read the `diagnostics` and `output` of the `LOST_EVIDENCE` changes.
    - 2, refused. Read `.error` in `compare.json`, and see [Handle a refusal](#handle-a-refusal).

`|| true` lets the job reach `compare` after a FAIL or INDETERMINATE run. A run with no verdict publishes nothing, so `compare` then refuses with exit 2, and `pr-run.out` holds the run's diagnostics. The default `--timeout` is 10 seconds for the whole run, so set one that fits your target.

If your target's answers are sensitive, print `jq '{recommendation, counts}' compare.json` instead of the whole file, because each change's `preview` holds target output. `pr-run.out` holds the outputs too.

A pull request that changes `cases` or `scorers` cannot be compared with the old baseline. Gate it on the exit code of `run`, and the next push to `main` publishes a new baseline. The target itself may differ between the two runs, because `compare` ignores `config_digest`.

### Read a full retained output

`compare` previews the first 1 KiB of each output. `run.json` keeps more. To print a case's whole output as text:

```sh
jq -r '.cases[] | select(.id == "case-2") | .output.bytes | @base64d' pr-run/run.json
```

For a version 1 run, use `.case.output.bytes`. `@base64d` replaces invalid UTF-8 with U+FFFD and `-r` adds a newline. To get the exact bytes, decode with `base64` instead:

```sh
jq -r '.cases[] | select(.id == "case-2") | .output.bytes' pr-run/run.json | base64 -d > case-2.out
```

Before you treat the bytes as complete, check `truncated`:

```sh
jq '.cases[] | {id, form: .output.form, size: .output.size, truncated: (.output.truncated // false)}' pr-run/run.json
```

When `truncated` is `true`, `bytes` is a prefix, and `size` and `sha256` still describe everything the target wrote. A case has no `output` when its target never ran, as for an INVALID case, or when the run's `retained_output` is `NONE`.

### Handle a refusal

When `inspect` or `compare` cannot give an answer, it exits 2 and prints a refusal on stdout:

```json
{"error":{"code":"COMPARISON_NOT_COMPARABLE","scope":"COMPARISON","reason":"SCORERS_DIFFER"},"schema_version":1}
```

Find the `code`, or the `reason` when there is one, and act on it:

- `INVOCATION_INVALID`. Pass `--artifact-dir` to `inspect`, and both `--baseline` and `--candidate` to `compare`.
- `ARTIFACT_VERIFICATION_FAILED`. The directory is missing, incomplete, edited, or written by a newer evalkit. For `compare`, `input` names the side. Check the path. If a newer evalkit may have written it, upgrade and try again. Otherwise run the evaluation again.
- `CASES_DIFFER`. The two runs have different case IDs, or the same IDs in a different order. Publish a new baseline with the candidate's config.
- `SCORERS_DIFFER`. A scorer's ID, kind, threshold, `required` flag, or custom `implementation` differs. A custom scorer upgraded from `@1` to `@2` counts. Publish a new baseline.
- `MEASUREMENT_NOT_RETAINED`. evalkit 0.4 or earlier wrote the directory that `input` names. Run that evaluation again with the current evalkit.

`run` never refuses. When it cannot reach a verdict, it prints its record with `state` set to `FAILED` or `CANCELLED` and a RUN diagnostic, and exits 2. For `ARTIFACT_COMMIT_FAILED`, pass an `--artifact-dir` that does not exist yet, inside a directory that does. For `RUN_DEADLINE_EXCEEDED`, raise `--timeout`.

## Reference

### Commands

| Command | Prints on stdout | Exit |
| --- | --- | --- |
| `evalkit run --config FILE --artifact-dir DIR [--timeout DURATION]` | The run record | 0 PASS, 1 FAIL, 3 INDETERMINATE, 2 no verdict |
| `evalkit inspect --artifact-dir DIR` | The inspection, or a refusal | 0 verified, 2 refused |
| `evalkit compare --baseline DIR --candidate DIR` | The comparison, or a refusal | 0 SHIP, 1 ITERATE, 3 INVESTIGATE, 2 refused |
| `evalkit version` | `evalkit VERSION` | 0 |

- `run`, `inspect`, and `compare` print one line of JSON.
- `--timeout` is the deadline for the whole run, as a Go duration such as `90s` or `15m`. The default is `10s`.
- `DIR` for `run` must not exist, and its parent directory must exist. Otherwise the run executes every case and then fails with `ARTIFACT_COMMIT_FAILED`.
- `run` with a missing or invalid flag prints `invalid run invocation` on stderr, and with an unreadable config it prints `cannot read configuration`. Both print nothing on stdout and exit 2.
- An unknown command, or no command, prints the usage line on stderr and exits 2.
- `inspect` and `compare` execute nothing, write nothing, and have no `--timeout`.

### Verdicts

| Quality | Recommendation | Exit |
| --- | --- | --- |
| `PASS` | `SHIP` | 0 |
| `FAIL` | `ITERATE` | 1 |
| `INDETERMINATE` | `INVESTIGATE` | 3 |
| No verdict, for a `FAILED` or `CANCELLED` run, a refusal, or an invocation error | None | 2 |

`run` decides the verdict by these rules, and `inspect` and `compare` check stored verdicts against them:

- A case is `INVALID` when its ID is empty or duplicated, or when its `input` or `expected` is not exactly one JSON value. Its target never runs, and its scorers are `SKIPPED`.
- A case is `ERRORED` when its target fails. Its scorers are then all `ERRORED`.
- A case is also `ERRORED` when a required scorer is `ERRORED` or `UNEVALUABLE`.
- Every other case is `SCORED`.
- The run is `PASS` when every case is `SCORED` and every required scorer `SUCCEEDED` with a score at or above its threshold.
- The run is `FAIL` when every case is `SCORED`, every required scorer `SUCCEEDED`, and at least one required score is below its threshold.
- Every other run is `INDETERMINATE`.
- Optional scorers change neither the case state nor the verdict.

### Configuration

A config is one JSON value of at most 1 MiB. Unknown fields are errors. `version` is required, and versions 1, 2, 3, and 4 are supported.

| Field | Versions | Rules |
| --- | --- | --- |
| `case` | 1 | `{id, input, expected}`. `id` is not empty. `input` and `expected` are each one JSON value. |
| `scorer` | 1 | `{kind, threshold}`. `kind` is `exact_match`. `threshold` is from 0 to 1. |
| `cases` | 2, 3, 4 | A non-empty list of `{id, input, expected}`. An invalid entry becomes an `INVALID` case, not a config error. |
| `target` | All | `{command, args}`. `command` is not empty. `args` is an optional list of strings. |
| `scorers` | 2, 3, 4 | A non-empty list of `{id, kind, threshold, required, implementation}`. IDs are not empty and are unique. `threshold` is from 0 to 1. At least one scorer is `required`. `kind` is `exact_match`, or `custom` in version 4 through the SDK. A `custom` scorer needs an `implementation` that matches its SDK registration. |
| `execution` | 3, 4 | `{concurrency, scorer_max_attempts}`, both required. `concurrency` is from 1 to 16. `scorer_max_attempts` is from 1 to 10 and counts executions, not retries. |

`exact_match` scores 1 when the target's answer equals `expected` and 0 otherwise. It compares JSON values by structure, and numbers by their text.

### Target protocol

- evalkit starts `target.command` with `target.args` and no shell. `command` is an absolute path or a program name on evalkit's `PATH`. A relative path fails, because the target starts in a new temporary directory.
- The target's environment holds only `PATH`.
- stdin holds one line, such as `{"case_id":"case-2","input":{"question":"still ready?"}}`.
- The target answers by printing exactly one JSON value, of at most 1 MiB, on stdout and exiting 0. Anything else is a `TARGET_EXECUTION_ERROR`.
- evalkit discards stderr.
- Each case runs the target once. evalkit never retries a target.
- At the deadline, evalkit starts no new case, kills each running target's process group, and waits up to 2 seconds for it to exit.

### Artifact directory

`run` publishes only `COMPLETED` runs. It writes `run.json`, `report.json`, and then `commit.json` into a staging directory next to `DIR`, syncs them, and renames the staging directory to `DIR`.

| File | Contents |
| --- | --- |
| `run.json` | The run record, the same record `run` prints. |
| `report.json` | `run_id`, `quality`, and `score` for version 1 or `cases` without their `output` for later versions. |
| `commit.json` | `version`, which is 1, and `run_sha256` and `report_sha256`, the SHA-256 of the other two files. |

Fields of the run record:

| Field | Meaning |
| --- | --- |
| `schema_version` | 1. |
| `run_id` | 32 hex characters. |
| `state` | `COMPLETED`, `FAILED`, or `CANCELLED`. Only `COMPLETED` runs are published. |
| `quality` | `PASS`, `FAIL`, `INDETERMINATE`, or `null`. |
| `config_digest` | SHA-256 of the validated config, target included. |
| `case` | Version 1 only. `{id, target_status, scorer_status, score, output}`. |
| `cases` | Versions 2 to 4. A list of `{id, state, target_status, scorers, output}` in config order. |
| `cases[].state` | `SCORED`, `ERRORED`, or `INVALID`. |
| `cases[].target_status` | `SUCCEEDED`, `ERRORED`, or `SKIPPED`. |
| `cases[].scorers` | One `{id, status, score, attempts, reason}` per scorer, in config order. `status` is `SUCCEEDED`, `ERRORED`, `UNEVALUABLE`, or `SKIPPED`. `score` is present only for `SUCCEEDED`. `reason` is present only for `UNEVALUABLE`. |
| `measurement` | `{schema_version, case_ids, scorers}`, the contract `inspect` and `compare` check against. Artifacts from evalkit 0.4 and earlier have none. |
| `retained_output` | `RETAINED` when every output is whole, `TRIMMED` when at least one output is a prefix, or `NONE` when the run kept no output. Artifacts from evalkit 0.6.0 and earlier have none. |
| `diagnostics` | A list of `{code, scope, message, subject}`. |
| `artifact_dir` | `DIR` as passed to `run`. |

`output` is present for each case whose target ran:

| Field | Meaning |
| --- | --- |
| `form` | `JSON` for exactly one JSON value, `MALFORMED` for any other output, `OVERSIZED` for more than 1 MiB, or `EMPTY` for no bytes. A target that never started is `EMPTY`. |
| `size` | Every byte the target wrote, including bytes past 1 MiB. |
| `sha256` | SHA-256 of every byte the target wrote. |
| `truncated` | `true` when `bytes` is a prefix. Omitted when `false`. |
| `bytes` | Base64 of the bytes the run kept. Omitted when empty. |

`subject` is `{case_index, case_id}` for CASE diagnostics and adds `scorer_id` for SCORER diagnostics. `case_index` is the position in `cases`, so it tells apart duplicate and empty IDs. RUN diagnostics, and diagnostics from evalkit 0.6.0 and earlier, have no `subject`.

### Inspect output

| Field | Meaning |
| --- | --- |
| `schema_version`, `run_id`, `state`, `quality`, `config_digest` | From the run record. `state` is always `COMPLETED`. |
| `integrity` | `SELF_CONSISTENT`. The checksums in `commit.json` match the files. |
| `measurement_accounting` | `REPORTED_DEFINITION_CONSISTENT` when the verdict follows from the retained contract. `NOT_RETAINED` for artifacts from evalkit 0.4 and earlier. |
| `retained_output` | From the run record. Omitted for artifacts from evalkit 0.6.0 and earlier. |
| `authenticated_author` | Always `false`. |

### Compare output

| Field | Meaning |
| --- | --- |
| `schema_version` | 1. |
| `recommendation` | `SHIP`, `ITERATE`, or `INVESTIGATE`, from the candidate's quality alone. |
| `baseline`, `candidate` | `{run_id, quality}`. |
| `counts` | The number of cells for each transition, over every cell. All five keys are always present. |
| `changes` | The cells whose state or score changed, in case order and then scorer order. Each is `{case_id, scorer_id, required, transition, from, to}`. |
| `from`, `to` | `{cell, case_state, status, score, output, diagnostics}`. `output` and `diagnostics` are omitted for a side that kept none. |
| `output` | `{form, size, sha256, truncated, complete, preview}`. `preview` is the first 1 KiB as valid UTF-8. `complete` is `true` when `preview` is every byte the target wrote. Equal `size` and `sha256` on both sides mean the target wrote the same bytes, even when both are truncated. |
| `diagnostics` | The case's CASE diagnostics and that change's scorer's SCORER diagnostics, as `{code, scope, message}`. |

A cell is one case under one scorer. A cell is `PASSING` when its case is `SCORED` and its scorer `SUCCEEDED` with a score at or above the threshold. It is `FAILING` when its case is `SCORED` and its scorer `SUCCEEDED` with a score below the threshold. Every other cell is `UNKNOWN`. When an `UNKNOWN` cell's own scorer succeeded, `case_state` shows why. A required scorer failed on the same case, so the case is `ERRORED`.

| From | To `PASSING` | To `FAILING` | To `UNKNOWN` |
| --- | --- | --- | --- |
| `PASSING` | `UNCHANGED` | `REGRESSED` | `LOST_EVIDENCE` |
| `FAILING` | `IMPROVED` | `UNCHANGED` | `LOST_EVIDENCE` |
| `UNKNOWN` | `GAINED_EVIDENCE` | `GAINED_EVIDENCE` | `UNCHANGED` |

- A score change alone, such as 0.6 to 0.9, is listed as `UNCHANGED`.
- A status change inside `UNKNOWN`, such as `ERRORED` to `SKIPPED`, is not listed.
- An output change alone is not listed.
- An optional scorer's regression is listed but never changes the recommendation.
- Two runs are comparable when their `measurement` contracts are identical. That means the same case IDs in the same order and the same scorers. `config_digest` is not compared and not printed.

### Refusals

A refusal is `{"error":{...},"schema_version":1}` on stdout, with exit 2. `error` has a `code` and a `scope`, and an `input` and a `reason` when they apply.

| `code` | `scope` | `input` | `reason` | Cause |
| --- | --- | --- | --- | --- |
| `INVOCATION_INVALID` | `INVOCATION` | | | A flag is missing or unknown. |
| `ARTIFACT_VERIFICATION_FAILED` | `INSPECTION` or `COMPARISON` | `baseline` or `candidate` for `compare` | | The directory is missing, not `COMPLETED`, edited, or holds evidence evalkit cannot produce. `compare` verifies both inputs before it checks comparability. |
| `COMPARISON_NOT_COMPARABLE` | `COMPARISON` | `baseline` or `candidate` | `MEASUREMENT_NOT_RETAINED` | The artifact has no `measurement`. |
| `COMPARISON_NOT_COMPARABLE` | `COMPARISON` | | `CASES_DIFFER` | The case IDs differ, or their order differs. |
| `COMPARISON_NOT_COMPARABLE` | `COMPARISON` | | `SCORERS_DIFFER` | A scorer's ID, kind, threshold, `required` flag, or `implementation` differs. |

### Diagnostic codes

Messages are fixed strings. evalkit never copies target stderr or scorer error text into them.

| Code | Scope | Cause |
| --- | --- | --- |
| `CONFIG_INVALID` | RUN | The config failed validation. The message names the rule. |
| `ARTIFACT_COMMIT_FAILED` | RUN | Publication failed. `DIR` exists, its parent is missing, or `run.json` would exceed 16 MiB even without outputs. |
| `RUN_DEADLINE_EXCEEDED` | RUN | `--timeout` passed. The run is `CANCELLED`. |
| `RUN_CANCELLED` | RUN | The SDK caller cancelled the context. The run is `CANCELLED`. |
| `RUN_ID_UNAVAILABLE` | RUN | evalkit could not read random bytes for a run ID. |
| `DATASET_INVALID_CASE` | CASE | The case is `INVALID`. |
| `TARGET_EXECUTION_ERROR` | CASE | The target failed. The message is `target process failed`, `target output exceeds V1 size limit`, `target output must be exactly one JSON value`, or `cannot prepare target workspace`. |
| `SCORER_OPERATION_ERROR` | SCORER | A custom scorer returned an error, after any retries. |
| `SCORER_RESULT_INVALID` | SCORER | A custom scorer returned a score outside 0 to 1, or an unknown unevaluable reason. |

### Limits

| Limit | Value |
| --- | --- |
| Config file | 1 MiB |
| Target stdout per case | 1 MiB. More is `OVERSIZED` and a `TARGET_EXECUTION_ERROR`. |
| Retained output per run | 8 MiB, split evenly. Each case keeps up to 1 MiB or 8 MiB divided by the number of cases, whichever is smaller. That is all of it for up to 8 cases, about 82 KiB per case at 100 cases, and about 8 KiB at 1000. |
| Evidence file read | 16 MiB for each of `run.json`, `report.json`, and `commit.json`. If `run.json` would pass it, `run` first cuts every output to the longest common prefix that fits. If even an empty prefix does not fit, it keeps no output and records `NONE`. If the record still does not fit, the run fails with `ARTIFACT_COMMIT_FAILED`. |
| `concurrency` | 1 to 16 |
| `scorer_max_attempts` | 1 to 10 executions |
| Thresholds and scores | 0 to 1 |
| `compare` preview | 1 KiB per side |
| `--timeout` | 10 seconds by default, for the whole run |

## Go SDK

The package `github.com/ai-evals/evalkit/sdk` runs configs with custom scorers written in Go, and inspects and compares artifacts. Add it with `go get github.com/ai-evals/evalkit@v0.7.0`.

This program registers a scorer that compares the `answer` field without regard to case, and runs a config:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ai-evals/evalkit/sdk"
)

type answerScorer struct{}

func (answerScorer) Evaluate(_ context.Context, in sdk.ScorerInput) (sdk.Assessment, error) {
	var got, want struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal(in.ActualJSON(), &got) != nil || got.Answer == "" {
		return sdk.Unevaluable(sdk.ReasonEvidenceMissing), nil
	}
	if err := json.Unmarshal(in.ExpectedJSON(), &want); err != nil {
		return sdk.Assessment{}, err
	}
	if strings.EqualFold(got.Answer, want.Answer) {
		return sdk.Scored(1), nil
	}
	return sdk.Scored(0), nil
}

func main() {
	runner, err := sdk.NewRunner(sdk.Registration{ID: "answer", Implementation: "example/answer@1", Scorer: answerScorer{}})
	if err != nil {
		panic(err)
	}
	config, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	result, err := runner.Run(context.Background(), sdk.Request{ConfigJSON: config, ArtifactDir: os.Args[2]})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	quality, _ := result.Quality()
	fmt.Println(result.RunID(), quality)
}
```

The config uses version 4 and names the scorer by its registered ID and implementation:

```json
{
  "version": 4,
  "cases": [
    {"id": "case-1", "input": {"question": "ready?"}, "expected": {"answer": "YES"}},
    {"id": "case-2", "input": {"question": "still ready?"}, "expected": {"answer": "yes"}}
  ],
  "target": {"command": "/ABSOLUTE/PATH/TO/evalkit-example-target", "args": []},
  "scorers": [{"id": "answer", "kind": "custom", "implementation": "example/answer@1", "threshold": 1, "required": true}],
  "execution": {"concurrency": 2, "scorer_max_attempts": 3}
}
```

```sh
go run . evalkit.json run-1
```

```text
7a24146d7ea2140c30c116b9a5997d7f PASS
```

`evalkit inspect` and `evalkit compare` read SDK artifacts like CLI artifacts.

- `sdk.NewRunner(regs ...sdk.Registration)` registers scorers. An `ID` or `Implementation` that does not match the config fails the run with `CONFIG_INVALID`.
- `sdk.Scored(v)` takes a score from 0 to 1, and any other value is a `SCORER_RESULT_INVALID`. `sdk.Unevaluable(reason)` takes `sdk.ReasonEvidenceMissing`, `sdk.ReasonEvidenceInsufficient`, or `sdk.ReasonNotApplicable`, and makes a required scorer's case `ERRORED`.
- Return `sdk.Retryable()` to run the scorer again within `scorer_max_attempts`. Any other error is a `SCORER_OPERATION_ERROR`.
- evalkit never runs two calls of one registered scorer at the same time, even when `concurrency` is above 1.
- `Runner.Run` returns an `sdk.Result` with `RunID`, `State`, `Quality`, and `JSON`. For a run that is not `COMPLETED` it also returns an `*sdk.RunError` with the last diagnostic's `Code`. `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` report cancellation.
- `sdk.NewInspector().Inspect(ctx, dir)` returns an `sdk.Inspection` with `RunID`, `Quality`, `Integrity`, `MeasurementAccounting`, `RetainedOutput`, and `JSON`.
- `sdk.NewComparer().Compare(ctx, sdk.ComparisonRequest{BaselineDir: b, CandidateDir: c})` returns an `sdk.Comparison` with `Recommendation`, `Baseline`, `Candidate`, `Count`, `Changes`, and `JSON`. `JSON()` is the bytes `evalkit compare` prints, without the trailing newline. Each change's `From` and `To` carry `Output` and `Diagnostics`, and `Change.OutputChanged()` reports whether the target's output differs.
- `Inspect` and `Compare` refuse with an `*sdk.ReadError` that has `Code`, `Scope`, `Input`, and `Reason`. On cancellation they return `context.Canceled` or `context.DeadlineExceeded` itself.

## What evalkit guarantees and what it does not

evalkit checks that a run's verdict follows from its evidence. It does not contain the code it runs. Keep those two apart when you decide what to trust.

`inspect` and `compare` recompute each artifact's verdict from its own `measurement`, and they refuse an artifact whose stored verdict does not follow, even when its checksums match. `run` publishes an artifact by renaming a staging directory that it has fully written and synced, so a reader sees a whole artifact or none. A repeated run never overwrites an artifact, and a failed or cancelled run publishes nothing.

evalkit is not a sandbox. Your target runs as your user, with `PATH`, in a temporary directory, with the network and file access your user has. evalkit gives the target its own process group and kills that group at the deadline, but a target that starts a new session escapes the kill. evalkit has no built-in model providers and loads no plugins. Custom Go scorers are code compiled into your program, and they run in your process as trusted code. evalkit passes them a context, but it cannot interrupt one that ignores it.

The checksums prove that an artifact is self-consistent, not who wrote it. Anyone who can write the directory can change a file and re-hash it, so `authenticated_author` is always `false`. Treat an artifact as evidence from whoever controls its storage.

evalkit never reads target stderr, and it keeps stdout. Every artifact directory, and the `run` stdout in your CI logs, holds your target's answers. Base64 is an encoding, not protection, and it hides those answers from secret scanners that search for plain text. Print only the answer on stdout, and send logs, tokens, and other secrets to stderr.

Old binaries refuse newer artifacts. evalkit decodes artifacts strictly, so a field it does not know makes the artifact fail verification. evalkit 0.6.0 and earlier refuse artifacts from 0.7.0 with `ARTIFACT_VERIFICATION_FAILED`, as 0.4 refused the `measurement` that 0.5 added. Newer binaries read older artifacts, and evalkit 0.7.0 prints the same `inspect` and `compare` output for 0.6.0 artifacts as 0.6.0 did. Upgrade every machine that reads artifacts before, or together with, the machines that write them.

Some hardening is not done yet. evalkit does not reserve the artifact path atomically against a concurrent writer. No test injects crashes or faults around the rename and the directory sync. The file reader rejects symlinks and oversized files, but it is not hardened against a concurrent attacker on the same host.

## Contributing

Read [AGENTS.md](AGENTS.md) before you change evalkit. It lists the verification commands, the contracts to keep, and the release rules, for people and coding agents. Releases are automatic. Pull requests are squash-merged, so the pull request title lands on `main` as the commit, and CI derives the next version from it. A `feat` title releases a minor version. A `fix`, `perf`, or `revert` title releases a patch. Other types, such as `docs` and `chore`, release nothing. CI then publishes the archives and `SHA256SUMS` and commits the updated `Formula/evalkit.rb` and `CHANGELOG.md`. [CHANGELOG.md](CHANGELOG.md) records every release.

## License

Apache-2.0. See [LICENSE](LICENSE).
