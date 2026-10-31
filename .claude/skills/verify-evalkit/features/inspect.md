# Inspect a past run

`evalkit inspect --artifact-dir DIR` reads a previously published run without executing anything. It reports the recorded verdict, whether the evidence is self-consistent, and whether measurement accounting could be checked. Tampered or missing evidence is rejected.

## Sub-features

- `inspect-ok` returns `integrity: SELF_CONSISTENT`, `measurement_accounting`, and `authenticated_author: false` with exit `0`.
- `inspect-tamper` returns `ARTIFACT_VERIFICATION_FAILED` with exit `2` after any byte of `report.json` or `run.json` changes.
- `inspect-missing` returns `ARTIFACT_VERIFICATION_FAILED` with exit `2` for a nonexistent dir.
- `inspect-invocation` returns `INVOCATION_INVALID` with exit `2` when `--artifact-dir` is missing.

## How to get to it (user POV)

- Run `evalkit inspect --artifact-dir /path/to/previous/run`.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`.
- A published run exists. Run `C=$($H config <id> v2 pass base)`, then `$H run <id> base -- run --config "$C" --artifact-dir art-base`, which exits `0`.

- **Inspect.** Run `$H run <id> insp -- inspect --artifact-dir art-base`. Exit `0`. `jq -r .integrity,.measurement_accounting,.quality` gives `SELF_CONSISTENT`, `REPORTED_DEFINITION_CONSISTENT`, and `PASS`. `.run_id` equals `base`'s `.run_id`.
- **Read-only.** Afterwards, `diff -r evidence/base/artifact-snapshot <scratch>/art-base` prints nothing.
- **Tamper.** Copy the dir and flip the verdict: `cp -R <scratch>/art-base <scratch>/art-tamper && sed -i '' 's/"PASS"/"FAIL"/' <scratch>/art-tamper/report.json` (GNU sed: `sed -i`). Then run `$H run <id> insp-tamper -- inspect --artifact-dir art-tamper`. Exit `2`, and `.error.code` is `ARTIFACT_VERIFICATION_FAILED`.
- **Missing dir.** Run `$H run <id> insp-missing -- inspect --artifact-dir does-not-exist`. Exit `2` and `ARTIFACT_VERIFICATION_FAILED`.
- **No flag.** Run `$H run <id> insp-noflag -- inspect`. Exit `2`, and `.error.code` is `INVOCATION_INVALID`.

## Gotchas

- Inspect errors go to stdout as JSON, not stderr. Assert on stdout.
- Missing and tampered dirs give the same error code, so the code alone can't tell you which one happened.
- `authenticated_author` is always `false`. SHA256 proves self-consistency, not authorship.
- Artifacts from v0.1 to v0.4 report `measurement_accounting: NOT_RETAINED`. That is expected, not a regression. This harness can only produce current-format artifacts.
