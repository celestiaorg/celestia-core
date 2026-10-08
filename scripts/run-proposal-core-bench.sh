#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p runs
export GOCACHE="$PWD/runs/go-build"
export GOPATH="$PWD/runs/gopath"
export GOMODCACHE="$GOPATH/pkg/mod"
CGO_ENABLED=0 go test -c -o runs/proposal-core.test ./consensus/propagation
start_ns=$(date +%s%N)
output=$(GOMAXPROCS=8 timeout 0.88s taskset -c 0-7 runs/proposal-core.test \
  -test.run '^$' \
  -test.bench '^BenchmarkProposalCore32MB$' \
  -test.benchtime=1x \
  -test.count=1)
end_ns=$(date +%s%N)
printf '%s\n' "$output"
printf 'BENCH_WALL_MS: %d\n' "$(((end_ns - start_ns) / 1000000))"
awk '$1 ~ /^BenchmarkProposalCore32MB/ {for (i=2; i<=NF; i++) if ($i == "proposal_ms/op") {printf "PROPOSAL_CORE_MS: %.6f\n", $(i-1); found=1}} END {if (!found) exit 1}' <<< "$output"
