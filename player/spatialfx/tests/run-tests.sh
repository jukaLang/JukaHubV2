#!/bin/sh
# run-tests.sh — build the JukaHub Spatial plugin for this machine and run
# its host tests against the golden vectors (player/testdata/spatial).
#
#   sh player/spatialfx/tests/run-tests.sh
#
# SPATIAL_WORK=<dir> keeps the build there (default: a temporary directory,
# removed afterwards). CC picks the compiler (default gcc).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
golden=$(cd "$root/../testdata/spatial" && pwd)

if [ -n "${SPATIAL_WORK:-}" ]; then
	work=$SPATIAL_WORK
	mkdir -p "$work"
	keep=1
else
	work=$(mktemp -d)
	keep=0
fi
cleanup() {
	if [ "$keep" -eq 0 ]; then
		rm -rf "$work"
	fi
}
trap cleanup EXIT

# The library exactly as a release builds it (with its link checks).
CROSS="" OUT="$work" sh "$root/build.sh"

cc=${CC:-gcc}
# shellcheck disable=SC2086
$cc -std=c99 -O2 -Wall -Wextra -Werror -o "$work/test_spatial" \
	"$here/test_spatial.c" "$root/jukahub_spatial.c" -lm -ldl

"$work/test_spatial" "$golden" "$work/jukahub-spatial.so"
