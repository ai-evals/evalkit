# Artifact publication

Every completed run publishes durable evidence into the `--artifact-dir`: `run.json`, `report.json`, and a `commit.json` holding the SHA256 of the other two. `run.json` also keeps what each case's target wrote to stdout, never what it wrote to stderr. A repeated run into an existing directory fails and leaves the earlier evidence untouched.

## Sub-features

- `artifact-publish` writes exactly `run.json`, `report.json`, and `commit.json` with mode `0600`.
- `artifact-commit-hashes` records `run_sha256` and `report_sha256` in `commit.json`.
- `artifact-no-overwrite` makes a second run into the same dir return `state: FAILED`, `ARTIFACT_COMMIT_FAILED`, and exit `2`, leaving the files unchanged.
- `artifact-none-on-cancel` publishes no artifact dir for a cancelled run.
- `artifact-retained-output` keeps each executed case's stdout in `run.json` at `cases[i].output` (`case.output` for v1) with `form`, `size`, `sha256` and base64 `bytes`, and records `retained_output`. `report.json` has no `output`, and target stderr appears nowhere.

## How to get to it (user POV)

- Run `evalkit run ... --artifact-dir DIR`, then list `DIR`.
- Run the same command again with the same `DIR`.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`.

- **Publish.** Run `C=$($H config <id> v2 pass pub)`, then `$H run <id> pub -- run --config "$C" --artifact-dir art-pub`. `evidence/pub/artifact-listing.txt` shows exactly `commit.json`, `report.json`, and `run.json`, each `-rw-------`.
- **Hashes match.** Run `cd evidence/pub/artifact-snapshot && shasum -a 256 run.json report.json` and compare the output with `jq -r .run_sha256,.report_sha256 commit.json`. The values are equal.
- **No overwrite.** Run `$H run <id> pub-again -- run --config "$C" --artifact-dir art-pub`. Exit `2`. `jq -r '.state, .diagnostics[0].code'` gives `FAILED` and `ARTIFACT_COMMIT_FAILED`. `diff -r evidence/pub/artifact-snapshot <scratch>/art-pub` prints nothing.
- **Still readable.** Run `$H run <id> pub-inspect -- inspect --artifact-dir art-pub`. Exit `0`, `.integrity` is `SELF_CONSISTENT`, and `.run_id` equals the first run's `.run_id`, not the second's.
- **Retained output.** In `evidence/pub/artifact-snapshot`, `jq -c '[.cases[].output | {form, size}], .retained_output' run.json` gives `[{"form":"JSON","size":17},{"form":"JSON","size":17}]` and `"RETAINED"`, and `jq -r '.cases[0].output.bytes | @base64d' run.json` prints `{"answer":"yes"}`. `jq '[.. | objects | has("output")] | any' report.json` is `false`. `.retained_output` of `pub-inspect` is `RETAINED`.
- **Malformed output is kept.** Run `C=$($H config <id> v2 indeterminate pub-ind)`, then `$H run <id> pub-ind -- run --config "$C" --artifact-dir art-pub-ind`. Exit `3`. Every `.cases[].output` has `form: MALFORMED` and `size: 9` (`not-json` and a newline), and every diagnostic has a `.subject` naming its `case_index` and `case_id`.
- **No stderr.** Run `jq '.target = {command: "/bin/sh", args: ["-c", "IFS= read -r l; printf partial; echo MARKER-7f3 >&2; exit 1"]}' "$C" > <scratch>/stderr.json`, then `$H run <id> pub-stderr -- run --config <scratch>/stderr.json --artifact-dir art-stderr`. Exit `3`. In `evidence/pub-stderr`, `grep -c MARKER-7f3` gives `0` for `stdout.txt` and for each file in `artifact-snapshot/`. `jq -r '.cases[].output.bytes | @base64d' artifact-snapshot/run.json` prints `partial` twice, and piping it to `grep -c MARKER-7f3` gives `0`.

## Gotchas

- The rejected second run still prints its own fresh `run_id` and case results on stdout. Only the exit code, `state`, and diagnostics say it was not published.
- `artifact_dir` appears in the run stdout only when publishing succeeded.
- `bytes` is base64, so a plain `grep` of `run.json` never finds target output. Decode it before you search, as the **No stderr** step does.
- evalkit v0.6.0 and earlier refuse these artifacts with `ARTIFACT_VERIFICATION_FAILED`. Verify with the binary this run built.
- Hostile-race and crash-fault guarantees around the rename are explicitly unproven (README "Known hardening work"). Don't claim them from this recipe.
