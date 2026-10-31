#!/usr/bin/env bash
# Verification harness for the evalkit CLI. See ../SKILL.md.
# Layout per verification run (RUN_ID):
#   $VERIFY_ROOT/$RUN_ID/scratch   binaries, rendered configs, artifact dirs (removed by cleanup)
#   $VERIFY_ROOT/$RUN_ID/evidence  proof artifacts (survive cleanup)
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
VERIFY_ROOT="${EVALKIT_VERIFY_ROOT:-${TMPDIR:-/tmp}}"
VERIFY_ROOT="${VERIFY_ROOT%/}/evalkit-verify"
PLACEHOLDER="/ABSOLUTE/PATH/TO/evalkit-example-target"

die() { echo "verify-evalkit: $*" >&2; exit 2; }
dirs() {
  [[ -n "${1:-}" ]] || die "missing RUN_ID"
  RUN_ID="$1"; BASE="$VERIFY_ROOT/$RUN_ID"; SCRATCH="$BASE/scratch"; EVIDENCE="$BASE/evidence"
}
need_launched() { [[ -f "$SCRATCH/.owner" ]] || die "run $RUN_ID was not launched by this harness ($SCRATCH/.owner missing)"; }

cmd_launch() {
  local id="${1:-$(date +%Y%m%d-%H%M%S)-$$}"
  dirs "$id"
  [[ -e "$BASE" ]] && die "$BASE already exists; pick a new RUN_ID"
  mkdir -p "$SCRATCH" "$EVIDENCE"
  echo "$$ $(date -u +%FT%TZ)" > "$SCRATCH/.owner"
  (cd "$REPO" && go build -o "$SCRATCH/evalkit" ./cmd/evalkit && go build -o "$SCRATCH/evalkit-example-target" ./examples/target)
  { echo "repo=$REPO"; echo "commit=$(git -C "$REPO" rev-parse HEAD)";
    echo "dirty=$(git -C "$REPO" status --porcelain -- cmd internal sdk examples go.mod | wc -l | tr -d ' ')";
    echo "go=$(go version)"; echo "built=$(date -u +%FT%TZ)"; } > "$EVIDENCE/build.txt"
  echo "READY run_id=$RUN_ID scratch=$SCRATCH evidence=$EVIDENCE"
}

cmd_doctor() {
  dirs "${1:-}"; local ok=1
  check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; ok=0; fi; }
  check "scratch owned by harness" '[[ -f "$SCRATCH/.owner" ]]'
  check "evalkit binary executable" '[[ -x "$SCRATCH/evalkit" ]]'
  check "example target executable" '[[ -x "$SCRATCH/evalkit-example-target" ]]'
  check "binary answers version" '"$SCRATCH/evalkit" version | grep -q "^evalkit "'
  check "binary built from current HEAD" '[[ "$(sed -n "s/^commit=//p" "$EVIDENCE/build.txt")" == "$(git -C "$REPO" rev-parse HEAD)" ]]'
  check "no source edits since build" '[[ -z "$(find "$REPO/cmd" "$REPO/internal" "$REPO/sdk" "$REPO/examples" "$REPO/go.mod" \( -name "*.go" -o -name go.mod \) -newer "$EVIDENCE/build.txt" -print -quit 2>/dev/null)" ]]'
  check "jq available" 'command -v jq >/dev/null'
  [[ $ok == 1 ]] && echo "HEALTHY run_id=$RUN_ID" || { echo "UNHEALTHY: rebuild with a new launch"; return 1; }
}

# config RUN_ID SOURCE(v1|v2|v3) VARIANT(pass|fail|indeterminate) NAME
cmd_config() {
  dirs "${1:-}"; need_launched
  local src="$2" variant="$3" name="$4" in out
  case "$src" in v1) in="$REPO/examples/evalkit.json";; v2|v3) in="$REPO/examples/evalkit-$src.json";; *) die "source must be v1|v2|v3";; esac
  out="$SCRATCH/$name.json"
  [[ -e "$out" ]] && die "$out exists"
  case "$variant" in
    pass) sed "s#$PLACEHOLDER#$SCRATCH/evalkit-example-target#" "$in" > "$out";;
    # last case's expected answer becomes "no"; the example target always answers "yes"
    fail) sed "s#$PLACEHOLDER#$SCRATCH/evalkit-example-target#" "$in" | awk '{l[NR]=$0} /"expected"/{n=NR} END{for(i=1;i<=NR;i++){if(i==n){sub(/"yes"/,"\"no\"",l[i])} print l[i]}}' > "$out";;
    # /bin/echo ignores stdin and prints non-JSON: a malformed target result
    indeterminate) sed -e "s#\"$PLACEHOLDER\"#\"/bin/echo\"#" -e 's#"args": \[\]#"args": ["not-json"]#' "$in" > "$out";;
    *) die "variant must be pass|fail|indeterminate";;
  esac
  cp "$out" "$EVIDENCE/config-$name.json"
  echo "$out"
}

# run RUN_ID LABEL -- <evalkit args>   (cwd = scratch, so relative paths land in scratch)
cmd_run() {
  dirs "${1:-}"; need_launched
  local label="$2"; shift 2; [[ "${1:-}" == "--" ]] && shift
  local ev="$EVIDENCE/$label"; [[ -e "$ev" ]] && die "evidence label $label already used"
  mkdir -p "$ev"
  printf '%q ' evalkit "$@" > "$ev/command.txt"; echo >> "$ev/command.txt"
  local code=0
  (cd "$SCRATCH" && "$SCRATCH/evalkit" "$@") > "$ev/stdout.txt" 2> "$ev/stderr.txt" || code=$?
  echo "$code" > "$ev/exit_code"
  local prev="" a
  for a in "$@"; do
    if [[ "$prev" == "--artifact-dir" ]]; then
      local p="$a"; [[ "$p" = /* ]] || p="$SCRATCH/$p"
      if [[ -d "$p" ]]; then ls -la "$p" > "$ev/artifact-listing.txt"; cp -R "$p" "$ev/artifact-snapshot"; else echo "absent: $p" > "$ev/artifact-listing.txt"; fi
    fi
    prev="$a"
  done
  echo "label=$label exit=$code evidence=$ev"
  cat "$ev/stdout.txt"; [[ -s "$ev/stderr.txt" ]] && { echo "--- stderr"; cat "$ev/stderr.txt"; }
  return 0
}

cmd_cleanup() {
  dirs "${1:-}"
  [[ -d "$SCRATCH" ]] || { echo "nothing to clean for $RUN_ID"; return 0; }
  need_launched
  rm -rf "$SCRATCH"
  echo "removed $SCRATCH; evidence kept at $EVIDENCE"
}

case "${1:-}" in
  launch) shift; cmd_launch "$@";;
  doctor) shift; cmd_doctor "$@";;
  config) shift; [[ $# == 4 ]] || die "usage: config RUN_ID v1|v2|v3 pass|fail|indeterminate NAME"; cmd_config "$@";;
  run) shift; [[ $# -ge 2 ]] || die "usage: run RUN_ID LABEL -- <evalkit args>"; cmd_run "$@";;
  cleanup) shift; cmd_cleanup "$@";;
  path) shift; dirs "${1:-}"; echo "scratch=$SCRATCH evidence=$EVIDENCE";;
  *) echo "usage: verify-evalkit.sh launch [RUN_ID] | doctor RUN_ID | config RUN_ID SRC VARIANT NAME | run RUN_ID LABEL -- ARGS | cleanup RUN_ID | path RUN_ID" >&2; exit 2;;
esac
