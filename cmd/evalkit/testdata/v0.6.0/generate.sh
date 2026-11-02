#!/usr/bin/env bash
# Regenerates the v0.6.0 artifact corpus and golden.json that
# TestV7AC06V060ArtifactsReadByteIdentically replays against HEAD.
# Usage: generate.sh /path/to/evalkit-0.6.0 [out-dir]
# Build the binary from tag v0.6.0 (f127808); any later binary would record
# its own bytes as the golden and the test would prove nothing.
set -euo pipefail
bin="$1"
out="${2:-$(cd "$(dirname "$0")" && pwd)}"
"$bin" version | grep -qx 'evalkit 0.6.0' || { echo "need an evalkit 0.6.0 binary" >&2; exit 2; }
cd "$out"
rm -rf artifacts configs golden.json
mkdir artifacts configs

# $1 is the mode, $2 the one case it changes. stderr carries a marker that
# must never reach an artifact.
target='IFS= read -r line
case "$line" in *"\"case_id\":\"$2\""*)
  case "$1" in
    fail) printf "{\"answer\":\"no\"}\n"; exit 0;;
    indet) printf partial; echo secret-on-stderr >&2; exit 1;;
  esac;;
esac
printf "{\"answer\":\"yes\"}\n"'

config() { # version mode extra-json
  jq -n --arg t "$target" --arg mode "$2" --argjson v "$1" --argjson extra "$3" '
    {version: $v, target: {command: "/bin/sh", args: ["-c", $t, "sh", $mode, "case-2"]}} + $extra'
}
v2cases='{"cases":[{"id":"case-1","input":{"q":1},"expected":{"answer":"yes"}},{"id":"case-2","input":{"q":2},"expected":{"answer":"yes"}}],
  "scorers":[{"id":"exact-required","kind":"exact_match","threshold":1,"required":true},{"id":"exact-observational","kind":"exact_match","threshold":0.5,"required":false}]}'
v3cases='{"cases":[{"id":"case-1","input":{"q":1},"expected":{"answer":"yes"}},{"id":"case-2","input":{"q":2},"expected":{"answer":"yes"}},{"id":"case-3","input":{"q":3},"expected":{"answer":"yes"}}],
  "scorers":[{"id":"exact-required","kind":"exact_match","threshold":1,"required":true}],"execution":{"concurrency":2,"scorer_max_attempts":2}}'
dupcases='{"cases":[{"id":"case-1","input":{"q":1},"expected":{"answer":"yes"}},{"id":"case-1","input":{"q":1},"expected":{"answer":"yes"}},{"id":"","input":{"q":0},"expected":{"answer":"yes"}},{"id":"case-2","input":{"q":2},"expected":{"answer":"yes"}}],
  "scorers":[{"id":"exact-required","kind":"exact_match","threshold":1,"required":true}]}'

for mode in pass fail indet; do
  config 1 "$mode" '{"case":{"id":"case-2","input":{"q":2},"expected":{"answer":"yes"}},"scorer":{"kind":"exact_match","threshold":1}}' >"configs/v1-$mode.json"
  config 2 "$mode" "$v2cases" >"configs/v2-$mode.json"
  config 3 "$mode" "$v3cases" >"configs/v3-$mode.json"
done
config 2 indet "$dupcases" >configs/v2-dup.json

for c in configs/*.json; do
  name="$(basename "$c" .json)"
  "$bin" run --config "$c" --artifact-dir "artifacts/$name" >/dev/null 2>&1 || true
done

rehash() { # dir: re-seal run.json and report.json as they are now
  local r p
  r="$(shasum -a 256 "$1/run.json" | cut -d' ' -f1)"
  p="$(shasum -a 256 "$1/report.json" | cut -d' ' -f1)"
  jq -n --arg r "$r" --arg p "$p" '{version: 1, run_sha256: $r, report_sha256: $p}' >"$1/commit.json"
}
cp -R artifacts/v2-pass artifacts/historical
jq 'del(.measurement)' artifacts/v2-pass/run.json >artifacts/historical/run.json
rehash artifacts/historical
cp -R artifacts/v2-pass artifacts/tampered
jq '.quality = "FAIL"' artifacts/v2-pass/report.json >artifacts/tampered/report.json
cp -R artifacts/v2-pass artifacts/rehashed-contradiction
jq '.quality = "FAIL"' artifacts/v2-pass/run.json >artifacts/rehashed-contradiction/run.json
jq '.quality = "FAIL"' artifacts/v2-pass/report.json >artifacts/rehashed-contradiction/report.json
rehash artifacts/rehashed-contradiction

record() { # args...: one golden entry, with paths relative to $out
  local stdout code
  set +e
  stdout="$("$bin" "$@")"
  code=$?
  set -e
  jq -n --arg out "$stdout" --argjson exit "$code" --argjson args "$(printf '%s\n' "$@" | jq -R . | jq -s .)" \
    '{args: $args, stdout: ($out + "\n"), exit: $exit}'
}
{
  for a in artifacts/*; do record inspect --artifact-dir "$a"; done
  for pair in v1-pass:v1-fail v1-pass:v1-indet v1-indet:v1-pass v2-pass:v2-pass v2-pass:v2-fail v2-pass:v2-indet \
    v2-fail:v2-pass v2-indet:v2-fail v3-pass:v3-fail v3-pass:v3-indet v1-pass:v2-pass v2-pass:v3-pass \
    historical:v2-pass v2-pass:historical v2-pass:tampered v2-pass:rehashed-contradiction v2-dup:v2-dup; do
    record compare --baseline "artifacts/${pair%%:*}" --candidate "artifacts/${pair##*:}"
  done
} | jq -s . >golden.json
rm -rf configs
echo "wrote $(jq length golden.json) golden entries for $(ls artifacts | wc -l | tr -d ' ') artifacts"
