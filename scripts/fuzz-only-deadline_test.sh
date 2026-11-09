#!/usr/bin/env bash
# Checks scripts/fuzz-only-deadline.sh against fixture logs: it must accept
# only a log whose sole failure is the bare -fuzztime deadline report.
set -euo pipefail

script="$(dirname "$0")/fuzz-only-deadline.sh"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
target=FuzzExample
failed=0

check() { # check <name> <want: pass|fail>; log on stdin
	local name=$1 want=$2 got
	cat >"$dir/$name.log"
	if "$script" "$dir/$name.log" "$target"; then got=pass; else got=fail; fi
	if [[ $got != "$want" ]]; then
		echo "FAIL $name: got $got, want $want"
		failed=1
	else
		echo "ok   $name"
	fi
}

check bare-deadline pass <<'LOG'
fuzz: elapsed: 10m0s, execs: 100000 (166/sec), new interesting: 3 (total: 40)
--- FAIL: FuzzExample (600.01s)
    context deadline exceeded
FAIL
exit status 1
FAIL	github.com/cometbft/cometbft/example	600.123s
LOG

check crash fail <<'LOG'
--- FAIL: FuzzExample (12.34s)
    --- FAIL: FuzzExample (0.00s)
        testing.go:1927: panic: runtime error: index out of range
    Failing input written to testdata/fuzz/FuzzExample/0123456789abcdef
    To re-run:
    go test -run=FuzzExample/0123456789abcdef
FAIL
exit status 1
LOG

check hung-worker fail <<'LOG'
--- FAIL: FuzzExample (600.01s)
    fuzzing process hung or terminated unexpectedly: exit status 2
    context deadline exceeded
FAIL
exit status 1
LOG

check no-target fail <<'LOG'
--- FAIL: FuzzOther (600.01s)
    context deadline exceeded
FAIL
exit status 1
LOG

check repeated-failure fail <<'LOG'
--- FAIL: FuzzExample (300.00s)
    context deadline exceeded
--- FAIL: FuzzExample (300.00s)
    context deadline exceeded
FAIL
exit status 1
LOG

exit $failed
