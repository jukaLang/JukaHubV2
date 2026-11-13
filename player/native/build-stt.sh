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

VOSK_ARCHIVE_SHA=45e95d37755deb07568e79497d7feba8c03aee5a9e071df29961aa023fd94541
VOSK_LIB_SHA=0e9df29f060a93cf3df3263a4d3635e1b75688a5fd84e86ade1599372e3c9597
VOSK_MODEL_SHA=30f26242c4eb449f948e42cb302dd7a686cb29a3423a8367f99ff41780942498
VOSK_MODEL_SIZE=41205931
VOSK_LICENSE_SHA=bb618af833c817ab01c1564bd3ec75ba4b4f9cf30910a4a2168e30a674ee8cc6
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

# Vosk's compact recognizer handles English voice commands substantially
# faster on the Smart Pro. Whisper stays in the package for other languages
# and for a Vosk startup failure.
curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
    https://github.com/alphacep/vosk-api/releases/download/v0.3.45/vosk-linux-aarch64-0.3.45.zip \
    -o "$WORK/vosk-linux-aarch64.zip"
printf '%s  %s\n' "$VOSK_ARCHIVE_SHA" "$WORK/vosk-linux-aarch64.zip" | sha256sum -c -
unzip -q "$WORK/vosk-linux-aarch64.zip" -d "$WORK/vosk"
VOSK="$WORK/vosk/vosk-linux-aarch64-0.3.45"
printf '%s  %s\n' "$VOSK_LIB_SHA" "$VOSK/libvosk.so" | sha256sum -c -

curl -fL --retry 3 --connect-timeout 15 --max-time 1200 \
    https://alphacephei.com/vosk/models/vosk-model-small-en-us-0.15.zip \
    -o "$WORK/vosk-model-small-en-us-0.15.zip"
test "$(wc -c < "$WORK/vosk-model-small-en-us-0.15.zip")" -eq "$VOSK_MODEL_SIZE"
printf '%s  %s\n' "$VOSK_MODEL_SHA" "$WORK/vosk-model-small-en-us-0.15.zip" | sha256sum -c -

curl -fL --retry 3 --connect-timeout 15 --max-time 120 \
    https://raw.githubusercontent.com/alphacep/vosk-api/v0.3.45/COPYING \
    -o "$WORK/VOSK_LICENSE.txt"
printf '%s  %s\n' "$VOSK_LICENSE_SHA" "$WORK/VOSK_LICENSE.txt" | sha256sum -c -

cc -O2 -Wall -Wextra -Werror -I "$VOSK" \
    /src/player/native/vosk-cli.c -o "$WORK/vosk-cli" -ldl
file "$WORK/vosk-cli" | grep -q 'ELF 64-bit.*ARM aarch64'
"$WORK/vosk-cli" --help > /dev/null

OUT=/src/player/native/bin/juka-whisper
mkdir -p "$OUT"
install -m 755 "$BIN" "$OUT/whisper-cli"
install -m 644 "$WORK/ggml-tiny.bin" "$OUT/ggml-tiny.bin"
install -m 644 "$SRC/LICENSE" "$OUT/LICENSE.txt"
install -m 755 "$WORK/vosk-cli" "$VOSK/libvosk.so" "$OUT/"
install -m 644 "$WORK/vosk-model-small-en-us-0.15.zip" "$WORK/VOSK_LICENSE.txt" "$OUT/"
printf 'whisper.cpp %s\nmodel %s\nsource sha256 %s\nmodel sha256 %s\n' \
    "$WHISPER_COMMIT" "$MODEL_REV" "$SOURCE_SHA" "$MODEL_SHA" > "$OUT/BUILDINFO.txt"
printf 'binary sha256 %s\n' "$(sha256sum "$OUT/whisper-cli" | cut -d' ' -f1)" >> "$OUT/BUILDINFO.txt"
printf 'vosk archive sha256 %s\nvosk library sha256 %s\nvosk model-archive sha256 %s\nvosk binary sha256 %s\n' \
    "$VOSK_ARCHIVE_SHA" "$VOSK_LIB_SHA" "$VOSK_MODEL_SHA" \
    "$(sha256sum "$OUT/vosk-cli" | cut -d' ' -f1)" >> "$OUT/BUILDINFO.txt"
echo "Offline speech ready in $OUT"
