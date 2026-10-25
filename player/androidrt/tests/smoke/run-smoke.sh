#!/bin/sh
# run-smoke.sh — AndroidRT smoke test on the host.
#
# Builds a fake NativeActivity game and the runner locally with gcc (no
# cross toolchain needed), stages a trivially sized tree, then drives the
# full lifecycle headlessly:
#   onCreate → onStart, onResume → window 1280x720 → N loop frames →
#   window teardown → onPause, onStop, onDestroy → exit 0.
#
# On Windows the runner .exe loads fake_game.dll with LoadLibrary; on
# Linux host it dlopens fake_game.so (and we compile with default
# visibility via ANDROIDRT_EXPORT).
set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SRC="$HERE/../../src"
WORK="${SMOKE_WORK:-$HERE/tmp}"
STAGED="$WORK/staged"

rm -rf "$WORK"
mkdir -p "$STAGED/assets/.." "$STAGED/lib/arm64-v8a"

# ── 1. fake game shared library ───────────────────────────────────────
case "$(uname -s)" in
  *MINGW*|*MSYS*|CYGWIN*)
    CC=gcc
    GAME="$STAGED/lib/arm64-v8a/fake_game.dll"
    LOADER="$WORK/loader.exe"
    LOADER_SRC="loader.exe";;
  *)
    CC=gcc
    GAME="$STAGED/lib/arm64-v8a/libgame.so"
    LOADER="$WORK/loader";;
esac

echo "== building fake game: $GAME =="
$CC -O0 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter \
    -I"$SRC" -shared -fPIC \
    "$HERE/fake_game.c" -o "$GAME"

echo "== building runner: $LOADER =="
$CC -O0 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter \
    -I"$SRC" \
    "$SRC/runner.c" "$SRC/activity.c" "$SRC/androidshim.c" "$SRC/inputshim.c" \
    "$SRC/logshim.c" "$SRC/openslessim.c" "$SRC/surface.c" "$SRC/dlport.c" \
    -o "$LOADER" -pthread

printf 'hello androidrt\n' > "$STAGED/assets/hello.txt"

# ── 2. run the lifecycle headlessly ───────────────────────────────────
echo "== running smoke (5 frames) =="
set +e
ANDROIDPAK_APK="$STAGED/game.apk" \
ANDROIDPAK_STAGED="$STAGED" \
ANDROIDPAK_MAINLIB="lib/arm64-v8a/fake_game.dll" \
ANDROIDPAK_SHIMS="" \
ANDROIDRT_SMOKE_FRAMES=5 \
ANDROIDRT_WINDOW=1280x720 \
"$LOADER" 2>&1
STATUS=$?
set -e
echo "== runner exit status: $STATUS =="

case "$STATUS" in
  0) echo "smoke: PASS (lifecycle ran and exited cleanly)";;
  *) echo "smoke: FAIL (exit status $STATUS)"; exit 1;;
esac

# ── 3. surface stack probe (milestone-3 measurement) ─────────────────
# The standalone probe needs no GPU: a host without EGL/GBM/DRM is as valid
# a measurement as a device with one, printed layer by layer. PASS here
# means the probe itself ran to completion and reported a verdict.
case "$(uname -s)" in
  *MINGW*|*MSYS*|CYGWIN*) PROBE="$WORK/probe.exe";;
  *)                      PROBE="$WORK/probe";;
esac

echo "== building surface probe: $PROBE =="
$CC -O0 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter \
    -I"$SRC" -DSURFACE_PROBE_MAIN \
    "$SRC/surface.c" "$SRC/dlport.c" -o "$PROBE"

echo "== running surface probe (no GPU expected on the host) =="
set +e
"$PROBE" 2>&1
PROBE_STATUS=$?
set -e
echo "== probe exit status: $PROBE_STATUS =="
case "$PROBE_STATUS" in
  0) echo "surface probe: PASS (measurement completed, verdict printed above)";;
  *) echo "surface probe: FAIL (internal failure, exit status $PROBE_STATUS)"; exit 1;;
esac

# ── 4. input shim probe (AInputQueue semantics) ──────────────────────
# Same shape as the surface probe: a host with no /dev/input is still a
# valid measurement bench, because the probe drives the same android_inputInject*
# producer hooks the device's evdev reader feeds. PASS means every queue
# assertion held (inject → pollAll LOOPER_ID_INPUT → getEvent → finish,
# axis coalescing, held-slot immutability).
case "$(uname -s)" in
  *MINGW*|*MSYS*|CYGWIN*) INPUT_PROBE="$WORK/input_probe.exe";;
  *) INPUT_PROBE="$WORK/input_probe";;
esac

echo "== building input probe: $INPUT_PROBE =="
$CC -O0 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter \
    -I"$SRC" \
    "$HERE/fake_input.c" "$SRC/androidshim.c" "$SRC/inputshim.c" \
    "$SRC/surface.c" "$SRC/dlport.c" \
    -o "$INPUT_PROBE" -pthread

echo "== running input probe =="
set +e
"$INPUT_PROBE" 2>&1
INPUT_STATUS=$?
set -e
echo "== input probe exit status: $INPUT_STATUS =="
case "$INPUT_STATUS" in
  0) echo "input probe: PASS (every AInputQueue assertion held)";;
  *) echo "input probe: FAIL ($INPUT_STATUS)"; exit 1;;
esac

# ── 5. audio probe (OpenSL ES semantics) ─────────────────────────────
# Same shape as the surface/input probes: no ALSA on the host is the
# valid measurement because the shim's silent wall-clock pacer keeps
# callback order and GetPosition truthful. PASS means every_assertion
# held: engine routing, FEATURE_UNSUPPORTED for unknown IIDs, Enqueue →
# play → per-buffer callback, GetPosition advancing, stop dropping
# queued content, and Destroy from inside the callback.
case "$(uname -s)" in
  *MINGW*|*MSYS*|CYGWIN*) AUDIO_PROBE="$WORK/audio_probe.exe";;
  *)                      AUDIO_PROBE="$WORK/audio_probe";;
esac

echo "== building audio probe: $AUDIO_PROBE =="
$CC -O0 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter \
    -I"$SRC" \
    "$HERE/fake_audio.c" "$SRC/openslessim.c" "$SRC/dlport.c" \
    -o "$AUDIO_PROBE" -pthread

echo "== running audio probe (ALSA-free; silent wall-clock pacing) =="
set +e
"$AUDIO_PROBE" 2>&1
AUDIO_STATUS=$?
set -e
echo "== audio probe exit status: $AUDIO_STATUS =="
case "$AUDIO_STATUS" in
  0) echo "audio probe: PASS (every OpenSL ES assertion held)";;
  *) echo "audio probe: FAIL ($AUDIO_STATUS)"; exit 1;;
esac
