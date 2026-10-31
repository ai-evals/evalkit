# evalkit verification map

This directory is the maintained source for verifying the user-facing behavior of the evalkit CLI. Read the index before driving the CLI, then use the matching feature file as the recipe.

## Baseline preconditions

- `H=.claude/skills/verify-evalkit/scripts/verify-evalkit.sh`, run from the repo root.
- `$H launch <id>` printed `READY`, and `$H doctor <id>` printed `HEALTHY`.
- Configs come from `$H config <id> v1|v2|v3 pass|fail|indeterminate <name>`, rendered from `examples/evalkit*.json`.
- Never drive a binary or scratch dir that this verification run did not create.

## Driving conventions

- Every evalkit call goes through `$H run <id> <label> -- <args>`. Labels are unique per run.
- Use a fresh `--artifact-dir` name per run unless the recipe is testing reuse on purpose.
- Assert with `jq` on `evidence/<label>/stdout.txt` and with `cat evidence/<label>/exit_code`.
- Treat commands as literal. Flags are `--config`, `--artifact-dir`, and `--timeout`. There are no short forms.

## Proof and skip reporting

- CLI proof is the command, stdout, stderr, and exit code, all captured by `$H run`.
- Artifact proof is `artifact-snapshot/` plus an `inspect` of the same dir.
- Record the feature ID and the entry point with every artifact.
- Report an unreachable path with the attempted command and the unmet precondition.
- Do not report an entry point as verified because a different one passed.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph describing the user-visible behavior. It then has exactly four H2 sections, in order: `Sub-features`, `How to get to it (user POV)`, `Driving it with verify-evalkit.sh`, `Gotchas`.

## Features

- [Run an evaluation](./run.md) covers v1, v2, and v3 configs with PASS, FAIL, and INDETERMINATE verdicts and their exit codes.
- [Artifact publication](./artifacts.md) covers the published run/report/commit evidence and the guarantee that a repeated run never overwrites it.
- [Inspect a past run](./inspect.md) covers read-only inspection, tamper detection, and invocation errors.
- [Deadlines and invocation errors](./deadlines-and-errors.md) covers `--timeout` cancellation, usage errors, and an unreadable config.
- [Version](./version.md) covers `evalkit version`.
