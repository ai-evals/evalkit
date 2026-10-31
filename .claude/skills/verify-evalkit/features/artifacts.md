# Artifact publication

Every completed run publishes durable evidence into the `--artifact-dir`: `run.json`, `report.json`, and a `commit.json` holding the SHA256 of the other two. A repeated run into an existing directory fails and leaves the earlier evidence untouched.

## Sub-features

- `artifact-publish` writes exactly `run.json`, `report.json`, and `commit.json` with mode `0600`.
- `artifact-commit-hashes` records `run_sha256` and `report_sha256` in `commit.json`.
- `artifact-no-overwrite` makes a second run into the same dir return `state: FAILED`, `ARTIFACT_COMMIT_FAILED`, and exit `2`, leaving the files unchanged.
- `artifact-none-on-cancel` publishes no artifact dir for a cancelled run.

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

## Gotchas

- The rejected second run still prints its own fresh `run_id` and case results on stdout. Only the exit code, `state`, and diagnostics say it was not published.
- `artifact_dir` appears in the run stdout only when publishing succeeded.
- Hostile-race and crash-fault guarantees around the rename are explicitly unproven (README "Known hardening work"). Don't claim them from this recipe.
