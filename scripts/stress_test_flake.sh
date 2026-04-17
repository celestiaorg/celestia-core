#!/usr/bin/env bash
# Usage: scripts/stress_test_flake.sh [COUNT] [TEST_NAME]
# Runs a single Go test N times and prints pass/fail counts.
# Exits 0 if all runs pass, 1 otherwise.

set -u

COUNT="${1:-50}"
TEST="${2:-TestByzantinePrevoteEquivocation}"
PKG="./consensus/..."
LOG_DIR="$(mktemp -d)" || { echo "Failed to create temp dir" >&2; exit 1; }

echo "Running $TEST x$COUNT, logs in $LOG_DIR"

pass=0
fail=0
for i in $(seq 1 "$COUNT"); do
  log="$LOG_DIR/run-$i.log"
  if go test -tags deadlock -run "^${TEST}$" -count=1 -timeout=300s "$PKG" >"$log" 2>&1; then
    pass=$((pass+1))
    printf "."
  else
    fail=$((fail+1))
    printf "F"
  fi
done
echo
echo "Passed: $pass / $COUNT"
echo "Failed: $fail / $COUNT"
if [ "$fail" -gt 0 ]; then
  first_fail=""
  for f in "$LOG_DIR"/run-*.log; do
    if grep -q -E 'FAIL|panic' "$f"; then
      first_fail="$f"
      break
    fi
  done
  if [ -n "$first_fail" ]; then
    echo "First failing log: $first_fail"
  else
    echo "Failing logs in: $LOG_DIR (no FAIL/panic pattern matched — check manually)"
  fi
  exit 1
fi
