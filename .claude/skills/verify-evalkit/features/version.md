# Version

`evalkit version` prints the CLI's version string, which is how a user confirms which build they are running.

## Sub-features

- `version-print` prints `evalkit <version>` with exit `0`.

## How to get to it (user POV)

- Run `evalkit version`.

## Driving it with verify-evalkit.sh

Preconditions:

- `$H doctor <id>` is `HEALTHY`.

- **Print.** Run `$H run <id> version -- version`. Exit `0`, and stdout is `evalkit dev`.

## Gotchas

- Local builds print `dev`. Release builds inject the bare version (`0.5.1`, no `v`) through `-ldflags -X main.version`, from `internal/release` in the CI `release` job, which this harness does not reproduce. There is no `VERSION` file: the version is derived from git tags and Conventional Commits (`go run ./internal/cmd/release plan`).
- `version` must be the only argument. `evalkit version --json` prints the usage line and exits `2`.
