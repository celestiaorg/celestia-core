#!/usr/bin/env bash
# Runs native Go fuzz targets in the packages a pull request changes, for a
# short time each. The nightly Fuzz Tests job fuzzes every target for longer;
# this gives pull requests a quick check without starting one job per target.
#
# Usage: scripts/fuzz-changed.sh <base-ref>
#   FUZZTIME   time to fuzz each target (default 15s)
#   DRY_RUN=1  list the targets that would run without fuzzing them
#
# Every target runs when the shared fuzz plumbing changes: internal/fuzzinput,
# this script, or the fuzz workflow. Otherwise only targets in packages with a
# changed Go file or saved corpus entry run. Seed corpora already run in the
# regular test jobs, which call `go test` on every package.
set -euo pipefail

base=${1:?usage: $0 <base-ref>}
fuzztime=${FUZZTIME:-15s}

changed=$(git diff --name-only "$base"...HEAD)

# Directories of Go packages that define a native fuzz target.
fuzz_dirs() {
	grep -rl --include='*_test.go' -E '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . |
		xargs -r -n1 dirname | sed 's|^\./||' | sort -u
}

if grep -qE '^(internal/fuzzinput/|scripts/fuzz-changed\.sh$|\.github/workflows/fuzz-nightly\.yml$)' <<<"$changed"; then
	dirs=$(fuzz_dirs)
else
	# A corpus entry lives in <pkg>/testdata/fuzz/<Target>/; map it to <pkg>.
	touched=$(
		{
			{ grep -E '\.go$' <<<"$changed" || true; } | xargs -r -n1 dirname
			{ grep -E '/testdata/fuzz/' <<<"$changed" || true; } | sed 's|/testdata/fuzz/.*||'
		} | sort -u
	)
	dirs=$(comm -12 <(fuzz_dirs) <(printf '%s\n' "$touched" | sed '/^$/d' | sort -u))
fi

if [[ -z $dirs ]]; then
	echo "No changed package defines a fuzz target."
	exit 0
fi

failed=()
for dir in $dirs; do
	targets=$(grep -ohE '^func (Fuzz[A-Za-z0-9_]*)\(f \*testing\.F\)' "$dir"/*_test.go |
		sed -E 's/^func (Fuzz[A-Za-z0-9_]*).*/\1/' | sort -u)
	for target in $targets; do
		if [[ ${DRY_RUN:-} == 1 ]]; then
			echo "$dir $target"
			continue
		fi
		echo "::group::$dir $target"
		if ! go test -run='^$' -fuzz="^${target}\$" -fuzztime="$fuzztime" -parallel=2 "./$dir"; then
			failed+=("$dir $target")
		fi
		echo "::endgroup::"
	done
done

if ((${#failed[@]})); then
	printf 'Fuzz target failed: %s\n' "${failed[@]}"
	exit 1
fi
