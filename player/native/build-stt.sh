#!/bin/sh
# Build the TrimUI offline speech recognizer inside build-root.sh's Ubuntu
# 20.04 arm64 root. Both upstream files are pinned and verified before they
# reach native/bin/juka-whisper, which the packaging jobs copy unchanged.
set -eu

WHISPER_COMMIT=d1be6fde11ac6e0407606b4e42fe72d34add8037
SOURCE_SHA=645445da884df6dbd52b30f82d7be7c01d1767c938714e477e786b88d529e9f4
MODEL_REV=5359861c739e955e79d9a303bcbc70fb988958b1
MODEL_SHA=be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21
MODEL_SIZE=77691713
WORK=$(mktemp -d /tmp/jukahub-stt.XXXXXXXX)
trap 'rm -rf "$WORK"' EXIT HUP INT TERM

curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
    "https://github.com/ggml-org/whisper.cpp/archive/$WHISPER_COMMIT.tar.gz" \
    -o "$WORK/whisper.tar.gz"
printf '%s  %s\n' "$SOURCE_SHA" "$WORK/whisper.tar.gz" | sha256sum -c -
tar -xzf "$WORK/whisper.tar.gz" -C "$WORK"
SRC="$WORK/whisper.cpp-$WHISPER_COMMIT"

cmake -S "$SRC" -B "$WORK/build" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    -DCMAKE_EXE_LINKER_FLAGS='-static-libstdc++ -static-libgcc' \
    -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF \
    -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON \
    -DWHISPER_BUILD_SERVER=OFF -DWHISPER_COMMON_FFMPEG=OFF -DWHISPER_SDL2=OFF
BUILD_JOBS=${JOBS:-4}
if [ "$BUILD_JOBS" -gt 4 ]; then BUILD_JOBS=4; fi
cmake --build "$WORK/build" --target whisper-cli --parallel "$BUILD_JOBS"
BIN="$WORK/build/bin/whisper-cli"
test -s "$BIN"
file "$BIN" | grep -q 'ELF 64-bit.*ARM aarch64'
# The firmware has glibc 2.33. Linking in Focal keeps dependencies older.
if readelf --version-info "$BIN" | grep -Eo 'GLIBC_[0-9]+\.[0-9]+' | \
    sed 's/GLIBC_//' | awk -F. '$1 > 2 || ($1 == 2 && $2 > 31) { bad = 1 } END { exit !bad }'; then
    echo 'whisper-cli requires a glibc newer than 2.31' >&2
    exit 1
fi
# The GNU ARM loader is named in DT_NEEDED by this toolchain. It is part of
# glibc on the handheld, alongside the C and math libraries allowed below.
if readelf -d "$BIN" | grep NEEDED | grep -Ev 'Shared library: \[(lib(c|m|pthread|dl|rt)\.so\.[^]]+|ld-linux-aarch64\.so\.1)\]$'; then
    echo 'whisper-cli has an unexpected shared dependency' >&2
    exit 1
fi
"$BIN" --help > /dev/null 2>&1

curl -fL --retry 3 --connect-timeout 15 --max-time 1200 \
    "https://huggingface.co/ggerganov/whisper.cpp/resolve/$MODEL_REV/ggml-tiny.bin?download=true" \
    -o "$WORK/ggml-tiny.bin"
test "$(wc -c < "$WORK/ggml-tiny.bin")" -eq "$MODEL_SIZE"
printf '%s  %s\n' "$MODEL_SHA" "$WORK/ggml-tiny.bin" | sha256sum -c -

# A known English clip proves the binary, model and WAV path work together.
"$BIN" -m "$WORK/ggml-tiny.bin" -f - \
    -t 2 -l en -ng -np -nt -otxt -of - \
    < "$SRC/samples/jfk.wav" > "$WORK/smoke.txt" 2> /dev/null
test -s "$WORK/smoke.txt"
grep -qi 'fellow Americans' "$WORK/smoke.txt"

OUT=/src/player/native/bin/juka-whisper
mkdir -p "$OUT"
install -m 755 "$BIN" "$OUT/whisper-cli"
install -m 644 "$WORK/ggml-tiny.bin" "$OUT/ggml-tiny.bin"
install -m 644 "$SRC/LICENSE" "$OUT/LICENSE.txt"
printf 'whisper.cpp %s\nmodel %s\nsource sha256 %s\nmodel sha256 %s\n' \
    "$WHISPER_COMMIT" "$MODEL_REV" "$SOURCE_SHA" "$MODEL_SHA" > "$OUT/BUILDINFO.txt"
printf 'binary sha256 %s\n' "$(sha256sum "$OUT/whisper-cli" | cut -d' ' -f1)" >> "$OUT/BUILDINFO.txt"
echo "Offline speech ready in $OUT"
