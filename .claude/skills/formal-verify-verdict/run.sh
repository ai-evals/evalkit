#!/usr/bin/env bash
# Bounded-exhaustive check of the evalkit quality verdict.
# Usage: .claude/skills/formal-verify-verdict/run.sh [test-regex]
#   default regex runs P1..P4 (~1 minute). Example: run.sh 'P3' for the 0.1s pure check.
# Exit 0: P1-P3 pass and P4 shows only the findings in known-findings.txt.
# Exit 1: a new violation (real bug or modeling error, see SKILL.md). Exit 2: did not run.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../.." && pwd)"
regex="^TestFormal(${1:-P1|P2|P3|P4})"
log="$(mktemp -t evalkit-formal.XXXXXX)"
trap 'rm -f "$log"' EXIT

cd "$root"
go test -tags formalverify -count=1 -v -run "$regex" ./internal/engine/ >"$log" 2>&1
status=$?

grep -E '^(=== RUN|--- (PASS|FAIL)|ok|FAIL)|checked [0-9]+' "$log" | grep -v '^=== RUN' || true

# Violations are emitted as "<file>:<line>: <message>" by t.Error.
violations="$(sed -nE 's/^ +formal_verdict_test\.go:[0-9]+: //p' "$log" | grep -v '^checked [0-9]' || true)"
if [ "$status" -ne 0 ] && [ -z "$violations" ]; then
  echo "formal check did not run cleanly (build error or panic):" >&2
  cat "$log" >&2
  exit 2
fi
new="$(printf '%s\n' "$violations" | grep -v '^$' | grep -vxF -f "$here/known-findings.txt" || true)"
known="$(printf '%s\n' "$violations" | grep -v '^$' | grep -cxF -f "$here/known-findings.txt" || true)"

echo
echo "known findings still present: ${known:-0} (see known-findings.txt)"
if [ -n "$new" ]; then
  echo "NEW VIOLATIONS:"
  printf '%s\n' "$new"
  exit 1
fi
echo "RESULT: no new violations"
exit 0
