#!/bin/sh
# build-tsp.sh — cross-builds AndroidRT for the TrimUI Smart Pro (A133P,
# aarch64) into the runtime layout the AndroidPak profile runtime resolves:
#
#   AndroidRT/
#     loader                  the runner: bionic loader + compat layer + the
#                             liblog/libandroid/OpenSL ES/EGL-present shims,
#                             all in one executable (src/*.c)
#     launch.sh               the launcher form
#     surface-probe           the milestone-3 GPU stack measurement
#     props.txt.example       ro.* property overrides (copy to props.txt)
#
# Set CROSS to an aarch64-linux-gnu- prefixed toolchain (SDK or distro
# cross-gcc); unset CROSS tries plain aarch64-linux-gnu-gcc on PATH.
set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OUT="${ANDROIDRT_OUT:-$HERE/AndroidRT}"
SRC="$HERE/src"

if [ -n "${CROSS:-}" ]; then
    CC="${CROSS}gcc"
    READELF="${CROSS}readelf"
elif command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
    CC="aarch64-linux-gnu-gcc"
    READELF="aarch64-linux-gnu-readelf"
elif [ -n "${SDK_ROOT:-}" ] && [ -x "$SDK_ROOT/host/bin/aarch64-none-linux-gnu-gcc" ]; then
    CC="$SDK_ROOT/host/bin/aarch64-none-linux-gnu-gcc"
    READELF="$SDK_ROOT/host/bin/aarch64-none-linux-gnu-readelf"
else
    echo "build-tsp.sh: no aarch64 cross compiler found." >&2
    echo "  set CROSS=<prefix> (with <prefix>gcc on PATH) or SDK_ROOT like the TG5050 SDK" >&2
    exit 1
fi

# -fvisibility=hidden + --export-dynamic: the runner exports exactly the
# ANDROIDRT_EXPORT functions (the game-facing Android ABI), which is what the
# bionic loader binds a game's liblog/libandroid/libOpenSLES imports to.
CFLAGS="-O2 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter -fvisibility=hidden"

rm -rf "$OUT"
mkdir -p "$OUT"

echo "== AndroidRT → $OUT (CC=$CC) =="

$CC $CFLAGS -DJUKAHUB_EVDEV -I"$SRC" \
    "$SRC/runner.c" "$SRC/activity.c" "$SRC/androidshim.c" "$SRC/inputshim.c" "$SRC/looper.c" \
    "$SRC/jnienv.c" "$SRC/jnibase.c" "$SRC/jnipack_minecraft.c" "$SRC/gameactivity.c" "$SRC/apkzip.c" \
    "$SRC/apkmeta.c" "$SRC/device.c" "$SRC/evdev.c" "$SRC/logshim.c" "$SRC/openslessim.c" "$SRC/surface.c" \
    "$SRC/eglpresent.c" \
    "$SRC/dlport.c" "$SRC/elfdyn.c" "$SRC/bionicld.c" "$SRC/bioniccompat.c" "$SRC/bionicpthread.c" \
    "$SRC/bionicjmp.S" "$SRC/sysprops.c" \
    -o "$OUT/loader" -pthread -ldl -lm -Wl,--export-dynamic
echo "  loader"

$CC $CFLAGS -DSURFACE_PROBE_MAIN -I"$SRC" "$SRC/surface.c" "$SRC/dlport.c" -o "$OUT/surface-probe" -ldl
echo "  surface-probe"

cp "$HERE/launch.sh" "$OUT/launch.sh"
cat > "$OUT/props.txt.example" <<'EOF'
# Copy to props.txt (next to `loader`) to override what games read through
# __system_property_get. One key=value per line; defaults describe the Smart
# Pro as an Android 9 (SDK 28) arm64 handheld with GLES 3.2.
#ro.product.model=Smart Pro
#ro.build.version.sdk=28
#persist.sys.locale=en-US
EOF
chmod +x "$OUT/loader" "$OUT/launch.sh" "$OUT/surface-probe"

# NDK clang reads the stack-protector canary at tpidr_el0 + 0x28; the runner
# pins that slot with apak_tls, which must be the first (only) object of its
# static TLS block. Check it while the symbol table is still there.
if command -v "$READELF" >/dev/null 2>&1; then
    off=$("$READELF" -sW "$OUT/loader" | awk '$NF == "apak_tls" { print $2; exit }')
    case "$off" in
        0000000000000000) echo "  TLS: apak_tls at offset 0 of the TLS block (canary slot = tpidr_el0+0x28)" ;;
        "") echo "  TLS: apak_tls not found in the symbol table — cannot verify the canary slot" >&2 ;;
        *) echo "build-tsp.sh: apak_tls is at TLS offset 0x$off, not 0 — another __thread variable was linked first" >&2
           exit 1 ;;
    esac
fi

echo "done."
