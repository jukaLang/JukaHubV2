#!/bin/sh
# launch.sh — AndroidRT's launch.sh form: run by an AndroidPak profile as
#   sh <AndroidRT>/launch.sh <apk> [args...]
# when the device build produced no direct `loader` binary. It arranges the
# library search paths a glibc-host runner needs and re-execs into the
# runner, keeping ANDROIDPAK_* explicit in the child environment.
set -eu

APK="${1:-}"
[ -n "$APK" ] || { echo "[androidrt] launch.sh needs the APK path as its first argument" >&2; exit 64; }
shift || true

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

: "${ANDROIDPAK_STAGED:?ANDROIDPAK_STAGED is not set — launch only from an AndroidPak profile}"
: "${ANDROIDPAK_MAINLIB:?ANDROIDPAK_MAINLIB is not set}"
: "${ANDROIDPAK_APK:=$APK}"
export ANDROIDPAK_APK

RUNNER="$HERE/loader"
[ -x "$RUNNER" ] || RUNNER="$HERE/bin/loader"
[ -x "$RUNNER" ] || { echo "[androidrt] launch.sh: no usable loader binary next to this script" >&2; exit 70; }

SHIMS="$HERE/shims"
[ -d "$SHIMS" ] || { echo "[androidrt] launch.sh: no shims/ directory — the game's bionic needs will not resolve" >&2; }
export ANDROIDPAK_SHIMS="$SHIMS"

# The staged lib dir first (game's own dependencies), then the shims, then
# the device's own GL userspace last so the PowerVR EGL always wins over a
# misplaced copy inside a game APK.
CHOSEN_ABIS="${ANDROIDPAK_ABIS:-arm64-v8a}"
ABI_PREFIX="${CHOSEN_ABIS%%,*}"
export LD_LIBRARY_PATH="$ANDROIDPAK_STAGED/lib/$ABI_PREFIX:$SHIMS${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

exec "$RUNNER" "$APK" "$@"
