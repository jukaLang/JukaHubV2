#!/bin/sh
# Build JukaHub's Remote Play helpers from chiaki-ng's libchiaki.
#
# These two binaries are the only native pieces of the PS Remote Play client:
#   chiaki-stream  SDL2 + FFmpeg streamer, driven by a session file
#   chiaki-regist  PS5 pairing handshake (PS4 pairing is Go, in ../psremote_ps4.go)
#
# They are built from the upstream chiaki-ng library sources, pinned to a commit,
# and installed into ../bin so release.sh can package them. Nothing here depends
# on the trimui-chiaki-ng port.
#
# Requirements:
#   SDK_ROOT        TG5050 aarch64 toolchain (default /opt/sdk-tg5050)
#   cmake, ninja    host build tools
#   python3         for nanopb's protoc wrapper
#   git             to fetch the pinned chiaki-ng commit
#
# Usage: ./build-helpers.sh            (bin/ is written next to this script)
#        JOBS=4 ./build-helpers.sh

set -eu

NATIVE_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PLAYER_DIR="$(dirname "$NATIVE_DIR")"
SDK_ROOT="${SDK_ROOT:-/opt/sdk-tg5050}"
CHIAKI_SOURCE="${CHIAKI_SOURCE:-/tmp/chiaki-ng-upstream}"
CHIAKI_COMMIT="${CHIAKI_COMMIT:-a9a2805884cfa83865fdfcc09ca3ddfcd628aa42}"
BUILD_DIR="${BUILD_DIR:-/tmp/jukahub-chiaki-build}"
TOOLCHAIN_FILE="${TOOLCHAIN_FILE:-$NATIVE_DIR/tg5050-toolchain.cmake}"
MINIUPNPC_SOURCE="${MINIUPNPC_SOURCE:-/tmp/miniupnp-src}"
PROTOC="${PROTOC:-$NATIVE_DIR/protoc-wrapper.py}"
OUTPUT_BIN="$PLAYER_DIR/bin"

if [ ! -x "$SDK_ROOT/host/bin/aarch64-none-linux-gnu-gcc" ]; then
    echo "TG5050 toolchain not found at $SDK_ROOT (set SDK_ROOT)" >&2
    exit 1
fi

if [ ! -d "$CHIAKI_SOURCE/.git" ]; then
    git clone https://github.com/streetpea/chiaki-ng.git "$CHIAKI_SOURCE"
fi
if [ "$(git -C "$CHIAKI_SOURCE" rev-parse HEAD 2>/dev/null || true)" != "$CHIAKI_COMMIT" ]; then
    git -C "$CHIAKI_SOURCE" fetch origin "$CHIAKI_COMMIT" --depth 1
    git -C "$CHIAKI_SOURCE" checkout --detach "$CHIAKI_COMMIT"
fi
if [ ! -d "$MINIUPNPC_SOURCE/.git" ]; then
    git clone --depth 1 --branch miniupnpc_2_2_8 https://github.com/miniupnp/miniupnp.git "$MINIUPNPC_SOURCE"
fi
git -C "$CHIAKI_SOURCE" submodule update --init --depth 1 \
    third-party/curl third-party/nanopb third-party/gf-complete third-party/jerasure

# Register the helper targets with chiaki-ng's top-level build. The hook is
# idempotent so a reused checkout does not accumulate copies.
if ! grep -q 'JUKAHUB_NATIVE_SOURCE' "$CHIAKI_SOURCE/CMakeLists.txt"; then
    cat >> "$CHIAKI_SOURCE/CMakeLists.txt" <<'EOF'

if(DEFINED JUKAHUB_NATIVE_SOURCE)
  add_subdirectory("${JUKAHUB_NATIVE_SOURCE}" "${CMAKE_BINARY_DIR}/jukahub-native")
endif()
EOF
fi

ln -sfn "$SDK_ROOT" /tmp/tg5050-sdk
ln -sfn "$CHIAKI_SOURCE" /tmp/chiaki-src
PATH="/tmp:/tmp/tg5050-sdk/host/bin:/usr/local/bin:/usr/bin:/bin"
export PATH

cmake -S /tmp/chiaki-src -B "$BUILD_DIR" -G Ninja \
    -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
    -DPROTOC="$PROTOC" -DPYTHON_EXECUTABLE=/usr/bin/python3 -DPython_EXECUTABLE=/usr/bin/python3 \
    -DFETCHCONTENT_SOURCE_DIR_MINIUPNPC="$MINIUPNPC_SOURCE" \
    -DJUKAHUB_NATIVE_SOURCE="$NATIVE_DIR" \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    -DCHIAKI_ENABLE_GUI=OFF -DCHIAKI_ENABLE_CLI=OFF -DCHIAKI_ENABLE_TESTS=OFF \
    -DCHIAKI_ENABLE_ANDROID=OFF -DCHIAKI_ENABLE_BOREALIS=OFF \
    -DCHIAKI_ENABLE_SETSU=OFF -DCHIAKI_ENABLE_STEAMDECK_NATIVE=OFF \
    -DCHIAKI_ENABLE_STEAM_SHORTCUT=OFF -DCHIAKI_ENABLE_SPEEX=OFF \
    -DCHIAKI_ENABLE_RUDP=OFF -DCHIAKI_ENABLE_FFMPEG_DECODER=ON \
    -DCHIAKI_ENABLE_PI_DECODER=OFF -DCHIAKI_LIB_ENABLE_OPUS=ON \
    -DCHIAKI_USE_SYSTEM_JERASURE=OFF -DCHIAKI_USE_SYSTEM_NANOPB=OFF \
    -DCHIAKI_USE_SYSTEM_CURL=OFF -DCHIAKI_LIB_MINIUPNPC_EXTERNAL_PROJECT=ON
cmake --build "$BUILD_DIR" --target chiaki-stream chiaki-regist -j "${JOBS:-8}"

"/tmp/tg5050-sdk/host/bin/aarch64-none-linux-gnu-strip" --strip-unneeded \
    "$BUILD_DIR/jukahub-native/chiaki-stream" \
    "$BUILD_DIR/jukahub-native/chiaki-regist"

mkdir -p "$OUTPUT_BIN"
cp "$BUILD_DIR/jukahub-native/chiaki-stream" "$OUTPUT_BIN/chiaki-stream"
cp "$BUILD_DIR/jukahub-native/chiaki-regist" "$OUTPUT_BIN/chiaki-regist"
chmod +x "$OUTPUT_BIN/chiaki-stream" "$OUTPUT_BIN/chiaki-regist"

echo "Installed helpers into $OUTPUT_BIN"
ls -l "$OUTPUT_BIN"
