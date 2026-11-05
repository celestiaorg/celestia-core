#!/usr/bin/env bash
# Usage: scripts/check-fuzz-targets.sh
#
# Checks that every native Go fuzz target (any `func FuzzX(` in a
# _test.go file) is either in the nightly matrix of
# .github/workflows/fuzz-nightly.yml or listed with a reason in
# test/fuzz/nightly-excluded.txt, and that neither list names a target that no
# longer exists. Run from the repository root; it only reads files.
#
#   FUZZ_WORKFLOW  matrix file (default .github/workflows/fuzz-nightly.yml)
#   FUZZ_EXCLUDED  exclusion list (default test/fuzz/nightly-excluded.txt)
set -euo pipefail

workflow=${FUZZ_WORKFLOW:-.github/workflows/fuzz-nightly.yml}
excluded=${FUZZ_EXCLUDED:-test/fuzz/nightly-excluded.txt}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# "<package dir> <Target>" for every fuzz target in the tree. Match any
# top-level func named Fuzz* in a _test.go file, whatever its parameter list
# looks like (multiline, aliased testing import), so a target can't slip past.
grep -rE --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(' . |
	sed -E 's|^\./(.*)/[^/]+_test\.go:func Fuzz([A-Za-z0-9_]*)\(.*|\1 \2|' |
	sort -u >"$tmp/defined"

# Matrix entries look like `- {package: <dir>, target: <Target>, priority: N}`.
grep -oE '\{package: *[^,}]+, *target: *[^,}]+' "$workflow" |
	sed -E 's/\{package: *([^,}]+), *target: *([^,} ]+).*/\1 \2/' |
	sort >"$tmp/matrix"

status=0

indent() { sed "s/^/  /"; }

# Exclusions must give a reason after the package and target.
grep -vE '^[[:space:]]*(#|$)' "$excluded" >"$tmp/excluded-lines" || true
while read -r pkg target reason; do
	if [[ -z ${reason:-} ]]; then
		echo "error: $excluded: $pkg $target has no reason"
		status=1
	fi
done <"$tmp/excluded-lines"
awk '{print $1, $2}' "$tmp/excluded-lines" | sort >"$tmp/excluded"

for list in matrix excluded; do
	dups=$(uniq -d "$tmp/$list")
	if [[ -n $dups ]]; then
		echo "error: listed more than once in the $list:"
		indent <<<"$dups"
		status=1
	fi
done

both=$(comm -12 <(sort -u "$tmp/matrix") <(sort -u "$tmp/excluded"))
if [[ -n $both ]]; then
	echo "error: in both the nightly matrix and $excluded:"
	indent <<<"$both"
	status=1
fi

sort -u "$tmp/matrix" "$tmp/excluded" >"$tmp/listed"

missing=$(comm -23 "$tmp/defined" "$tmp/listed")
if [[ -n $missing ]]; then
	echo "error: fuzz targets in neither the nightly matrix ($workflow) nor $excluded:"
	indent <<<"$missing"
	echo "Add each to the matrix with a priority, or exclude it with a reason."
	status=1
fi

stale=$(comm -13 "$tmp/defined" "$tmp/listed")
if [[ -n $stale ]]; then
	echo "error: listed targets that no _test.go file defines:"
	indent <<<"$stale"
	status=1
fi

if [[ $status == 0 ]]; then
	echo "All $(wc -l <"$tmp/defined") fuzz targets are in the nightly matrix ($(wc -l <"$tmp/matrix")) or excluded ($(wc -l <"$tmp/excluded"))."
fi
exit "$status"
