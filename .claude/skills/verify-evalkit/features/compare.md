# Compare two runs

`evalkit compare --baseline DIR --candidate DIR` reads two published runs without executing or writing anything. It compares them cell by cell, one cell per case and scorer, prints one JSON document listing what got better or worse, and exits with the candidate's recommendation: `0` SHIP, `1` ITERATE, `3` INVESTIGATE. It refuses with exit `2` when either input fails verification or the two runs did not use the same measurement.

## Sub-features

- `compare-ship` reports `recommendation: SHIP` with exit `0` when the candidate is PASS, including after an IMPROVED cell.
- `compare-iterate` reports `ITERATE` with exit `1` and a `REGRESSED` change when the candidate is FAIL.
- `compare-investigate` reports `INVESTIGATE` with exit `3` and `LOST_EVIDENCE` changes when the candidate is INDETERMINATE.
- `compare-self` compares a run with itself: `changes` is `[]`, `counts.UNCHANGED` covers every cell, and the exit equals the exit of the `run` that published it.
- `compare-not-comparable` returns `COMPARISON_NOT_COMPARABLE` with exit `2` and a `reason` of `SCORERS_DIFFER` or `CASES_DIFFER`.
- `compare-tamper` and `compare-missing` return `ARTIFACT_VERIFICATION_FAILED` with exit `2` and `input` naming the bad side.
- `compare-invocation` returns `INVOCATION_INVALID` with exit `2` when `--baseline` or `--candidate` is missing.
- `compare-why` gives each side of a listed change its retained `output` (form, size, sha256, truncated, complete and a 1 KiB `preview`) and its `diagnostics`: the case's CASE diagnostics and that change's scorer's SCORER diagnostics.

## How to get to it (user POV)

- Publish two runs with `evalkit run`, then run `evalkit compare --baseline art-a --candidate art-b`.
- The README's `## V6 read-only comparison` section documents the output fields, the refusal table, and the limits. `## V7 retained output and attributed diagnostics` documents `output` and `diagnostics`.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`.
- Published runs exist. For each of `pass`, `fail` and `indeterminate`, run `C=$($H config <id> v2 <mode> v2-<mode>)`, then `$H run <id> v2-<mode> -- run --config "$C" --artifact-dir art-v2-<mode>`. They exit `0`, `1` and `3`. Also publish `art-v3-pass` from `$H config <id> v3 pass v3-pass`.

- **Iterate.** Run `$H run <id> cmp-iterate -- compare --baseline art-v2-pass --candidate art-v2-fail`. Exit `1`. `jq -r .recommendation` gives `ITERATE`, and `jq -c '[.changes[] | [.case_id, .scorer_id, .transition]]'` gives `[["case-2","exact-required","REGRESSED"],["case-2","exact-observational","REGRESSED"]]`.
- **Ship.** Swap the sides: `$H run <id> cmp-ship -- compare --baseline art-v2-fail --candidate art-v2-pass`. Exit `0`, `SHIP`, and the same two cells are `IMPROVED`.
- **Investigate.** Run `$H run <id> cmp-investigate -- compare --baseline art-v2-pass --candidate art-v2-indeterminate`. Exit `3`, `INVESTIGATE`, and `.counts.LOST_EVIDENCE` is `4`. Each `.to` has `cell: UNKNOWN`, `case_state: ERRORED` and `score: null`.
- **Self.** Run `$H run <id> cmp-self -- compare --baseline art-v2-fail --candidate art-v2-fail`. Exit `1`, the same as `v2-fail`'s run. `.changes` is `[]` and `.counts.UNCHANGED` is `4`.
- **Not comparable.** Run `$H run <id> cmp-scorers -- compare --baseline art-v2-pass --candidate art-v3-pass`. Exit `2`, `.error.code` is `COMPARISON_NOT_COMPARABLE`, `.error.scope` is `COMPARISON`, and `.error.reason` is `SCORERS_DIFFER`.
- **Tamper.** Copy and flip a verdict as in [inspect.md](./inspect.md) to make `art-tamper`, then run `$H run <id> cmp-tamper -- compare --baseline art-v2-pass --candidate art-tamper`. Exit `2`, `.error.code` is `ARTIFACT_VERIFICATION_FAILED`, and `.error.input` is `candidate`.
- **Missing dir.** Run `$H run <id> cmp-missing -- compare --baseline does-not-exist --candidate art-v2-pass`. Exit `2`, `ARTIFACT_VERIFICATION_FAILED`, and `.error.input` is `baseline`.
- **No flag.** Run `$H run <id> cmp-noflag -- compare --baseline art-v2-pass`. Exit `2`, and `.error.code` is `INVOCATION_INVALID`.
- **Why, malformed.** In `cmp-investigate`, `jq -c '.changes[0].to | {output, diagnostics}'` shows `output.form` `MALFORMED`, `size` `9`, `complete` `true` and `preview` `"not-json\n"`, and one diagnostic: `TARGET_EXECUTION_ERROR`, scope `CASE`, message `target output must be exactly one JSON value`. `.changes[0].from.output.preview` is `"{\"answer\":\"yes\"}\n"`.
- **Why, same answer.** In `cmp-iterate`, `jq -c '[.changes[] | .from.output.sha256 == .to.output.sha256]'` gives `[true,true]`. The target answered the same, so the expectation moved, not the target.
- **Read-only.** Afterwards, `diff -r evidence/v2-pass/artifact-snapshot <scratch>/art-v2-pass` prints nothing.

## Gotchas

- Compare output and refusals go to stdout as JSON, not stderr. Assert on stdout.
- The exit is the candidate's recommendation, not a measure of change. Two identical FAIL runs exit `1` with no changes.
- Optional scorers never change the exit. A `REGRESSED` cell with `required: false` still allows `SHIP`.
- `config_digest` is not compared. Runs that differ only in their target are comparable, and that is the intended use.
- The harness's `fail` mode changes the last case's `expected`, not the target. The measurement contract stays the same, so pass and fail runs are comparable, and their outputs have equal `sha256`.
- `output` and `diagnostics` are omitted for a side that kept none, such as an artifact from v0.6.0 or earlier. Comparing two such artifacts prints the V6 bytes.
- A cell whose output changed but whose state and score did not is not listed.
- v1 and v2 runs from the shipped examples refuse as `CASES_DIFFER`, because their case lists differ. v0.1-v0.4 artifacts refuse as `MEASUREMENT_NOT_RETAINED`, which this harness cannot produce.
