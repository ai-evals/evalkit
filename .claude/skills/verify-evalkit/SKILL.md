---
name: verify-evalkit
description: Drive the evalkit CLI (`evalkit run`, `evalkit inspect`, `evalkit version`) the way a user does, from a freshly built binary in an isolated scratch dir, and capture command/stdout/stderr/exit-code/artifact evidence. Use to prove a change to cmd/, internal/engine, examples/, or the config/artifact format actually works end to end, not just in `go test`.
---

# Verify evalkit

evalkit is a Go CLI. The user surface is the terminal: `evalkit run --config FILE --artifact-dir DIR [--timeout D]`, `evalkit inspect --artifact-dir DIR`, `evalkit version`. Each command prints one JSON object to stdout and communicates the verdict through the exit code. The secondary surface is the Go SDK in `sdk/` (`sdk.Runner`, `sdk.NewInspector`); it has no runnable app, so prove SDK changes with `go test ./sdk` and note it as test-level, not user-level, proof.

All work goes through one helper, run from the repo root:

```sh
H=.claude/skills/verify-evalkit/scripts/verify-evalkit.sh
```

It needs `bash`, `go` (go.mod says 1.23+), `git`, `sed`, `awk`, and `jq` for assertions.

## Launch

evalkit is short-lived: there is no server. Launch means building fresh binaries from the working tree into a per-run scratch dir.

```sh
$H launch            # or: $H launch my-run-id
```

Ready when it prints `READY run_id=<id> scratch=<dir> evidence=<dir>`. It builds `./cmd/evalkit` and `./examples/target` into `$TMPDIR/evalkit-verify/<id>/scratch/` and records the commit, the dirty-file count, and the Go version in `evidence/build.txt`. Launch refuses to reuse an existing RUN_ID. Set `EVALKIT_VERIFY_ROOT` to move the root.

Never verify with an `evalkit` from `PATH` or Homebrew. That is a released build, not your change.

## Doctor

```sh
$H doctor <id>
```

Read-only. It checks that the scratch dir was created by this harness, both binaries are executable, `evalkit version` answers, the build commit equals the current `HEAD`, and no `.go`/`go.mod` file under `cmd internal sdk examples` changed after the build. It ends with `HEALTHY` or `UNHEALTHY`. If you edit source after launch, doctor goes unhealthy. Launch a new run instead of driving a stale binary.

## Drive

1. Render a config from the shipped examples. The placeholder `/ABSOLUTE/PATH/TO/evalkit-example-target` is replaced for you:

   ```sh
   C=$($H config <id> v1|v2|v3 pass|fail|indeterminate <name>)
   ```

   - `pass`: the example target (always answers `{"answer":"yes"}`) matches every case.
   - `fail`: the last case's expected answer is changed to `"no"`, so the required exact-match scorer scores it 0.
   - `indeterminate`: the target becomes `/bin/echo not-json`, which ignores stdin and returns a malformed result.

   For any other config, write the JSON into the scratch dir yourself (`$H path <id>` prints it). Use absolute target paths and no shell.

2. Run evalkit through the harness so every call is recorded:

   ```sh
   $H run <id> <label> -- run --config "$C" --artifact-dir art-<label>
   $H run <id> <label> -- inspect --artifact-dir art-<other>
   ```

   The CWD is the scratch dir, so relative `--artifact-dir` paths land there. Each label must be unique. The helper prints `label=… exit=…` and then the JSON.

3. Assert on the recorded evidence, not on what you remember:

   ```sh
   E=$TMPDIR/evalkit-verify/<id>/evidence/<label>
   cat $E/exit_code; jq -r .state,.quality $E/stdout.txt
   ```

The exit-code contract: `0` PASS, `1` FAIL, `3` INDETERMINATE (all three mean `state: COMPLETED`), `2` framework error, cancellation, or invalid invocation. For `inspect`, `0` means verified and `2` means `ARTIFACT_VERIFICATION_FAILED` or `INVOCATION_INVALID`.

Feature recipes live in [features/README.md](features/README.md). Read the matching feature file before driving it, and cover every entry point it lists.

## Evidence

Each `run` writes `evidence/<label>/command.txt`, `stdout.txt`, `stderr.txt`, and `exit_code`. When the args include `--artifact-dir`, it also writes `artifact-listing.txt` and `artifact-snapshot/`, a copy of `run.json`, `report.json`, and `commit.json`, or `absent: <path>` if nothing was published. Rendered configs are copied to `evidence/config-<name>.json`, and `build.txt` ties everything to a commit.

Proof standards:

- Drive the built CLI binary. Do not call `engine.RunJSON` or test helpers and call it user proof.
- Prove both the verdict (stdout JSON and exit code) and the side effect (the published artifact dir, or its absence, plus `inspect` reading it back).
- A non-overwrite or tamper claim needs a before/after comparison (`diff -r` of the snapshot against the live dir), not only an exit code.
- The only fake target is `/bin/echo`, which is the real command-target boundary. Don't stub the engine.

## Cleanup

```sh
$H cleanup <id>
```

Removes only `<root>/<id>/scratch` (binaries, configs, artifact dirs), and only if the harness created it. Evidence stays at `$TMPDIR/evalkit-verify/<id>/evidence/`. Report that path. Target processes are children of evalkit and are killed at the run deadline, so a normal run leaves nothing to kill. If you started a target by hand, kill that PID, never by process name.

## Isolation

Runs are fully isolated by RUN_ID, so any number can go side by side. Nothing writes into the repo except a `go build` cache. Never point `--artifact-dir` at a path inside the repo or at someone else's run.
