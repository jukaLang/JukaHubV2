#!/bin/sh
# build-tsp.sh — cross-builds AndroidRT for the TrimUI Smart Pro (A133P,
# aarch64) into the runtime layout the AndroidPak profile runtime resolves:
#
#   AndroidRT/
#     loader                  ← the runner executable
#     launch.sh               ← the launcher form
#     shims/liblog.so         ← logshim.c
#     shims/libandroid.so     ← androidshim.c + inputshim.c
#     shims/libOpenSLES.so    ← openslessim.c
#
# Set CROSS to an aarch64-linux-gnu- prefixed toolchain (SDK or distro
# cross-gcc); unset CROSS tries plain aarch64-linux-gnu-gcc on PATH.
set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OUT="${ANDROIDRT_OUT:-$HERE/AndroidRT}"
SRC="$HERE/src"

if [ -n "${CROSS:-}" ]; then
    CC="${CROSS}gcc"
elif command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
    CC="aarch64-linux-gnu-gcc"
elif [ -n "${SDK_ROOT:-}" ] && [ -x "$SDK_ROOT/host/bin/aarch64-none-linux-gnu-gcc" ]; then
    CC="$SDK_ROOT/host/bin/aarch64-none-linux-gnu-gcc"
else
    echo "build-tsp.sh: no aarch64 cross compiler found." >&2
    echo "  set CROSS=<prefix> (with <prefix>gcc on PATH) or SDK_ROOT like the TG5050 SDK" >&2
    exit 1
fi

CFLAGS="-O2 -g0 -fPIC -std=gnu11 -Wall -Wextra -Wno-unused-parameter -fvisibility=hidden"
LDFLAGS="-shared -Wl,-z,relro,-z,now"

# A shim preloaded RTLD_GLOBAL only satisfies a game's DT_NEEDED when its
# SONAME matches the bionic library name the APK references (liblog.so, ...).
# Without it, glibc searches LD_LIBRARY_PATH for a file with that name and
# may resolve DT_NEEDED to a different bionic copy than the preloaded shim,
# so every shim is linked with -Wl,-soname,<its exact name>.
SHIM_SONAME() { printf %s "-Wl,-soname,$1"; }

rm -rf "$OUT"
mkdir -p "$OUT/shims"

echo "== AndroidRT → $OUT (CC=$CC) =="

# The runner (loader). Exported API must surface through the -rdynamic-free
# default visibility: main()/runOneActivity live in different objects, so
# keep -fvisibility=hidden off the runner link.
$CC -O2 -g0 -std=gnu11 -Wall -Wextra -Wno-unused-parameter \
    -DJUKAHUB_EVDEV \
    -I"$SRC" \
    "$SRC/runner.c" "$SRC/activity.c" "$SRC/androidshim.c" "$SRC/inputshim.c" \
    "$SRC/evdev.c" "$SRC/logshim.c" \
    "$SRC/openslessim.c" "$SRC/surface.c" "$SRC/dlport.c" \
    -o "$OUT/loader" \
    -pthread -ldl
echo "  loader"

# One shared object per shimmed Android library, with the
# exact DT_NEEDED names games reference.
build_shim() {
    name="$1"; src="$2"; extra="${3:-}"
    $CC $CFLAGS -I"$SRC" -c "$SRC/$src" -o "$OUT/shims/$name.o"
    $CC $LDFLAGS "$(SHIM_SONAME "$name")" "$OUT/shims/$name.o" -o "$OUT/shims/$name" $extra
    rm -f "$OUT/shims/$name.o"
    echo "  shims/$name"
}

# androidshim is compiled twice: once linked into the runner (all symbols,
# default visibility) and once as shims/libandroid.so (hidden exports, only
# the Android ABI surface marked default via actbridge's declaration).
# libandroid.so carries inputshim.c too: the AInputEvent/AInputQueue
# functions are part of libandroid's ABI, so a game's DT_NEEDED resolves
# them out of the same file it resolves the rest of the shim from.
$CC $CFLAGS -DJUKAHUB_SHIM_SO -DJUKAHUB_SHIM_ANDROID -I"$SRC" \
    -c "$SRC/androidshim.c" -o "$OUT/shims/.androidshim_so.o"
$CC $LDFLAGS -I"$SRC" -c "$SRC/inputshim.c" -o "$OUT/shims/.inputshim_so.o"
$CC $LDFLAGS "$(SHIM_SONAME libandroid.so)" -pthread "$OUT/shims/.androidshim_so.o" "$OUT/shims/.inputshim_so.o" \
    -o "$OUT/shims/libandroid.so" -ldl
rm -f "$OUT/shims/.androidshim_so.o" "$OUT/shims/.inputshim_so.o"
echo "  shims/libandroid.so"

build_shim liblog.so  logshim.c    ""
build_shim libOpenSLES.so openslessim.c "-ldl"

chmod +x "$OUT/launch.sh" "$OUT/loader"
chmod +x "$OUT/shims/"*.so 2>/dev/null || true

echo "done."
