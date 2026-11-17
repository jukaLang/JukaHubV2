#!/bin/sh
# Build the optional natural offline voice for the TrimUI handhelds in
# build-root.sh's Ubuntu 20.04 (glibc 2.31) arm64 root: Gradium's Phonon 31M
# English model (Apache-2.0) behind the OpenAI-compatible speech server of
# xn-ptts (gradium-ai/xn-ptts), which JukaHub keeps loaded between lines
# (tts_phonon.go) and asks for raw 24 kHz PCM, one sentence at a time.
#
# Why build it here rather than take Gradium's own Linux ARM64 archive: xn
# picks its matrix kernels when it is compiled, and the stock arm64 build turns
# on the dotprod and fp16 instructions (ARMv8.2). The Smart Pro's and Brick's
# Allwinner A133P has four Cortex-A53 cores (ARMv8.0) without either, so the
# stock binary would die with "Illegal instruction" there. This build targets
# the Cortex-A53, and its smoke run goes through qemu's Cortex-A53 model with
# the network cut off, which proves both that no ARMv8.2 instruction is on its
# path and that the package needs nothing from the internet once installed.
#
# It lives in .github/scripts rather than player/native so that it is not part
# of the Remote Play helpers' reuse key (build-root.sh hashes player/native).
#
# Output: /src/player/native/bin/juka-voice/{juka-voice.tar,BUILDINFO.txt}.
# juka-voice.tar holds the folder juka-voice/ that package-voice.sh zips into
# JukaHub-Voice-linux-arm64.zip:
#
#   juka-voice-server   the server (a "juka" name: Close all never ends it)
#   lib/                the shared libraries it needs beyond the C library
#   model/ or hf/       the model, voices and tokenizer, in the form the
#                       offline smoke run proved works
#   voice.json          how JukaHub starts and asks it (the smoke run's own
#                       arguments, the address flag, voices, memory, rate)
#   *LICENSE*.txt       xn-ptts's and the model's licences
#
# Every phase is bounded and prints the time it took, as in build-llm.sh.
#
#   build-voice.sh                    the build (as root, in the build root)
#   build-voice.sh offline-inner NAME used by the build itself: one offline
#                                     smoke run inside `unshare -n`
set -eu

# The release is pinned by tag and checked against the commit its release
# notes name; BUILDINFO.txt records the full commit id.
PTTS_REPO=https://github.com/gradium-ai/xn-ptts.git
PTTS_TAG=v0.6.0
PTTS_COMMIT_PREFIX=95bf24d
APACHE_SHA=cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30
SMOKE_TEXT='Hello, I am Juka. This is my natural offline voice.'
PORT=18741

SERVER_PID=

# stop_server: SIGTERM, 15 s, then SIGKILL; never a bare wait.
stop_server() {
    if [ -z "$SERVER_PID" ]; then
        return 0
    fi
    kill -TERM "$SERVER_PID" 2> /dev/null || true
    for _ in $(seq 1 15); do
        kill -0 "$SERVER_PID" 2> /dev/null || break
        sleep 1
    done
    kill -KILL "$SERVER_PID" 2> /dev/null || true
    wait "$SERVER_PID" 2> /dev/null || true
    SERVER_PID=
}

# wait_health SECONDS LOG: until GET /health answers, the server exits, or
# SECONDS pass.
wait_health() {
    for _ in $(seq 1 "$1"); do
        if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" > /dev/null 2>&1; then
            return 0
        fi
        kill -0 "$SERVER_PID" 2> /dev/null || break
        sleep 1
    done
    tail -n 40 "$2" >&2
    return 1
}

# speak OUT VOICE MAX: one request in the shape tts_phonon.go sends
# (response_format pcm); VOICE may be empty (the server's default voice).
speak() {
    sp_voice=
    if [ -n "$2" ]; then
        sp_voice=",\"voice\":\"$2\""
    fi
    sp_model=
    if [ -n "${REQ_MODEL:-}" ]; then
        sp_model="\"model\":\"$REQ_MODEL\","
    fi
    curl -fsS --max-time "$3" -H 'Content-Type: application/json' \
        -d "{${sp_model}\"input\":\"$SMOKE_TEXT\"${sp_voice},\"response_format\":\"pcm\"}" \
        "http://127.0.0.1:$PORT/v1/audio/speech" -o "$1"
}

# pcm_ok FILE: at least a second of 16-bit 24 kHz sound that is not silence.
pcm_ok() {
    python3 - "$1" << 'EOF'
import struct, sys
data = open(sys.argv[1], "rb").read()
if data[:4] == b"RIFF":
    i = data.find(b"data")
    data = data[i + 8:] if i >= 0 else b""
n = len(data) // 2
if n < 24000:
    sys.exit("only %d samples" % n)
peak = max(abs(v) for v in struct.unpack("<%dh" % n, data[: n * 2]))
if peak < 500:
    sys.exit("silent (peak %d)" % peak)
print("    %.2f s of sound, peak %d" % (n / 24000.0, peak))
EOF
}

# ── offline-inner: one smoke run in a namespace with only a loopback ──────
#
# The parent passes WORK, PKG, QEMU, FEMALE, REQ_MODEL, OFF_ENV and OFF_ARGS.
# The server runs under qemu's Cortex-A53 model, so an ARMv8.2 instruction
# anywhere on the path it takes stops it with SIGILL here, not on a handheld.
if [ "${1:-}" = offline-inner ]; then
    NAME=${2:?}
    trap 'stop_server' EXIT
    # dash skips the EXIT trap when a signal kills it: turn the signal (from
    # the parent's timeout) into an exit, so the emulated server goes too.
    trap 'exit 1' HUP INT TERM
    ip link set lo up
    # shellcheck disable=SC2086 # OFF_ENV and OFF_ARGS are word lists
    env $OFF_ENV "$QEMU" -cpu cortex-a53 "$PKG/juka-voice-server" $OFF_ARGS \
        --addr "127.0.0.1:$PORT" < /dev/null > "$WORK/server-$NAME.log" 2>&1 &
    SERVER_PID=$!
    # Under emulation everything is ten to fifty times slower.
    wait_health 900 "$WORK/server-$NAME.log"
    if ! speak "$WORK/offline-$NAME.pcm" "$FEMALE" 900 2> "$WORK/speak-$NAME.err"; then
        cat "$WORK/speak-$NAME.err" >&2
        tail -n 20 "$WORK/server-$NAME.log" >&2
        exit 1
    fi
    pcm_ok "$WORK/offline-$NAME.pcm"
    exit 0
fi

WORK=$(mktemp -d /tmp/jukahub-voice.XXXXXXXX)
cleanup() {
    stop_server
    rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

START=$(date +%s)
phase() {
    echo "==> [$(( $(date +%s) - START )) s] $*"
}

fetch() {
    curl -fL --retry 4 --retry-delay 5 --connect-timeout 15 \
        --speed-limit 20480 --speed-time 60 --max-time "$3" \
        -sS "$1" -o "$2"
}

phase "build dependencies"
apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends \
    libopus-dev libmp3lame-dev clang libclang-dev cmake qemu-user binutils iproute2 \
    > "$WORK/apt.log" 2>&1 || {
    tail -n 40 "$WORK/apt.log" >&2
    exit 1
}
QEMU=$(command -v qemu-aarch64 || true)
if [ -z "$QEMU" ]; then
    echo 'qemu-aarch64 is missing: cannot prove the build runs on a Cortex-A53' >&2
    exit 1
fi

phase "Rust (stable, minimal)"
export CARGO_HOME="$WORK/cargo" RUSTUP_HOME="$WORK/rustup"
export PATH="$CARGO_HOME/bin:$PATH"
RUSTUP_URL=https://static.rust-lang.org/rustup/dist/aarch64-unknown-linux-gnu/rustup-init
fetch "$RUSTUP_URL" "$WORK/rustup-init" 300
fetch "$RUSTUP_URL.sha256" "$WORK/rustup-init.sha256" 60
printf '%s  %s\n' "$(cut -d' ' -f1 "$WORK/rustup-init.sha256")" "$WORK/rustup-init" | sha256sum -c -
chmod 755 "$WORK/rustup-init"
timeout -k 10 600 "$WORK/rustup-init" -y -q --no-modify-path --profile minimal \
    --default-toolchain stable > "$WORK/rustup.log" 2>&1 || {
    tail -n 40 "$WORK/rustup.log" >&2
    exit 1
}
RUSTC_VERSION=$(rustc --version)
echo "    $RUSTC_VERSION"

phase "xn-ptts $PTTS_TAG"
timeout -k 10 600 git clone -q --depth 1 --branch "$PTTS_TAG" "$PTTS_REPO" "$WORK/xn-ptts"
PTTS_COMMIT=$(git -C "$WORK/xn-ptts" rev-parse HEAD)
case "$PTTS_COMMIT" in
    "$PTTS_COMMIT_PREFIX"*) ;;
    *)
        echo "xn-ptts $PTTS_TAG is $PTTS_COMMIT, not the pinned $PTTS_COMMIT_PREFIX: the tag moved" >&2
        exit 1
        ;;
esac
SRC="$WORK/xn-ptts"

phase "compile ptts-openai-server for the Cortex-A53"
# RUSTFLAGS outranks any rustflags in the project's .cargo/config.toml, so
# nothing turns dotprod or fp16 back on; ARM64_FEATURES is the knob the
# project's own scripts read for the same thing, and CFLAGS covers any C a
# crate compiles.
export RUSTFLAGS='-C target-cpu=cortex-a53'
export ARM64_FEATURES=
export CFLAGS='-O2 -mcpu=cortex-a53' CXXFLAGS='-O2 -mcpu=cortex-a53'
export CARGO_PROFILE_RELEASE_STRIP=symbols
BUILD_JOBS=${JOBS:-4}
if [ "$BUILD_JOBS" -gt 4 ]; then BUILD_JOBS=4; fi
LOCKED=
if [ -f "$SRC/Cargo.lock" ]; then LOCKED=--locked; fi
# shellcheck disable=SC2086 # LOCKED is empty or one flag
if ! (cd "$SRC" && timeout -k 30 2700 cargo build --release $LOCKED -j "$BUILD_JOBS" \
    -p ptts-openai-server > "$WORK/cargo.log" 2>&1); then
    tail -n 80 "$WORK/cargo.log" >&2
    echo 'ptts-openai-server did not compile within 45 minutes (or failed: see above)' >&2
    exit 1
fi
SERVER="$SRC/target/release/ptts-openai-server"
test -s "$SERVER"

phase "check the executable"
file "$SERVER" | grep -q 'ELF 64-bit.*ARM aarch64'
# ARMv8.2 instructions the A53 does not have: the dot products, and half
# precision arithmetic (fp16 conversions are ARMv8.0 and allowed).
objdump -d --no-show-raw-insn "$SERVER" > "$WORK/disasm.txt"
DOT='[[:space:]](sdot|udot|usdot|sudot)[[:space:]]'
HALF='[[:space:]](fmla|fmls|fmul|fadd|fsub|fdiv|fmax|fmin|fabd)[[:space:]]+(v[0-9]+\.(4h|8h)|h[0-9]+,)'
# A warning, not a failure: a crate may carry such kernels behind a run-time
# CPU check. What decides is the offline smoke run below, under qemu's
# Cortex-A53, which stops at the first instruction that CPU lacks (and qemu's
# hardware capabilities there make run-time checks pick the plain kernels).
if grep -Eq "$DOT" "$WORK/disasm.txt"; then
    grep -Em 5 "$DOT" "$WORK/disasm.txt"
    echo '    warning: the server contains dot-product instructions (the A53 smoke run decides)'
fi
if grep -Eq "$HALF" "$WORK/disasm.txt"; then
    grep -Em 5 "$HALF" "$WORK/disasm.txt"
    echo '    warning: the server contains half-precision arithmetic (the A53 smoke run decides)'
fi
rm -f "$WORK/disasm.txt"

phase "package layout"
PKG="$WORK/pkg/juka-voice"
mkdir -p "$PKG/lib"
install -m 755 "$SERVER" "$PKG/juka-voice-server"
# Every shared library beyond the C library's own goes into lib/, found
# through an $ORIGIN run path, so the firmware's copies (if any) never matter.
readelf -d "$PKG/juka-voice-server" | sed -n 's/.*Shared library: \[\(.*\)\]$/\1/p' > "$WORK/needed.txt"
while read -r lib; do
    case "$lib" in
        libc.so.*|libm.so.*|libpthread.so.*|libdl.so.*|librt.so.*|ld-linux-aarch64.so.1) continue ;;
    esac
    path=$(ldconfig -p | awk -v l="$lib" '$1 == l { print $NF; exit }')
    if [ -z "$path" ] || [ ! -f "$path" ]; then
        echo "cannot find $lib for the package" >&2
        exit 1
    fi
    cp -L "$path" "$PKG/lib/$lib"
    chmod 644 "$PKG/lib/$lib"
    echo "    bundled $lib"
done < "$WORK/needed.txt"
# shellcheck disable=SC2016 # $ORIGIN is for the loader, not the shell
patchelf --set-rpath '$ORIGIN/lib' "$PKG/juka-voice-server"
for exe in "$PKG/juka-voice-server" "$PKG"/lib/*; do
    [ -f "$exe" ] || continue
    if readelf --version-info "$exe" | grep -Eo 'GLIBC_[0-9]+\.[0-9]+' | \
        sed 's/GLIBC_//' | awk -F. '$1 > 2 || ($1 == 2 && $2 > 31) { bad = 1 } END { exit !bad }'; then
        echo "$(basename "$exe") requires a glibc newer than 2.31" >&2
        exit 1
    fi
done

# The server fetches its default model (Phonon 31M English, pinned by xn-ptts
# to one commit) into HF_HOME on its first start: that native run is the
# download, the request-shape check and the speed and memory measurement.
phase "first start: download the model and measure"
HF="$WORK/hf"
mkdir -p "$HF"
HF_HOME="$HF" "$PKG/juka-voice-server" --quant q8 --lang en --addr "127.0.0.1:$PORT" \
    < /dev/null > "$WORK/server-online.log" 2>&1 &
SERVER_PID=$!
if ! wait_health 600 "$WORK/server-online.log"; then
    echo 'the server did not become ready (model download or load failed)' >&2
    exit 1
fi
curl -fsS --max-time 10 "http://127.0.0.1:$PORT/v1/models" -o "$WORK/models.json" || echo '{}' > "$WORK/models.json"
curl -fsS --max-time 10 "http://127.0.0.1:$PORT/v1/audio/voices" -o "$WORK/voices.json" || echo '{}' > "$WORK/voices.json"
REQ_MODEL=$(python3 - "$WORK/models.json" << 'EOF'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    items = d.get("data", []) if isinstance(d, dict) else d
    first = items[0] if items else {}
    print(first.get("id", "") if isinstance(first, dict) else "")
except Exception:
    print("")
EOF
)
# A female and a male voice for Juka (tts_phonon.go picks by the gender of
# the chosen Groq voice): Freya and Easton when the catalogue has them, else
# its first. The names are printed for the job log.
VOICES=$(python3 - "$WORK/voices.json" << 'EOF'
import json, sys
names = []
def walk(v, key=""):
    if isinstance(v, dict):
        for k, x in v.items():
            walk(x, k)
    elif isinstance(v, list):
        for x in v:
            walk(x, key)
    elif isinstance(v, str) and key in ("", "id", "name", "voice", "voice_id", "voices", "data") \
            and 0 < len(v) < 64 and v.replace("_", "").replace("-", "").isalnum():
        names.append(v)
try:
    walk(json.load(open(sys.argv[1])))
except Exception:
    pass
def pick(*want):
    for w in want:
        for n in names:
            low = n.lower()
            if low == w or low.endswith("_" + w):
                return n
    return names[0] if names else "-"
print(pick("freya", "eva", "emma"), pick("easton", "ethan", "adam"), len(names))
EOF
)
FEMALE=$(echo "$VOICES" | cut -d' ' -f1)
MALE=$(echo "$VOICES" | cut -d' ' -f2)
[ "$FEMALE" != - ] || FEMALE=
[ "$MALE" != - ] || MALE=
echo "    model id: ${REQ_MODEL:-none}; $(echo "$VOICES" | cut -d' ' -f3) voices; female ${FEMALE:-default}, male ${MALE:-default}"
t0=$(date +%s%N)
if ! speak "$WORK/online.pcm" "$FEMALE" 120 2> "$WORK/speak.err"; then
    cat "$WORK/speak.err" >&2
    echo '    the voice name was refused; using the default voice'
    FEMALE=
    MALE=
    t0=$(date +%s%N)
    speak "$WORK/online.pcm" "" 120
fi
t1=$(date +%s%N)
pcm_ok "$WORK/online.pcm"
if [ -n "$MALE" ] && ! speak "$WORK/online-male.pcm" "$MALE" 120 2> /dev/null; then
    echo "    the male voice $MALE was refused; Juka uses the female one for both"
    MALE=
fi
CI_RTF=$(python3 -c "import os; n = os.path.getsize('$WORK/online.pcm') / 2 / 24000.0; print('%.3f' % (($t1 - $t0) / 1e9 / max(n, 0.1)))")
RSS_KB=$(awk '/^VmHWM:/ { print $2 }' "/proc/$SERVER_PID/status" 2> /dev/null || echo 0)
RSS_MB=$(( (${RSS_KB:-0} + 1023) / 1024 ))
echo "    runner real-time factor $CI_RTF, peak memory $RSS_MB MB"
stop_server

# The offline package: the model's own folder first (--model DIR, the form
# the server's README documents); if the server still wants something from
# the internet with only that, the parts of the Hugging Face cache it filled
# (HF_HOME). Whichever speaks under qemu's Cortex-A53 with no network is what
# ships, and voice.json records its arguments.
phase "offline smoke run on a Cortex-A53, no network"
SNAP=$(find "$HF" -type d -path '*models--gradium--phonon-31m-en/snapshots/*' -prune | head -n 1)
if [ -z "$SNAP" ]; then
    SNAP=$(find "$HF" -name config.json -path '*/snapshots/*' -exec dirname {} \; | head -n 1)
fi
if [ -z "$SNAP" ]; then
    find "$HF" -maxdepth 4 >&2
    echo 'the model did not land in the Hugging Face cache where expected' >&2
    exit 1
fi
echo "    model snapshot: ${SNAP#"$HF"/}"
# offline_run NAME: one smoke run inside a namespace of its own.
offline_run() {
    WORK="$WORK" PKG="$PKG" QEMU="$QEMU" FEMALE="$FEMALE" REQ_MODEL="$REQ_MODEL" \
        OFF_ENV="$OFF_ENV" OFF_ARGS="$OFF_ARGS" \
        timeout -k 10 2100 unshare -n sh "$0" offline-inner "$1"
}
MODE=
mkdir -p "$PKG/model" "$WORK/empty-hf"
cp -rL "$SNAP/." "$PKG/model/"
OFF_ENV="HF_HOME=$WORK/empty-hf HF_HUB_OFFLINE=1"
OFF_ARGS="--model $PKG/model --quant q8 --lang en"
if offline_run model; then
    MODE=model
else
    echo '    --model with the snapshot folder was not enough; trying the cache'
    rm -rf "$PKG/model"
    for repo in "$HF"/hub/models--*; do
        [ -d "$repo/snapshots" ] || continue
        name=$(basename "$repo")
        mkdir -p "$PKG/hf/hub/$name"
        if [ -d "$repo/refs" ]; then
            cp -r "$repo/refs" "$PKG/hf/hub/$name/"
        fi
        # Files, not links into blobs/: the zip carries no symbolic links.
        cp -rL "$repo/snapshots" "$PKG/hf/hub/$name/"
    done
    OFF_ENV="HF_HOME=$PKG/hf HF_HUB_OFFLINE=1"
    OFF_ARGS="--quant q8 --lang en"
    if offline_run cache; then
        MODE=cache
    fi
fi
if [ -z "$MODE" ]; then
    echo 'the server did not speak offline on a Cortex-A53 in either form' >&2
    exit 1
fi
echo "    offline form: $MODE"

phase "licences and voice.json"
for lic in "$SRC"/LICENSE*; do
    [ -f "$lic" ] || continue
    install -m 644 "$lic" "$PKG/PTTS_$(basename "$lic" .txt).txt"
done
fetch 'https://www.apache.org/licenses/LICENSE-2.0.txt' "$PKG/MODEL_LICENSE.txt" 120
printf '%s  %s\n' "$APACHE_SHA" "$PKG/MODEL_LICENSE.txt" | sha256sum -c -
# {dir} is the installed folder and {tmp} a private scratch folder; JukaHub
# adds the address flag and a port itself.
python3 - "$PKG/voice.json" "$MODE" "$REQ_MODEL" "$FEMALE" "$MALE" "$RSS_MB" "$CI_RTF" \
    "$PTTS_TAG" << 'EOF'
import json, sys
out, mode, model, female, male, rss, rtf, tag = sys.argv[1:]
if mode == "model":
    args = ["--model", "{dir}/model", "--quant", "q8", "--lang", "en"]
    env = {"HF_HOME": "{tmp}", "HF_HUB_OFFLINE": "1"}
else:
    args = ["--quant", "q8", "--lang", "en"]
    env = {"HF_HOME": "{dir}/hf", "HF_HUB_OFFLINE": "1"}
json.dump({
    "engine": "phonon",
    "name": "Phonon",
    "version": tag,
    "server": "juka-voice-server",
    "args": args,
    "env": env,
    "addr_flag": "--addr",
    "model": model,
    "format": "pcm",
    "rate": 24000,
    "female": female,
    "male": male,
    "rss_mb": int(rss or 0),
    "ci_rtf": float(rtf or 0),
}, open(out, "w"), indent=1)
EOF
cat "$PKG/voice.json"

phase "install"
OUT=/src/player/native/bin/juka-voice
rm -rf "$OUT"
mkdir -p "$OUT"
(cd "$WORK/pkg" && tar -cf "$OUT/juka-voice.tar" --owner=0 --group=0 --sort=name juka-voice)
{
    printf 'xn-ptts %s %s\n' "$PTTS_TAG" "$PTTS_COMMIT"
    printf 'rustc %s\n' "$RUSTC_VERSION"
    printf 'target-cpu cortex-a53\n'
    printf 'offline form %s\n' "$MODE"
    printf 'runner rtf %s\n' "$CI_RTF"
    printf 'peak memory mb %s\n' "$RSS_MB"
    printf 'tar sha256 %s\n' "$(sha256sum "$OUT/juka-voice.tar" | cut -d' ' -f1)"
} > "$OUT/BUILDINFO.txt"
cat "$OUT/BUILDINFO.txt"
phase "done"
