#!/usr/bin/env bash
# Usage: expect_witness.sh <quint run command... --out-itf FILE>
# Runs a `quint run` whose invariant is the negation of a goal. Succeeds only
# if quint reports a violation (it reached the goal) and wrote the trace.
set -uo pipefail
out="${*: -1}"
log=$(mktemp)
trap 'rm -f "$log"' EXIT
"$@" >"$log" 2>&1
status=$?
if [ "$status" -ne 0 ] && grep -q "\[violation\]" "$log" && [ -s "$out" ]; then
  echo "ok: witness written to $out"
  exit 0
fi
cat "$log"
echo "FAILED: no witness for $out (exit $status)"
exit 1
