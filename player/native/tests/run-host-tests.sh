#!/bin/sh
# Host tests for chiaki-stream. They need only a C compiler (no SDL, FFmpeg or
# chiaki): the pure parts are tested directly, and the whole helper's main()
# runs against scripted fakes (stubs/ holds minimal headers, fake_runtime.c the
# fake SDL / chiaki / FFmpeg with a fake clock).
#
#   sh player/native/tests/run-host-tests.sh            # tests
#   sh player/native/tests/run-host-tests.sh preview    # also render the OSD to <work>/preview/*.ppm
#
# CC, OUT and SAN (sanitizer flags, "" to turn them off) can be set. Everything
# is built in "$OUT/jukahub-native-tests" (OUT defaults to $TMPDIR or /tmp):
# that subdirectory is the only thing this script ever deletes.
set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
NATIVE="$(dirname "$HERE")"
CC="${CC:-cc}"
OUT="${OUT:-${TMPDIR:-/tmp}}/jukahub-native-tests"
SAN="${SAN--fsanitize=address,undefined -fno-omit-frame-pointer}"
FLAGS="-std=gnu11 -g -O0 -Wall -Wextra -Wpedantic -Werror $SAN"

rm -rf "$OUT"
mkdir -p "$OUT"

# The parts without SDL compile once.
$CC $FLAGS -I"$NATIVE" -c "$NATIVE/stream_input.c" -o "$OUT/stream_input.o"
$CC $FLAGS -I"$NATIVE" -c "$NATIVE/stream_sys.c" -o "$OUT/stream_sys.o"

echo "== stream_input"
$CC $FLAGS -I"$NATIVE" "$HERE/test_stream_input.c" "$OUT/stream_input.o" -o "$OUT/test_stream_input"
"$OUT/test_stream_input"

echo "== stream_sys"
$CC $FLAGS -I"$NATIVE" "$HERE/test_stream_sys.c" "$OUT/stream_sys.o" -lm -o "$OUT/test_stream_sys"
"$OUT/test_stream_sys" "$OUT"

# The whole helper, against both generations of the stub SDL headers: 2.0.14+
# (touchpad events compiled in) and older (compiled out).
for patch in 14 10; do
    echo "== chiaki-stream end to end (SDL 2.0.$patch headers)"
    STUB="-DSTUB_SDL_PATCH=$patch -I$HERE/stubs -I$NATIVE"
    $CC $FLAGS $STUB -Dmain=stream_main -c "$NATIVE/chiaki-stream.c" -o "$OUT/chiaki-stream.o"
    $CC $FLAGS $STUB -c "$NATIVE/stream_osd.c" -o "$OUT/stream_osd.o"
    $CC $FLAGS $STUB "$HERE/test_stream_e2e.c" "$HERE/fake_runtime.c" "$HERE/fake_draw.c" \
        "$OUT/chiaki-stream.o" "$OUT/stream_osd.o" "$OUT/stream_input.o" "$OUT/stream_sys.o" \
        -lm -ldl -o "$OUT/test_stream_e2e"
    "$OUT/test_stream_e2e" "$OUT" > "$OUT/e2e-$patch.log" 2>&1 || { cat "$OUT/e2e-$patch.log"; exit 1; }
    tail -n 1 "$OUT/e2e-$patch.log"
done

if [ "${1:-}" = "preview" ]; then
    echo "== OSD preview"
    mkdir -p "$OUT/preview"
    $CC -std=gnu11 -O1 -I"$HERE/stubs" -I"$NATIVE" "$HERE/preview/osd_preview.c" "$HERE/preview/fake_sdl.c" \
        "$NATIVE/stream_osd.c" -lm -o "$OUT/osd_preview"
    "$OUT/osd_preview" "$OUT/preview"
    ls "$OUT/preview"
fi
echo "host tests passed"
