#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p runs
export GOCACHE="$PWD/runs/go-build"
export GOPATH="$PWD/runs/gopath"
export GOMODCACHE="$GOPATH/pkg/mod"

# The measured operation is ~100ms, but building the fixture (a 32 MB block of
# 64 blob transactions) and the unmeasured warm-up iteration dominate the wall
# time: ~1.2s on an idle fast machine. The default is deliberately generous so
# that a busy runner still produces a result instead of being killed; lower it
# only when you want the run to fail fast.
bench_timeout=${PROPOSAL_CORE_BENCH_TIMEOUT:-60s}

# The benchmark needs a fixed number of CPUs for runs to be comparable, but the
# CPUs a worker allows are not necessarily numbered 0-N. Take them from this
# process's own affinity list unless an explicit list is given.
cpu_count=${PROPOSAL_CORE_BENCH_CPU_COUNT:-8}
cpus=${PROPOSAL_CORE_BENCH_CPUS:-}
if [ -z "$cpus" ] && command -v taskset >/dev/null 2>&1; then
  allowed=$(taskset -cp $$ | sed 's/.*: *//')
  expanded=()
  IFS=',' read -ra ranges <<<"$allowed"
  for range in "${ranges[@]}"; do
    case "$range" in
    *-*) for ((cpu = ${range%%-*}; cpu <= ${range##*-}; cpu++)); do expanded+=("$cpu"); done ;;
    *) expanded+=("$range") ;;
    esac
  done
  if [ "${#expanded[@]}" -lt "$cpu_count" ]; then
    printf 'warning: only %d CPUs are allowed here, %d were requested; these numbers are not comparable with %d-CPU runs\n' \
      "${#expanded[@]}" "$cpu_count" "$cpu_count" >&2
    cpu_count=${#expanded[@]}
  fi
  cpus=$(
    IFS=','
    printf '%s' "${expanded[*]:0:cpu_count}"
  )
fi

CGO_ENABLED=0 go test -c -o runs/proposal-core.test ./consensus/propagation

cmd=(runs/proposal-core.test
  -test.run '^$'
  -test.bench '^BenchmarkProposalCore32MB$'
  -test.benchtime=1x
  -test.count=1)
if [ -n "$cpus" ]; then
  cmd=(taskset -c "$cpus" "${cmd[@]}")
else
  printf 'warning: running without CPU pinning; set PROPOSAL_CORE_BENCH_CPUS to pin\n' >&2
fi

start_ns=$(date +%s%N)
set +e
output=$(GOMAXPROCS="$cpu_count" timeout "$bench_timeout" "${cmd[@]}")
status=$?
set -e
end_ns=$(date +%s%N)
printf '%s\n' "$output"
if [ "$status" -ne 0 ]; then
  if [ "$status" -eq 124 ]; then
    printf 'benchmark timed out after %s; raise PROPOSAL_CORE_BENCH_TIMEOUT\n' "$bench_timeout" >&2
  else
    printf 'benchmark exited with status %d\n' "$status" >&2
  fi
  exit "$status"
fi
printf 'BENCH_WALL_MS: %d\n' "$(((end_ns - start_ns) / 1000000))"
awk '$1 ~ /^BenchmarkProposalCore32MB/ {for (i=2; i<=NF; i++) if ($i == "proposal_ms/op") {printf "PROPOSAL_CORE_MS: %.6f\n", $(i-1); found=1}} END {if (!found) exit 1}' <<<"$output"
