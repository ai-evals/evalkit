# Command scorers

A user who does not write Go scores cases with graded scores from `evalkit run`. A version 5 config names a `command` scorer, a program in any language. evalkit sends it one JSON line per case with the case input, the expected value, and the target's answer, and reads back a score from 0 to 1 or an unevaluable reason. Required command scorers decide the verdict like any other required scorer.

## Sub-features

- `command-graded` runs `examples/evalkit-v5.json` with the example word-overlap scorer and records scores between 0 and 1.
- `command-pass-fail` gives PASS with exit `0` and FAIL with exit `1` by the scorer's threshold.
- `command-inspect` verifies a v5 artifact whose `measurement` holds the scorer's `command` and `args`.
- `command-compare` gives SHIP for two identical v5 runs and refuses with `SCORERS_DIFFER` when the scorer's `args` or `command` changed.
- `command-errors` records `SCORER_OPERATION_ERROR` for a scorer that exits non-zero, with exit `3`.
- `command-deadline` cancels the run at `--timeout` while a scorer sleeps, and publishes nothing.
- `command-version-gate` rejects a command scorer in a version 2, 3, or 4 config with `CONFIG_INVALID`.

## How to get to it (user POV)

- Build a scorer, for example `go build -o evalkit-example-scorer ./examples/scorer`.
- Copy `examples/evalkit-v5.json`, set the target and scorer paths, and run `evalkit run --config FILE --artifact-dir DIR`.
- Read scores in `jq '.cases[].scorers'` on the run stdout.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`, which includes `example scorer executable`.
- `S` is the scratch dir from `$H path <id>`.

- **Graded pass.** Run `C=$($H config <id> v5 pass v5-pass)`, then `$H run <id> v5-pass -- run --config "$C" --artifact-dir art-v5-pass`. Exit `0`, `.quality` is `PASS`, and `jq -c '[.cases[].scorers[0].score]'` is `[1,0.6666666666666666]`. `.measurement.scorers[0].kind` is `command`.
- **Graded fail.** Run `C=$($H config <id> v5 fail v5-fail)`, then `$H run <id> v5-fail -- run --config "$C" --artifact-dir art-v5-fail`. Exit `1`, `.quality` is `FAIL`, and `case-2` scores `0`.
- **Inspect.** Run `$H run <id> v5-inspect -- inspect --artifact-dir art-v5-pass`. Exit `0`, and `.measurement_accounting` is `REPORTED_DEFINITION_CONSISTENT`.
- **Compare identical runs.** Run the pass config again into `art-v5-pass-again`, then `$H run <id> v5-compare-same -- compare --baseline art-v5-pass --candidate art-v5-pass-again`. Exit `0` and `.recommendation` is `SHIP`.
- **Compare after an args change.** Write `jq '.scorers[0].args = ["--strict"]' "$C" > $S/v5-args.json`, run it into `art-v5-args`, then `$H run <id> v5-compare-args -- compare --baseline art-v5-pass --candidate art-v5-args`. Exit `2`, and `.error` is `{"code":"COMPARISON_NOT_COMPARABLE","scope":"COMPARISON","reason":"SCORERS_DIFFER"}`.
- **Operation error.** Write `jq '.scorers[0].command = "/bin/false"' "$C" > $S/v5-false.json` and run it. Exit `3`, `.quality` is `INDETERMINATE`, and each case has one `SCORER_OPERATION_ERROR` with `subject.scorer_id` `overlap` and `attempts` `1`.
- **Deadline.** Write a scorer `$S/sleepy.sh` that reads stdin, prints a secret on stderr, and sleeps 30 seconds. Point the config at it and run with `--timeout 1s`. The run returns in about 1 second with exit `2`, `.state` `CANCELLED` and `RUN_DEADLINE_EXCEEDED`, `artifact-listing.txt` says `absent:`, and `grep -r` finds the secret nowhere under the evidence dir.
- **Version gate.** Write `jq '.version = 4' "$C" > $S/v4-command.json` and run it. Exit `2`, and the diagnostic is `CONFIG_INVALID` with `command scorers require configuration version 5`.

## Gotchas

- `command` and `args` are part of the `measurement`, so two runs compare only when they name the scorer by the same string. A scorer built into two different scratch dirs makes `compare` refuse with `SCORERS_DIFFER`. Use one path, or a program name on `PATH`, for runs you compare.
- A relative `command` fails with `SCORER_OPERATION_ERROR`, because the scorer starts in a new temporary directory.
- The scorer's environment holds only `PATH`. A judge that needs an API key cannot read it from evalkit's environment, so it must load credentials itself, for example from a secrets manager.
- Exit `75` asks for another attempt within `scorer_max_attempts`. Every other non-zero exit is a `SCORER_OPERATION_ERROR` on the first attempt.
- The example scorer prints `{"unevaluable":"EVIDENCE_MISSING"}` when `output.answer` or `expected.answer` is missing or null. It exits 1, a `SCORER_OPERATION_ERROR`, when either is not a string.
- The CLI still rejects a `custom` scorer in a v5 config, because only the SDK registers Go scorers.
