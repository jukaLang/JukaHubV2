#!/bin/sh
# Build the optional Smart Pro text model in build-root.sh's Ubuntu 20.04 arm64
# root. The source, model and both licenses are pinned and verified.
set -eu

LLAMA_COMMIT=4762ad7316dcdec20016ab5985fb46a27902204d
SOURCE_SHA=06b6a3a1c2d12becc05d02c701274ab1f6d033399b4a20a166b919289e19233c
MODEL_REV=7be6f65f1db715fe5dc5a4634c0d459b4eed42ec
MODEL_NAME=SmolLM2-360M-Instruct-Q4_0.gguf
MODEL_SHA=c3608933eb6e5763b87f769bda40c204dc158333668c7af214644fe39da58627
MODEL_SIZE=229733280
APACHE_SHA=cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30

WORK=$(mktemp -d /tmp/jukahub-llm.XXXXXXXX)
trap 'rm -rf "$WORK"' EXIT HUP INT TERM

curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
    "https://github.com/ggml-org/llama.cpp/archive/$LLAMA_COMMIT.tar.gz" \
    -o "$WORK/llama.tar.gz"
printf '%s  %s\n' "$SOURCE_SHA" "$WORK/llama.tar.gz" | sha256sum -c -
tar -xzf "$WORK/llama.tar.gz" -C "$WORK"
SRC="$WORK/llama.cpp-$LLAMA_COMMIT"

cmake -S "$SRC" -B "$WORK/build" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    -DCMAKE_EXE_LINKER_FLAGS='-static-libstdc++ -static-libgcc' \
    -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF -DGGML_VULKAN=OFF \
    -DLLAMA_CURL=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_SERVER=OFF \
    -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TOOLS=ON
BUILD_JOBS=${JOBS:-4}
if [ "$BUILD_JOBS" -gt 4 ]; then BUILD_JOBS=4; fi
cmake --build "$WORK/build" --target llama-cli --parallel "$BUILD_JOBS"
BIN="$WORK/build/bin/llama-cli"
test -s "$BIN"
file "$BIN" | grep -q 'ELF 64-bit.*ARM aarch64'
if readelf --version-info "$BIN" | grep -Eo 'GLIBC_[0-9]+\.[0-9]+' | \
    sed 's/GLIBC_//' | awk -F. '$1 > 2 || ($1 == 2 && $2 > 31) { bad = 1 } END { exit !bad }'; then
    echo 'llama-cli requires a glibc newer than 2.31' >&2
    exit 1
fi
if readelf -d "$BIN" | grep NEEDED | grep -Ev 'Shared library: \[(lib(c|m|pthread|dl|rt)\.so\.[^]]+|ld-linux-aarch64\.so\.1)\]$'; then
    echo 'llama-cli has an unexpected shared dependency' >&2
    exit 1
fi
"$BIN" --help > /dev/null 2>&1

MODEL="$WORK/$MODEL_NAME"
curl -fL --retry 3 --connect-timeout 15 --max-time 2400 \
    "https://huggingface.co/bartowski/SmolLM2-360M-Instruct-GGUF/resolve/$MODEL_REV/$MODEL_NAME?download=true" \
    -o "$MODEL"
test "$(wc -c < "$MODEL")" -eq "$MODEL_SIZE"
printf '%s  %s\n' "$MODEL_SHA" "$MODEL" | sha256sum -c -

# This small known answer also checks that the runner understands the GGUF.
SMOKE_PROMPT=$(printf '<|im_start|>user\nWhat is two plus two?<|im_end|>\n<|im_start|>assistant\n')
timeout 180 "$BIN" -m "$MODEL" -p "$SMOKE_PROMPT" \
    -n 32 -c 256 -b 64 -ub 64 -t 2 -ngl 0 --temp 0.2 \
    --no-conversation --single-turn --no-display-prompt --no-warmup --simple-io \
    > "$WORK/smoke.txt" 2> "$WORK/smoke.log"
grep -Eiq '(^|[^0-9])(4|four)([^0-9]|$)' "$WORK/smoke.txt"

curl -fL --retry 3 --connect-timeout 15 --max-time 120 \
    'https://www.apache.org/licenses/LICENSE-2.0.txt' -o "$WORK/MODEL_LICENSE.txt"
printf '%s  %s\n' "$APACHE_SHA" "$WORK/MODEL_LICENSE.txt" | sha256sum -c -

OUT=/src/player/native/bin/juka-llm
mkdir -p "$OUT"
install -m 755 "$BIN" "$OUT/llama-cli"
install -m 644 "$MODEL" "$OUT/$MODEL_NAME"
install -m 644 "$SRC/LICENSE" "$OUT/LLAMA_LICENSE.txt"
install -m 644 "$WORK/MODEL_LICENSE.txt" "$OUT/MODEL_LICENSE.txt"
printf 'llama.cpp %s\nmodel %s\nmodel name %s\nsource sha256 %s\nmodel sha256 %s\n' \
    "$LLAMA_COMMIT" "$MODEL_REV" "$MODEL_NAME" "$SOURCE_SHA" "$MODEL_SHA" > "$OUT/BUILDINFO.txt"
printf 'binary sha256 %s\n' "$(sha256sum "$OUT/llama-cli" | cut -d' ' -f1)" >> "$OUT/BUILDINFO.txt"
echo "Offline AI ready in $OUT"
