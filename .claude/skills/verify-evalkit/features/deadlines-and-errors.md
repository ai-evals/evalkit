# Deadlines and invocation errors

A user can bound a run with `--timeout`. When the deadline passes, evalkit cancels the run, terminates the target's process group, publishes nothing, and exits `2`. Malformed invocations and unreadable configs fail fast with a one-line stderr message and exit `2`.

## Sub-features

- `deadline-cancel` reports `state: CANCELLED` and `RUN_DEADLINE_EXCEEDED` with exit `2`, close to the deadline.
- `deadline-no-artifact` leaves no artifact dir after a cancelled run.
- `deadline-no-stray` leaves no target process alive after cancellation.
- `error-usage` prints the usage line on stderr with exit `2` for an unknown subcommand.
- `error-invocation` prints `invalid run invocation` with exit `2` for missing flags or a non-positive `--timeout`.
- `error-config-read` prints `cannot read configuration` with exit `2` for a missing config file.
- `error-config-invalid` reports `state: FAILED` and `CONFIG_INVALID` with exit `2` for a schema-invalid config.

## How to get to it (user POV)

- Run `evalkit run --config FILE --artifact-dir DIR --timeout 1s`.
- Run `evalkit` with a wrong subcommand, missing flags, or a bad config path.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`.

- **Slow target.** Render the indeterminate config with `C=$($H config <id> v3 indeterminate slow-src)`, then make it sleep: `sed -e 's#"/bin/echo"#"/bin/sleep"#' -e 's#"not-json"#"30"#' "$C" > <scratch>/slow.json`.
- **Cancel.** Run `time $H run <id> slow -- run --config slow.json --artifact-dir art-slow --timeout 1s`. It takes about 1s, not 30s. Exit `2`. `jq -r '.state, .diagnostics[0].code'` gives `CANCELLED` and `RUN_DEADLINE_EXCEEDED`.
- **Nothing published.** `evidence/slow/artifact-listing.txt` reads `absent: …/art-slow`.
- **No stray.** `pgrep -fl "/bin/sleep 30"` prints nothing.
- **Usage.** Run `$H run <id> usage -- bogus`. Exit `2`, and stderr starts with `usage: evalkit run`.
- **Bad invocation.** Run `$H run <id> noflags -- run` and `$H run <id> zero-timeout -- run --config slow.json --artifact-dir z --timeout 0s`. Both exit `2` with stderr `invalid run invocation`.
- **Missing config.** Run `$H run <id> nofile -- run --config missing.json --artifact-dir x`. Exit `2` with stderr `cannot read configuration`.
- **Invalid config.** Run `echo '{"version":9}' > <scratch>/bad.json`, then `$H run <id> badcfg -- run --config bad.json --artifact-dir art-bad`. Exit `2`, and `.diagnostics[0].code` is `CONFIG_INVALID`.

## Gotchas

- The default `--timeout` is `10s`. A slow real target can be cancelled without anyone noticing unless you check `state`.
- `pgrep` matches other users' sleeps too. Use an unusual duration (`30`) and check right after the run.
- Process-group termination is not a sandbox. A target that escapes its group is out of scope (README V3).
