#!/bin/sh
# Build JukaHub's Remote Play helpers from chiaki-ng's libchiaki.
#
# These two binaries are the only native pieces of the PS Remote Play client:
#   chiaki-stream  SDL2 + FFmpeg streamer, driven by a session file
#   chiaki-regist  PS5 pairing handshake (PS4 pairing is Go, in ../psremote_ps4.go)
#
# They are built from the upstream chiaki-ng library sources, pinned to a commit,
# and installed into ./bin next to this script (player/native/bin). That is
# where release.sh, package-sdcard.sh and the release workflows pick them up, so
# every TrimUI package carries them as Apps/JukaHub/bin/. Nothing here depends
# on the trimui-chiaki-ng port.
#
# Two ways to build them:
#
#   Native aarch64 — what the release does (JUKAHUB_HELPERS_NATIVE=1)
#       Runs ON an aarch64 Linux machine whose glibc is no newer than the
#       handheld's: the Smart Pro has glibc 2.33 since firmware 1.0.4, and a
#       binary linked against a newer glibc does not even start there. The
#       release builds inside an Ubuntu 20.04 (glibc 2.31) arm64 root, see
#       .github/workflows/remote-play-helpers.yml. FFmpeg (the H.264 and HEVC
#       decoders only), opus and json-c are built from pinned, checksum-verified
#       sources into DEPS_PREFIX and linked statically, OpenSSL statically from
#       the distribution (libssl-dev), so the binaries need nothing from the
#       handheld but its C library and its SDL2 — the firmware's SDL2 is the
#       one that knows the display, and it must stay dynamic. They link against
#       SDL2 2.26.1, the version the TrimUI firmware ships, built here only for
#       its headers and soname. The result is verified: no other shared library
#       may be NEEDED, no symbol may need a glibc newer than GLIBC_MAX, and both
#       binaries must start (no arguments -> exit 2, missing input -> exit 3).
#       The binaries are staged in BUILD_DIR and reach ./bin only once every
#       check has passed.
#
#   TG5050 SDK cross build (the default)
#       SDK_ROOT=/opt/sdk-tg5050 ./build-helpers.sh
#       Links the SDK sysroot's FFmpeg/OpenSSL dynamically, so the result needs
#       the matching firmware libraries (Smart Pro S) or libs/brick-stock.
#
# Requirements (both): cmake (>= 3.24 recommended), ninja, git, python3 with
# grpcio-tools and protobuf (nanopb's protoc wrapper) — the exact versions the
# release uses are in helper-build-requirements.txt next to this script. Native
# mode also needs a C compiler, make, pkg-config, curl or wget, xz, sha256sum,
# readelf/objdump and OpenSSL's static libraries (libssl-dev); patchelf is used
# when present.
#
# Usage: ./build-helpers.sh
#        JOBS=4 ./build-helpers.sh
#        JUKAHUB_HELPERS_NATIVE=1 DEPS_PREFIX=/opt/jukahub-deps ./build-helpers.sh

set -eu

NATIVE_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
NATIVE_MODE="${JUKAHUB_HELPERS_NATIVE:-0}"
# chiaki-ng moved from streetpea/ to its own organisation; the old URL only
# redirects. The commit below is fetched and checked by its full id either way.
CHIAKI_REPO="${CHIAKI_REPO:-https://github.com/chiaki-ng/chiaki-ng.git}"
CHIAKI_SOURCE="${CHIAKI_SOURCE:-/tmp/chiaki-ng-upstream}"
CHIAKI_COMMIT="${CHIAKI_COMMIT:-a9a2805884cfa83865fdfcc09ca3ddfcd628aa42}"
BUILD_DIR="${BUILD_DIR:-/tmp/jukahub-chiaki-build}"
MINIUPNPC_SOURCE="${MINIUPNPC_SOURCE:-/tmp/miniupnp-src}"
MINIUPNPC_REPO="${MINIUPNPC_REPO:-https://github.com/miniupnp/miniupnp.git}"
MINIUPNPC_TAG="${MINIUPNPC_TAG:-miniupnpc_2_2_8}"
# The full id of the commit that tag names ("miniupnpc version 2.2.8"). A tag
# can be moved and a short prefix can be forged cheaply, so the source is
# fetched by this id, the way chiaki-ng's is, and checked against all of it.
MINIUPNPC_COMMIT="${MINIUPNPC_COMMIT:-b55145ec095652289a59c33603f3abafee898273}"
OUTPUT_BIN="${OUTPUT_BIN:-$NATIVE_DIR/bin}"
PYTHON="${PYTHON:-$(command -v python3 || echo /usr/bin/python3)}"
if [ -z "${JOBS:-}" ]; then
    JOBS="$(nproc 2>/dev/null || echo 4)"
fi

# Native mode: where the static dependencies are built and installed.
DEPS_PREFIX="${DEPS_PREFIX:-/opt/jukahub-deps}"
DEPS_SRC="${DEPS_SRC:-/tmp/jukahub-deps-src}"
GLIBC_MAX="${GLIBC_MAX:-2.31}"

# Pinned dependency sources. Each digest was published by, or cross-checked
# against, the project or a distribution that verified the release signature;
# fetch() refuses anything else. Bump a version and its digest together.
SDL2_VERSION=2.26.1
SDL2_SHA256=02537cc7ebd74071631038b237ec4bfbb3f4830ba019e569434da33f42373e04
FFMPEG_VERSION=6.1.6
FFMPEG_SHA256=d4fcb164028dd3beee5d92c0ac72e46aac6973c75ea12dc14de07bf8f407370a
OPUS_VERSION=1.5.2
OPUS_SHA256=65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1
JSONC_VERSION=0.19
JSONC_SHA256=37ad0249902e301bd9052bf712e511fcc6acff4ecaad4b5900aad9ce564e26de

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'build-helpers: %s\n' "$*" >&2; exit 1; }

# sha256_ok FILE SHA256
sha256_ok() {
    printf '%s  %s\n' "$2" "$1" | sha256sum -c - >/dev/null 2>&1
}

# fetch FILE SHA256 URL... downloads FILE into DEPS_SRC from the first URL whose
# content has exactly that digest. A cached copy is reused only if it matches.
fetch() {
    file="$1"
    sum="$2"
    shift 2
    dest="$DEPS_SRC/$file"
    if [ -f "$dest" ] && sha256_ok "$dest" "$sum"; then
        return 0
    fi
    for url in "$@"; do
        rm -f "$dest.part"
        echo "fetching $url"
        # Bounded: a stalled mirror must cost a retry or the next URL, not the
        # job's whole timeout.
        if command -v curl >/dev/null 2>&1; then
            curl -fsSL --retry 3 --retry-delay 5 --connect-timeout 30 --max-time 900 \
                -o "$dest.part" "$url" || continue
        else
            wget -q --tries=3 --timeout=30 -O "$dest.part" "$url" || continue
        fi
        if sha256_ok "$dest.part" "$sum"; then
            mv "$dest.part" "$dest"
            return 0
        fi
        echo "checksum mismatch for $url" >&2
    done
    rm -f "$dest.part"
    die "could not fetch $file with sha256 $sum"
}

# ── Native mode: static dependencies ─────────────────────────────────────

build_sdl2() {
    # Only the headers and libSDL2-2.0.so.0's soname are used: the handheld
    # loads its own SDL2 at run time. So no video or audio backend matters, and
    # no rpath may point into DEPS_PREFIX.
    if [ -f "$DEPS_PREFIX/lib/pkgconfig/sdl2.pc" ]; then
        return 0
    fi
    say "SDL2 $SDL2_VERSION (link target only)"
    fetch "SDL2-$SDL2_VERSION.tar.gz" "$SDL2_SHA256" \
        "https://www.libsdl.org/release/SDL2-$SDL2_VERSION.tar.gz" \
        "https://github.com/libsdl-org/SDL/releases/download/release-$SDL2_VERSION/SDL2-$SDL2_VERSION.tar.gz"
    rm -rf "$DEPS_SRC/SDL2-$SDL2_VERSION" "$DEPS_SRC/sdl2-build"
    tar -xzf "$DEPS_SRC/SDL2-$SDL2_VERSION.tar.gz" -C "$DEPS_SRC"
    cmake -S "$DEPS_SRC/SDL2-$SDL2_VERSION" -B "$DEPS_SRC/sdl2-build" -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$DEPS_PREFIX" \
        -DCMAKE_INSTALL_LIBDIR=lib \
        -DSDL_SHARED=ON -DSDL_STATIC=OFF -DSDL_TEST=OFF -DSDL_RPATH=OFF
    cmake --build "$DEPS_SRC/sdl2-build" -j "$JOBS"
    cmake --install "$DEPS_SRC/sdl2-build"
}

build_opus() {
    if [ -f "$DEPS_PREFIX/lib/pkgconfig/opus.pc" ]; then
        return 0
    fi
    say "opus $OPUS_VERSION (static)"
    fetch "opus-$OPUS_VERSION.tar.gz" "$OPUS_SHA256" \
        "https://downloads.xiph.org/releases/opus/opus-$OPUS_VERSION.tar.gz" \
        "https://ftp.osuosl.org/pub/xiph/releases/opus/opus-$OPUS_VERSION.tar.gz"
    rm -rf "$DEPS_SRC/opus-$OPUS_VERSION"
    tar -xzf "$DEPS_SRC/opus-$OPUS_VERSION.tar.gz" -C "$DEPS_SRC"
    (
        cd "$DEPS_SRC/opus-$OPUS_VERSION"
        ./configure --prefix="$DEPS_PREFIX" --libdir="$DEPS_PREFIX/lib" \
            --enable-static --disable-shared --with-pic \
            --disable-doc --disable-extra-programs
        make -j "$JOBS"
        make install
    )
}

build_ffmpeg() {
    # Decoders and swscale only: the helper decodes the console's H.264 or HEVC
    # stream and converts odd pixel formats, nothing else. --disable-autodetect
    # keeps every optional system library (zlib, VA-API, X11...) out, so the
    # static archives pull in nothing the handheld might lack.
    if [ -f "$DEPS_PREFIX/lib/pkgconfig/libavcodec.pc" ]; then
        return 0
    fi
    say "FFmpeg $FFMPEG_VERSION (static, H.264 + HEVC decoders, swscale)"
    fetch "ffmpeg-$FFMPEG_VERSION.tar.xz" "$FFMPEG_SHA256" \
        "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz" \
        "https://www.ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz"
    rm -rf "$DEPS_SRC/ffmpeg-$FFMPEG_VERSION"
    tar -xJf "$DEPS_SRC/ffmpeg-$FFMPEG_VERSION.tar.xz" -C "$DEPS_SRC"
    (
        cd "$DEPS_SRC/ffmpeg-$FFMPEG_VERSION"
        ./configure --prefix="$DEPS_PREFIX" --libdir="$DEPS_PREFIX/lib" \
            --enable-static --disable-shared --enable-pic \
            --disable-programs --disable-doc --disable-debug \
            --disable-autodetect --disable-network --disable-everything \
            --disable-avdevice --disable-avformat --disable-avfilter \
            --disable-swresample --disable-postproc \
            --enable-decoder=h264 --enable-decoder=hevc \
            --enable-parser=h264 --enable-parser=hevc \
            --enable-pthreads
        make -j "$JOBS"
        make install
    )
}

build_jsonc() {
    # chiaki-lib's PSN code needs json-c. Built with CMake so both a
    # find_package(json-c CONFIG) and a pkg-config lookup find the static one.
    if [ -f "$DEPS_PREFIX/lib/pkgconfig/json-c.pc" ]; then
        return 0
    fi
    say "json-c $JSONC_VERSION (static)"
    fetch "json-c-$JSONC_VERSION.tar.gz" "$JSONC_SHA256" \
        "https://s3.amazonaws.com/json-c_releases/releases/json-c-$JSONC_VERSION.tar.gz"
    rm -rf "$DEPS_SRC/json-c-$JSONC_VERSION" "$DEPS_SRC/json-c-build"
    tar -xzf "$DEPS_SRC/json-c-$JSONC_VERSION.tar.gz" -C "$DEPS_SRC"
    cmake -S "$DEPS_SRC/json-c-$JSONC_VERSION" -B "$DEPS_SRC/json-c-build" -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$DEPS_PREFIX" \
        -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
        -DBUILD_SHARED_LIBS=OFF -DBUILD_STATIC_LIBS=ON \
        -DBUILD_TESTING=OFF -DBUILD_APPS=OFF -DDISABLE_WERROR=ON
    cmake --build "$DEPS_SRC/json-c-build" -j "$JOBS"
    cmake --install "$DEPS_SRC/json-c-build"
}

# ── Native mode: verification ────────────────────────────────────────────

# version_le A B: true when version A <= B.
version_le() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$1" ]
}

# glibc_max FILE prints the newest GLIBC_x.y symbol version FILE needs.
glibc_max() {
    objdump -T "$1" | grep -o 'GLIBC_[0-9][0-9.]*' | sed 's/GLIBC_//' | sort -uV | tail -n 1
}

needed_libs() {
    readelf -d "$1" | sed -n 's/.*(NEEDED).*\[\(.*\)\].*/\1/p'
}

# expect_exit CODE BINARY ARGS... runs BINARY with every symbol bound up front,
# so a missing one fails here instead of on the handheld.
expect_exit() {
    want="$1"
    shift
    rc=0
    LD_BIND_NOW=1 LD_LIBRARY_PATH="$DEPS_PREFIX/lib" "$@" >/dev/null 2>&1 || rc=$?
    if [ "$rc" != "$want" ]; then
        die "smoke test: '$*' exited with $rc, expected $want"
    fi
    echo "smoke test: '$*' -> $rc"
}

# verify_native checks the staged binaries in STAGE_DIR and writes
# STAGE_DIR/BUILDINFO.txt; only a build that passes is copied into OUTPUT_BIN.
verify_native() {
    say "Verifying the helpers"
    report="$STAGE_DIR/BUILDINFO.txt"
    {
        echo "JukaHub Remote Play helpers"
        echo "built:      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
        echo "chiaki-ng:  $CHIAKI_REPO @ $CHIAKI_COMMIT"
        echo "miniupnpc:  $MINIUPNPC_TAG @ $(git -C "$MINIUPNPC_SOURCE" rev-parse HEAD 2>/dev/null || echo unknown)"
        echo "FFmpeg:     $FFMPEG_VERSION (static: h264, hevc, swscale)"
        echo "opus:       $OPUS_VERSION (static)"
        echo "json-c:     $JSONC_VERSION (static)"
        echo "OpenSSL:    $(pkg-config --modversion openssl 2>/dev/null || echo unknown) (static, distribution)"
        echo "curl:       chiaki-ng's third-party/curl submodule, static"
        echo "SDL2:       $SDL2_VERSION headers; the handheld's own libSDL2-2.0.so.0 at run time"
        echo "compiler:   $(cc --version 2>/dev/null | head -n 1)"
        echo "build libc: $(ldd --version 2>/dev/null | head -n 1)"
        # Anything chiaki-ng's configure downloaded by itself (FetchContent)
        # shows up here, so an unpinned fetch cannot go unnoticed.
        if [ -d "$BUILD_DIR/_deps" ]; then
            echo "fetched by the configure: $(ls "$BUILD_DIR/_deps" | tr '\n' ' ')"
        fi
    } > "$report"
    for name in chiaki-stream chiaki-regist; do
        bin="$STAGE_DIR/$name"
        if readelf -d "$bin" | grep -Eq '\((RPATH|RUNPATH)\)'; then
            if command -v patchelf >/dev/null 2>&1; then
                patchelf --remove-rpath "$bin"
            else
                die "$name carries an rpath and patchelf is not installed to remove it"
            fi
        fi
        needed="$(needed_libs "$bin" | tr '\n' ' ')"
        for lib in $needed; do
            case "$lib" in
                libSDL2-2.0.so.0|libc.so.6|libm.so.6|libpthread.so.0|libdl.so.2|librt.so.1|libgcc_s.so.1|ld-linux-aarch64.so.1) ;;
                *) die "$name needs $lib, which the handheld may not have: a dependency was linked dynamically" ;;
            esac
        done
        max="$(glibc_max "$bin")"
        if [ -z "$max" ] || ! version_le "$max" "$GLIBC_MAX"; then
            die "$name needs GLIBC_${max:-?}, newer than GLIBC_$GLIBC_MAX"
        fi
        {
            echo ""
            echo "$name"
            echo "  sha256:     $(sha256sum "$bin" | cut -d' ' -f1)"
            echo "  size:       $(wc -c < "$bin" | tr -d ' ') bytes"
            echo "  needs:      $needed"
            echo "  glibc max:  GLIBC_$max (limit GLIBC_$GLIBC_MAX)"
        } >> "$report"
    done
    expect_exit 2 "$STAGE_DIR/chiaki-stream"
    expect_exit 3 "$STAGE_DIR/chiaki-stream" "$DEPS_SRC/no-such-session.conf"
    expect_exit 2 "$STAGE_DIR/chiaki-regist"
    expect_exit 3 "$STAGE_DIR/chiaki-regist" "$DEPS_SRC/no-such-input.conf" "$DEPS_SRC/no-such-result.conf"
    cat "$report"
}

# ── Mode setup ───────────────────────────────────────────────────────────

if [ "$NATIVE_MODE" = "1" ]; then
    case "$(uname -m)" in
        aarch64|arm64) ;;
        *) die "native mode builds on an aarch64 Linux host; use SDK_ROOT=... for a cross build" ;;
    esac
    mkdir -p "$DEPS_PREFIX" "$DEPS_SRC"
    PKG_CONFIG_PATH="$DEPS_PREFIX/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
    export PKG_CONFIG_PATH
    build_sdl2
    build_opus
    build_ffmpeg
    build_jsonc
    STRIP="${STRIP:-strip}"
else
    SDK_ROOT="${SDK_ROOT:-/opt/sdk-tg5050}"
    TOOLCHAIN_FILE="${TOOLCHAIN_FILE:-$NATIVE_DIR/tg5050-toolchain.cmake}"
    if [ ! -x "$SDK_ROOT/host/bin/aarch64-none-linux-gnu-gcc" ]; then
        die "TG5050 toolchain not found at $SDK_ROOT (set SDK_ROOT, or JUKAHUB_HELPERS_NATIVE=1 on an aarch64 host)"
    fi
    # The toolchain file names these fixed paths.
    ln -sfn "$SDK_ROOT" /tmp/tg5050-sdk
    PATH="/tmp/tg5050-sdk/host/bin:$PATH"
    export PATH
    STRIP="${STRIP:-/tmp/tg5050-sdk/host/bin/aarch64-none-linux-gnu-strip}"
fi

# ── chiaki-ng ────────────────────────────────────────────────────────────

say "chiaki-ng $CHIAKI_COMMIT"
if [ ! -d "$CHIAKI_SOURCE/.git" ]; then
    mkdir -p "$CHIAKI_SOURCE"
    git -C "$CHIAKI_SOURCE" init -q
    git -C "$CHIAKI_SOURCE" remote add origin "$CHIAKI_REPO"
fi
# The build edits two of chiaki-ng's files (the hook in the top-level
# CMakeLists.txt, the curl target in lib/CMakeLists.txt); put both back first
# so a reused checkout can move to another commit and never stacks edits.
for tracked in CMakeLists.txt lib/CMakeLists.txt; do
    git -C "$CHIAKI_SOURCE" checkout -q -- "$tracked" 2>/dev/null || true
done
if [ "$(git -C "$CHIAKI_SOURCE" rev-parse HEAD 2>/dev/null || true)" != "$CHIAKI_COMMIT" ]; then
    git -C "$CHIAKI_SOURCE" fetch -q --depth 1 origin "$CHIAKI_COMMIT"
    git -C "$CHIAKI_SOURCE" checkout -q --detach "$CHIAKI_COMMIT"
fi
if [ "$(git -C "$CHIAKI_SOURCE" rev-parse HEAD)" != "$CHIAKI_COMMIT" ]; then
    die "chiaki-ng checkout is not at $CHIAKI_COMMIT"
fi
git -C "$CHIAKI_SOURCE" submodule update --init --depth 1 \
    third-party/curl third-party/nanopb third-party/gf-complete third-party/jerasure

# miniupnpc, fetched by its full commit id like chiaki-ng.
if [ ! -d "$MINIUPNPC_SOURCE/.git" ]; then
    mkdir -p "$MINIUPNPC_SOURCE"
    git -C "$MINIUPNPC_SOURCE" init -q
    git -C "$MINIUPNPC_SOURCE" remote add origin "$MINIUPNPC_REPO"
fi
if [ "$(git -C "$MINIUPNPC_SOURCE" rev-parse HEAD 2>/dev/null || true)" != "$MINIUPNPC_COMMIT" ]; then
    git -C "$MINIUPNPC_SOURCE" fetch -q --depth 1 origin "$MINIUPNPC_COMMIT"
    git -C "$MINIUPNPC_SOURCE" checkout -q --detach "$MINIUPNPC_COMMIT"
fi
if [ "$(git -C "$MINIUPNPC_SOURCE" rev-parse HEAD)" != "$MINIUPNPC_COMMIT" ]; then
    die "miniupnp checkout is not the $MINIUPNPC_TAG commit $MINIUPNPC_COMMIT"
fi

# chiaki-lib links the bundled curl as CURL::libcurl_shared, the target curl's
# CMake defines only when it builds a shared library. This build is static
# (BUILD_SHARED_LIBS=OFF, BUILD_STATIC_LIBS=ON), where curl defines
# libcurl_static instead, so the link names that one — the same one-word
# change distributions make to link chiaki-ng against their own curl.
lib_cmake="$CHIAKI_SOURCE/lib/CMakeLists.txt"
if [ -f "$lib_cmake" ] && grep -q 'libcurl_shared' "$lib_cmake"; then
    sed -i 's/libcurl_shared/libcurl_static/g' "$lib_cmake"
    if grep -q 'libcurl_shared' "$lib_cmake" || ! grep -q 'libcurl_static' "$lib_cmake"; then
        die "could not point chiaki-lib at the static curl target in lib/CMakeLists.txt"
    fi
    echo "chiaki-lib links the bundled curl statically (libcurl_shared -> libcurl_static)"
else
    echo "note: lib/CMakeLists.txt names no libcurl_shared at $CHIAKI_COMMIT; the curl link is left as it is"
fi
# Any other place that still asks for the shared target fails the configure;
# name it here so the log says where to look.
other_shared="$(grep -rln 'libcurl_shared' "$CHIAKI_SOURCE/CMakeLists.txt" "$CHIAKI_SOURCE/lib" \
    "$CHIAKI_SOURCE/cmake" 2>/dev/null || true)"
if [ -n "$other_shared" ]; then
    echo "warning: libcurl_shared is still named in: $other_shared" >&2
fi

# Register the helper targets with chiaki-ng's top-level build.
cat >> "$CHIAKI_SOURCE/CMakeLists.txt" <<'EOF'

if(DEFINED JUKAHUB_NATIVE_SOURCE)
  add_subdirectory("${JUKAHUB_NATIVE_SOURCE}" "${CMAKE_BINARY_DIR}/jukahub-native")
endif()
EOF

# nanopb's protoc wrapper, with LF endings and the exec bit whatever the
# checkout did to it (a Windows checkout gives CRLF, which breaks the shebang).
mkdir -p "$BUILD_DIR"
PROTOC="${PROTOC:-$BUILD_DIR/protoc-wrapper.py}"
if [ "$PROTOC" = "$BUILD_DIR/protoc-wrapper.py" ]; then
    tr -d '\r' < "$NATIVE_DIR/protoc-wrapper.py" > "$PROTOC"
    chmod +x "$PROTOC"
fi
# Generated code from an older nanopb still loads with protobuf 4 in its
# pure-Python implementation.
PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION="${PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION:-python}"
export PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION

# Binaries from an earlier run must not outlive a build that fails, or a
# packaging script would ship them as this build's.
rm -f "$OUTPUT_BIN/chiaki-stream" "$OUTPUT_BIN/chiaki-regist" "$OUTPUT_BIN/BUILDINFO.txt"

say "Configuring"
set -- \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DBUILD_STATIC_LIBS=ON \
    -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DCMAKE_SKIP_RPATH=ON \
    -DPROTOC="$PROTOC" -DPYTHON_EXECUTABLE="$PYTHON" -DPython_EXECUTABLE="$PYTHON" \
    -DPython3_EXECUTABLE="$PYTHON" \
    -DFETCHCONTENT_SOURCE_DIR_MINIUPNPC="$MINIUPNPC_SOURCE" \
    -DJUKAHUB_NATIVE_SOURCE="$NATIVE_DIR" \
    -DCHIAKI_ENABLE_GUI=OFF -DCHIAKI_ENABLE_CLI=OFF -DCHIAKI_ENABLE_TESTS=OFF \
    -DCHIAKI_ENABLE_ANDROID=OFF -DCHIAKI_ENABLE_BOREALIS=OFF \
    -DCHIAKI_ENABLE_SETSU=OFF -DCHIAKI_ENABLE_STEAMDECK_NATIVE=OFF \
    -DCHIAKI_ENABLE_STEAM_SHORTCUT=OFF -DCHIAKI_ENABLE_SPEEX=OFF \
    -DCHIAKI_ENABLE_RUDP=OFF -DCHIAKI_ENABLE_FFMPEG_DECODER=ON \
    -DCHIAKI_ENABLE_PI_DECODER=OFF -DCHIAKI_LIB_ENABLE_OPUS=ON \
    -DCHIAKI_USE_SYSTEM_JERASURE=OFF -DCHIAKI_USE_SYSTEM_NANOPB=OFF \
    -DCHIAKI_USE_SYSTEM_CURL=OFF -DCHIAKI_LIB_MINIUPNPC_EXTERNAL_PROJECT=ON \
    -DCURL_USE_LIBPSL=OFF -DCURL_ZLIB=OFF -DCURL_BROTLI=OFF -DCURL_ZSTD=OFF \
    -DUSE_NGHTTP2=OFF -DUSE_LIBIDN2=OFF -DCURL_USE_LIBSSH2=OFF
if [ "$NATIVE_MODE" = "1" ]; then
    set -- "$@" \
        -DCMAKE_PREFIX_PATH="$DEPS_PREFIX" \
        -DOPENSSL_USE_STATIC_LIBS=TRUE \
        -DJUKAHUB_STATIC_DEPS=ON
else
    set -- "$@" -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE"
fi
cmake -S "$CHIAKI_SOURCE" -B "$BUILD_DIR" -G Ninja "$@"
# Static is the point of this build: a configure that switched shared libraries
# back on would make the helpers need chiaki-ng's own .so files on the handheld.
if grep -Eiq '^BUILD_SHARED_LIBS:BOOL=(ON|1|TRUE|YES|Y)$' "$BUILD_DIR/CMakeCache.txt"; then
    die "chiaki-ng's configure turned BUILD_SHARED_LIBS on; the helpers must be linked statically"
fi
# What the configure settled on, for the log of a first run at a new commit.
grep -E '^(BUILD_SHARED_LIBS|BUILD_STATIC_LIBS|CHIAKI_[A-Z0-9_]+|FETCHCONTENT_[A-Z0-9_]+):' \
    "$BUILD_DIR/CMakeCache.txt" || true

say "Building"
cmake --build "$BUILD_DIR" --target chiaki-stream chiaki-regist -j "$JOBS"

# Staged in the build directory and checked there: only binaries that passed
# reach OUTPUT_BIN, where the packaging scripts ship whatever they find.
STAGE_DIR="$BUILD_DIR/jukahub-stage"
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR"
for name in chiaki-stream chiaki-regist; do
    cp "$BUILD_DIR/jukahub-native/$name" "$STAGE_DIR/$name"
    "$STRIP" --strip-unneeded "$STAGE_DIR/$name"
    chmod 755 "$STAGE_DIR/$name"
done

if [ "$NATIVE_MODE" = "1" ]; then
    verify_native
fi

mkdir -p "$OUTPUT_BIN"
for name in chiaki-stream chiaki-regist BUILDINFO.txt; do
    if [ -f "$STAGE_DIR/$name" ]; then
        cp "$STAGE_DIR/$name" "$OUTPUT_BIN/$name"
    fi
done
chmod 755 "$OUTPUT_BIN/chiaki-stream" "$OUTPUT_BIN/chiaki-regist"

echo "Installed helpers into $OUTPUT_BIN"
ls -l "$OUTPUT_BIN"
