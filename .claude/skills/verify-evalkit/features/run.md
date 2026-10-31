# Run an evaluation

A user points `evalkit run` at a JSON config naming cases, a command target, and exact-match scorers. evalkit feeds each case to the target on stdin, scores the target's JSON answer, prints one JSON Run outcome, and exits with a code that encodes the quality verdict.

## Sub-features

- `run-v1` runs a single-case version 1 config (`case` plus `scorer`).
- `run-v2` runs a multi-case version 2 config with required and observational named scorers.
- `run-v3` runs a version 3 config with `execution.concurrency` and `scorer_max_attempts`, keeping cases in dataset order.
- `run-pass` reports `quality: PASS` with exit `0` when every required score meets its threshold.
- `run-fail` reports `quality: FAIL` with exit `1` when a required score misses.
- `run-indeterminate` reports `quality: INDETERMINATE` with exit `3` when a target result is malformed.

## How to get to it (user POV)

- Run `evalkit run --config FILE --artifact-dir DIR` in a terminal.
- Start from one of `examples/evalkit.json` (v1), `examples/evalkit-v2.json`, or `examples/evalkit-v3.json`, after replacing the target placeholder.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`.

- **v1 pass.** Run `C=$($H config <id> v1 pass v1-pass)`, then `$H run <id> v1-pass -- run --config "$C" --artifact-dir art-v1-pass`. Exit `0`, `jq -r .state,.quality` gives `COMPLETED` and `PASS`, and `.cases[0].scorers[0].score` is `1`.
- **v2 pass with two scorers.** Run `C=$($H config <id> v2 pass v2-pass)`, then `$H run <id> v2-pass -- run --config "$C" --artifact-dir art-v2-pass`. Exit `0`. `jq -c '[.cases[].scorers[].id]'` lists `exact-required` and `exact-observational` for both cases.
- **v2 fail.** Run `C=$($H config <id> v2 fail v2-fail)`, then `$H run <id> v2-fail -- run --config "$C" --artifact-dir art-v2-fail`. Exit `1`, `.quality` is `FAIL`, and `case-2`'s `exact-required` score is `0` while `case-1`'s is `1`.
- **v3 bounded execution.** Run `C=$($H config <id> v3 pass v3-pass)`, then `$H run <id> v3-pass -- run --config "$C" --artifact-dir art-v3-pass`. Exit `0`. `jq -c '[.cases[].id]'` is `["case-1","case-2"]`, in dataset order.
- **Indeterminate.** Run `C=$($H config <id> v2 indeterminate v2-ind)`, then `$H run <id> v2-ind -- run --config "$C" --artifact-dir art-v2-ind`. Exit `3`, `.quality` is `INDETERMINATE`, and every case has `state: ERRORED` and `target_status: ERRORED`.
- **Proof.** Also run `$H run <id> <label>-inspect -- inspect --artifact-dir art-<label>` for at least one verdict, and confirm `.quality` matches the run's stdout.

## Gotchas

- The shipped examples contain `/ABSOLUTE/PATH/TO/evalkit-example-target`. Running them unrendered gives a target error, not a config bug.
- The example target always answers `{"answer":"yes"}`. FAIL needs a changed `expected`, not a changed target.
- A target gets only `PATH` in its environment and a temp CWD. A target that needs other env vars errors, which shows up as INDETERMINATE.
- Exit `3` and exit `2` are different verdicts. INDETERMINATE is a completed run, and `2` is a framework failure.
- A config with an unsupported `version` reports `CONFIG_INVALID` with the message `only configuration version 1 is supported`, even though v2 to v4 are supported. Don't read that message as the supported-version list.
