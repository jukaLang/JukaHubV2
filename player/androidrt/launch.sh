#!/bin/sh
# launch.sh — AndroidRT's launch.sh form: run by an AndroidPak profile as
#   sh <AndroidRT>/launch.sh <apk> [args...]
# when the runtime directory has no direct `loader` entry for JukaHub to
# exec. It fills in the ANDROIDPAK_* defaults and re-execs into the runner.
#
# The runner is self-contained: its own bionic loader finds the game's
# libraries in the staged lib/<abi> directory and binds liblog/libandroid/
# OpenSL ES to itself, so no shims/ directory and no LD_LIBRARY_PATH games
# are needed (host GPU libraries resolve through the normal glibc paths, or
# ANDROIDPAK_EGL_LIB / ANDROIDPAK_GBM_LIB / ANDROIDPAK_DRM_LIB).
set -eu

APK="${1:-}"
[ -n "$APK" ] || { echo "[androidrt] launch.sh needs the APK path as its first argument" >&2; exit 64; }
shift || true

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

: "${ANDROIDPAK_STAGED:?ANDROIDPAK_STAGED is not set — launch only from an AndroidPak profile}"
: "${ANDROIDPAK_MAINLIB:?ANDROIDPAK_MAINLIB is not set}"
: "${ANDROIDPAK_APK:=$APK}"
: "${ANDROIDPAK_RT:=$HERE}"
export ANDROIDPAK_APK ANDROIDPAK_RT

RUNNER="$HERE/loader"
[ -f "$RUNNER" ] || RUNNER="$HERE/bin/loader"
[ -f "$RUNNER" ] || { echo "[androidrt] launch.sh: no loader binary next to this script" >&2; exit 70; }
# A copy that lost its permissions (unpacked by a tool that drops them, on an
# ext4 card) gets its execute bit back; FAT and exFAT cards grant it to all.
[ -x "$RUNNER" ] || chmod +x "$RUNNER" 2>/dev/null || true
[ -x "$RUNNER" ] || { echo "[androidrt] launch.sh: $RUNNER is not executable — copy the runtime again, or install it from JukaHub's APK Store" >&2; exit 70; }

exec "$RUNNER" "$APK" "$@"
