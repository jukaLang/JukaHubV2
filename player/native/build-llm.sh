#!/bin/sh
# Build the optional Smart Pro text model in build-root.sh's Ubuntu 20.04 arm64
# root. The source, model and both licenses are pinned and verified.
# llama-server keeps the model loaded between questions (offline_ai_server.go);
# llama-cli stays in the package as the one-shot fallback.
#
# Everything here is bounded: a download that stalls is cut off and retried, a
# compile or a smoke run that hangs is killed, and llama-server is stopped with
# SIGKILL when SIGTERM does not end it. (Before, a server that took its time
# over a graceful shutdown left `wait` blocked until the job's own timeout, and
# the workflow sat on "Build optional offline AI" for 40 minutes.) Each phase
# prints a line with the time it took, so the log shows where a run is.
#
# The pins below (MODEL_NAME, MODEL_SHA, MODEL_SIZE) are read by
# .github/workflows/offline-ai.yml and package-llm.sh: change them here only.
set -eu

LLAMA_COMMIT=4762ad7316dcdec20016ab5985fb46a27902204d
SOURCE_SHA=06b6a3a1c2d12becc05d02c701274ab1f6d033399b4a20a166b919289e19233c
MODEL_REV=09816acd5d99df7be770d85ea30822623dab342c
MODEL_NAME=SmolLM2-135M-Instruct-Q4_0.gguf
MODEL_SHA=bcc3af2849ad6095af57e9b5cd43775256efdc66e306acb529172f92d0c04b03
MODEL_SIZE=91893088
APACHE_SHA=cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30

WORK=$(mktemp -d /tmp/jukahub-llm.XXXXXXXX)
SERVER_PID=
cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill -KILL "$SERVER_PID" 2> /dev/null || true
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

START=$(date +%s)
phase() {
    echo "==> [$(( $(date +%s) - START )) s] $*"
}

# fetch URL OUT MAX_SECONDS: a transfer slower than 20 kB/s for a whole minute
# counts as stalled; curl gives up on it and --retry starts it again.
fetch() {
    curl -fL --retry 4 --retry-delay 5 --connect-timeout 15 \
        --speed-limit 20480 --speed-time 60 --max-time "$3" \
        -sS "$1" -o "$2"
}

phase "llama.cpp $LLAMA_COMMIT"
fetch "https://github.com/ggml-org/llama.cpp/archive/$LLAMA_COMMIT.tar.gz" "$WORK/llama.tar.gz" 600
printf '%s  %s\n' "$SOURCE_SHA" "$WORK/llama.tar.gz" | sha256sum -c -
tar -xzf "$WORK/llama.tar.gz" -C "$WORK"
SRC="$WORK/llama.cpp-$LLAMA_COMMIT"

# The model, early: it downloads while nothing else needs the network, and a
# wrong pin fails the run before the compile instead of after it.
phase "model $MODEL_NAME"
MODEL="$WORK/$MODEL_NAME"
fetch "https://huggingface.co/bartowski/SmolLM2-135M-Instruct-GGUF/resolve/$MODEL_REV/$MODEL_NAME?download=true" \
    "$MODEL" 900
test "$(wc -c < "$MODEL")" -eq "$MODEL_SIZE"
printf '%s  %s\n' "$MODEL_SHA" "$MODEL" | sha256sum -c -

fetch 'https://www.apache.org/licenses/LICENSE-2.0.txt' "$WORK/MODEL_LICENSE.txt" 120
printf '%s  %s\n' "$APACHE_SHA" "$WORK/MODEL_LICENSE.txt" | sha256sum -c -

phase "configure"
# Static and CPU-only: the handheld gets two self-contained executables that
# need nothing but its C library (no libllama.so or libggml*.so to keep beside
# them, which a GGML_BACKEND_DL build would need).
timeout -k 10 300 cmake -S "$SRC" -B "$WORK/build" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    -DCMAKE_EXE_LINKER_FLAGS='-static-libstdc++ -static-libgcc' \
    -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF -DGGML_VULKAN=OFF -DGGML_BACKEND_DL=OFF \
    -DLLAMA_CURL=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_SERVER=ON \
    -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TOOLS=ON > "$WORK/configure.log" 2>&1 || {
    tail -n 60 "$WORK/configure.log" >&2
    exit 1
}

phase "compile llama-cli and llama-server"
BUILD_JOBS=${JOBS:-4}
if [ "$BUILD_JOBS" -gt 4 ]; then BUILD_JOBS=4; fi
# Ninja prints a line as each file finishes; server.cpp, with the web page
# compiled into it, is the slowest by far (several quiet minutes).
if ! timeout -k 30 1500 cmake --build "$WORK/build" --target llama-cli llama-server --parallel "$BUILD_JOBS"; then
    echo 'llama.cpp did not compile within 25 minutes (or failed: see above)' >&2
    exit 1
fi
BIN="$WORK/build/bin/llama-cli"
SERVER="$WORK/build/bin/llama-server"

phase "check the executables"
for exe in "$BIN" "$SERVER"; do
    name=$(basename "$exe")
    test -s "$exe"
    file "$exe" | grep -q 'ELF 64-bit.*ARM aarch64'
    if readelf --version-info "$exe" | grep -Eo 'GLIBC_[0-9]+\.[0-9]+' | \
        sed 's/GLIBC_//' | awk -F. '$1 > 2 || ($1 == 2 && $2 > 31) { bad = 1 } END { exit !bad }'; then
        echo "$name requires a glibc newer than 2.31" >&2
        exit 1
    fi
    if readelf -d "$exe" | grep NEEDED | grep -Ev 'Shared library: \[(lib(c|m|pthread|dl|rt)\.so\.[^]]+|ld-linux-aarch64\.so\.1)\]$'; then
        echo "$name has an unexpected shared dependency" >&2
        exit 1
    fi
    timeout -k 5 30 "$exe" --help < /dev/null > /dev/null 2>&1
done

# The one-shot runner with the options offlineAIArgs (offline_ai.go) gives it.
# stdin is /dev/null: a runner that wanted to chat would read end-of-file and
# stop instead of waiting for a keyboard. The check is that the GGUF loads and
# text comes out; a 135M model's arithmetic is not something to fail on.
phase "smoke test: llama-cli"
SMOKE_PROMPT=$(printf '<|im_start|>user\nWhat is two plus two?<|im_end|>\n<|im_start|>assistant\n')
if ! timeout -k 10 180 "$BIN" -m "$MODEL" -p "$SMOKE_PROMPT" \
    -n 32 -c 384 -b 64 -ub 64 -t 4 -ngl 0 --temp 0 \
    --no-conversation --single-turn --no-display-prompt --no-warmup --simple-io \
    < /dev/null > "$WORK/smoke.txt" 2> "$WORK/smoke.log"; then
    tail -n 40 "$WORK/smoke.log" >&2
    echo 'llama-cli did not answer' >&2
    exit 1
fi
# llama-cli ends its output with " [end of text]", which is not an answer.
if ! sed 's/\[end of text\]//g' "$WORK/smoke.txt" | grep -q '[[:alnum:]]'; then
    tail -n 40 "$WORK/smoke.log" >&2
    echo 'llama-cli answered with no text' >&2
    exit 1
fi
echo "    llama-cli: $(tr '\n' ' ' < "$WORK/smoke.txt" | cut -c1-120)"

# The server with exactly the options offlineAIServerArgs (offline_ai_server.go)
# gives it: an option this llama.cpp does not know fails the build here, not
# on the handheld. Keep the two lists the same.
phase "smoke test: llama-server"
SMOKE_PORT=18631
SMOKE_KEY=jukahub-smoke
"$SERVER" -m "$MODEL" --host 127.0.0.1 --port "$SMOKE_PORT" --api-key "$SMOKE_KEY" \
    -c 384 -np 1 -t 3 -tb 4 -b 64 -ub 64 -ngl 0 --no-warmup \
    < /dev/null > "$WORK/server.log" 2>&1 &
SERVER_PID=$!
up=0
for _ in $(seq 1 120); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$SMOKE_PORT/health" > /dev/null 2>&1; then
        up=1
        break
    fi
    kill -0 "$SERVER_PID" 2> /dev/null || break
    sleep 1
done
if [ "$up" != 1 ]; then
    tail -n 40 "$WORK/server.log" >&2
    echo 'llama-server did not become ready' >&2
    exit 1
fi
# The same request shape the app streams (stream=false here, to grep it).
if ! curl -fsS --max-time 120 -H "Authorization: Bearer $SMOKE_KEY" \
    -H 'Content-Type: application/json' \
    -d '{"prompt":"<|im_start|>user\nWhat is two plus two?<|im_end|>\n<|im_start|>assistant\n","n_predict":32,"temperature":0,"cache_prompt":true,"stream":false,"stop":["<|im_end|>","<|im_start|>"]}' \
    "http://127.0.0.1:$SMOKE_PORT/completion" > "$WORK/server-smoke.json"; then
    tail -n 40 "$WORK/server.log" >&2
    echo 'llama-server did not answer' >&2
    exit 1
fi
# Only the answer's text: the response's timings and settings hold digits too.
grep -o '"content":"[^"]*"' "$WORK/server-smoke.json" | head -n 1 > "$WORK/server-smoke.txt" || true
if ! grep -q '"content":"[^"]*[[:alnum:]]' "$WORK/server-smoke.txt"; then
    head -c 2000 "$WORK/server-smoke.json" >&2
    echo '' >&2
    echo 'llama-server answered with no text' >&2
    exit 1
fi
echo "    llama-server: $(cut -c1-140 "$WORK/server-smoke.txt")"
# SIGTERM asks for a graceful shutdown; give it 15 s, then SIGKILL. Never a
# bare `wait`, which is what used to hang the job.
kill -TERM "$SERVER_PID" 2> /dev/null || true
for _ in $(seq 1 15); do
    kill -0 "$SERVER_PID" 2> /dev/null || break
    sleep 1
done
if kill -0 "$SERVER_PID" 2> /dev/null; then
    echo '    llama-server ignored SIGTERM for 15 s; killing it'
    kill -KILL "$SERVER_PID" 2> /dev/null || true
fi
wait "$SERVER_PID" 2> /dev/null || true
SERVER_PID=

phase "install"
OUT=/src/player/native/bin/juka-llm
mkdir -p "$OUT"
install -m 755 "$BIN" "$OUT/llama-cli"
install -m 755 "$SERVER" "$OUT/llama-server"
install -m 644 "$MODEL" "$OUT/$MODEL_NAME"
install -m 644 "$SRC/LICENSE" "$OUT/LLAMA_LICENSE.txt"
install -m 644 "$WORK/MODEL_LICENSE.txt" "$OUT/MODEL_LICENSE.txt"
printf 'llama.cpp %s\nmodel %s\nmodel name %s\nsource sha256 %s\nmodel sha256 %s\n' \
    "$LLAMA_COMMIT" "$MODEL_REV" "$MODEL_NAME" "$SOURCE_SHA" "$MODEL_SHA" > "$OUT/BUILDINFO.txt"
printf 'binary sha256 %s\n' "$(sha256sum "$OUT/llama-cli" | cut -d' ' -f1)" >> "$OUT/BUILDINFO.txt"
printf 'server sha256 %s\n' "$(sha256sum "$OUT/llama-server" | cut -d' ' -f1)" >> "$OUT/BUILDINFO.txt"
phase "offline AI ready in $OUT"
