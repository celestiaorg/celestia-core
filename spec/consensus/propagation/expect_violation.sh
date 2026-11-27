#!/usr/bin/env bash
# Usage: expect_violation.sh <label> <quint verify command...>
# Runs the command, which must check exactly one property, and succeeds only
# if the model checker reports a violation ("[violation]" and a non-zero
# exit). Any other outcome (property holds, tool error) fails. TLC reports
# the combined property as q_inv, so the label is only for the log.
set -uo pipefail
inv="$1"
shift
log=$(mktemp)
trap 'rm -f "$log"' EXIT
start=$(date +%s)
"$@" >"$log" 2>&1
status=$?
end=$(date +%s)
if [ "$status" -ne 0 ] && grep -q "\[violation\]" "$log"; then
  echo "ok: $inv violated as expected ($((end - start))s): $*"
  exit 0
fi
cat "$log"
echo "FAILED: expected a violation of $inv (exit $status): $*"
exit 1
