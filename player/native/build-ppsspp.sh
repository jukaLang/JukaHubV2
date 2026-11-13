#!/bin/sh
# Build JukaHub's own PPSSPP for the A133P TrimUI handhelds (Smart Pro, Brick,
# Hammer: "tg5040", PowerVR GE8300) from an upstream PPSSPP release tag.
#
# Why: the PPSSPP JukaHub installs for PSP games comes from ben16w/minui-psp,
# which packages spruceUI's builds, and those lag upstream (1.20.3 while
# hrydgard/ppsspp is at 1.20.4; CrossMix noted in October 2026 that 1.20.4 has
# no tg5040 binary at all). This script builds the newest upstream release
# itself; .github/workflows/ppsspp-trimui.yml runs it weekly and publishes the
# result as the rolling release "ppsspp-tg5040" of this repository, and
# ppsspp_managed.go installs whichever of the two sources names the newer
# PPSSPP (same verification, same launcher, same fallback to the firmware's
# PPSSPP when a build does not start).
#
# It runs as root inside the Ubuntu 20.04 arm64 build root
# (.github/scripts/build-root.sh build ppsspp), native, with network:
#
#   PPSSPP_TAG   the upstream tag, e.g. v1.20.4 (required)
#   GL_DIR       optional: the TrimUI SDK's own GLES/EGL libraries in
#                GL_DIR/lib, copied out of the toolchain image by the workflow.
#                Linking against them gives the binary exactly the sonames the
#                handheld has. Without them it links against Mesa's
#                (libGLESv2.so.2, libEGL.so.1) and says so. The headers are
#                always Mesa's (Khronos's, the same on both).
#   OUT_DIR      where PPSSPP-tg5040.zip and ppsspp-version.txt go
#   JOBS, LOG_DIR
#
# What the handheld provides at run time and is therefore only linked against,
# never bundled: its C library (glibc 2.33 on current firmware; the build root
# has 2.31), its SDL2 (2.26.1, the one that knows the display; built here as a
# link target only, like the Remote Play helpers do), its GLES/EGL and zlib.
# What is bundled: libstdc++.so.6 from the root's gcc 9 (the firmware's is
# older), the way minui-psp 6.1.0 bundles one.
#
# The package mirrors what ppsspp_managed.go's ppssppDiscoverLayout looks for:
#   PPSSPP/PPSSPPSDL_tg5040   the binary
#   PPSSPP/assets/            PPSSPP's assets (flash0, atlases, compat.ini,
#                             gamecontrollerdb.txt)
#   PPSSPP/lib/               bundled libraries
#   PPSSPP/BUILDINFO.txt      tag, commit, compiler, GL source, NEEDED list
set -eu

PPSSPP_TAG="${PPSSPP_TAG:?set PPSSPP_TAG to an upstream tag such as v1.20.4}"
OUT_DIR="${OUT_DIR:-/src/player/native/bin}"
LOG_DIR="${LOG_DIR:-/tmp/jukahub-ppsspp-logs}"
GL_DIR="${GL_DIR:-}"
WORK="${WORK:-/tmp/jukahub-ppsspp}"
if [ -z "${JOBS:-}" ]; then
    JOBS="$(nproc 2> /dev/null || echo 4)"
fi
PLATFORM=tg5040
# The newest glibc the binary may ask for: the Smart Pro's since firmware 1.0.4.
GLIBC_MAX=2.33

SDL2_VERSION=2.26.1
SDL2_SHA256=02537cc7ebd74071631038b237ec4bfbb3f4830ba019e569434da33f42373e04

say() { printf 'build-ppsspp: %s\n' "$*"; }
die() {
    printf 'build-ppsspp: %s\n' "$*" >&2
    exit 1
}

case "$PPSSPP_TAG" in
    v[0-9]*.[0-9]*) ;;
    *) die "PPSSPP_TAG must look like v1.20.4, not '$PPSSPP_TAG'" ;;
esac
VERSION="${PPSSPP_TAG#v}"

mkdir -p "$LOG_DIR" "$OUT_DIR" "$WORK"

# quietly NAME CMD...: output to LOG_DIR/NAME.log, one line in the job log, the
# end of the log on failure.
quietly() {
    q_name="$1"
    shift
    q_log="$LOG_DIR/$q_name.log"
    if "$@" > "$q_log" 2>&1; then
        say "$q_name: ok"
        return 0
    fi
    echo "::group::$q_name (last 80 lines)"
    tail -n 80 "$q_log"
    echo "::endgroup::"
    die "$q_name failed (full log: $q_log)"
}

# version_le A B: A <= B for dotted version numbers.
version_le() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)" = "$2" ]
}

# ── Packages the build needs beyond the root's own ───────────────────────────

# Mesa's GLES/EGL development files give the headers in every case (the SDK
# copy may carry libraries only) and the fallback link target. zlib: PPSSPP
# links the system one, which the firmware has.
quietly apt-packages apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends \
    libgles2-mesa-dev libegl1-mesa-dev zlib1g-dev
# gcc 10 from 20.04's universe: PPSSPP 1.20 is developed against far newer
# compilers and gcc 9's C++17 library lacks parts of it. The root's
# libstdc++6 is gcc 10's already, so the bundled copy is the same either way.
if apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends gcc-10 g++-10 \
    > "$LOG_DIR/gcc-10.log" 2>&1; then
    CC=gcc-10 CXX=g++-10
else
    tail -n 20 "$LOG_DIR/gcc-10.log"
    echo "::warning title=PPSSPP::gcc-10 could not be installed; building with gcc 9"
    CC=gcc CXX=g++
fi
export CC CXX
say "compiler: $($CXX --version | sed -n 1p)"

# ── SDL2: link target only ───────────────────────────────────────────────────

SDL_PREFIX="$WORK/sdl2"
if [ ! -f "$SDL_PREFIX/lib/cmake/SDL2/SDL2Config.cmake" ]; then
    sdl_tar="$WORK/SDL2-$SDL2_VERSION.tar.gz"
    rm -f "$sdl_tar"
    for url in "https://www.libsdl.org/release/SDL2-$SDL2_VERSION.tar.gz" \
        "https://github.com/libsdl-org/SDL/releases/download/release-$SDL2_VERSION/SDL2-$SDL2_VERSION.tar.gz"; do
        if curl -fsSL --retry 3 --retry-delay 5 --connect-timeout 30 --max-time 900 -o "$sdl_tar" "$url" &&
            printf '%s  %s\n' "$SDL2_SHA256" "$sdl_tar" | sha256sum -c - > /dev/null 2>&1; then
            break
        fi
        rm -f "$sdl_tar"
    done
    [ -f "$sdl_tar" ] || die "could not fetch SDL2 $SDL2_VERSION with sha256 $SDL2_SHA256"
    rm -rf "$WORK/SDL2-$SDL2_VERSION" "$WORK/sdl2-build"
    tar -xzf "$sdl_tar" -C "$WORK"
    quietly sdl2-configure cmake -S "$WORK/SDL2-$SDL2_VERSION" -B "$WORK/sdl2-build" -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$SDL_PREFIX" -DCMAKE_INSTALL_LIBDIR=lib \
        -DSDL_SHARED=ON -DSDL_STATIC=OFF -DSDL_TEST=OFF -DSDL_RPATH=OFF
    quietly sdl2-build cmake --build "$WORK/sdl2-build" -j "$JOBS"
    quietly sdl2-install cmake --install "$WORK/sdl2-build"
fi

# ── GLES/EGL link targets ────────────────────────────────────────────────────

# LINK_DIR holds libGLESv2.so and libEGL.so for the linker: the SDK's (any
# libGLESv2.so* found, linked under the development name) or nothing, in which
# case the linker finds Mesa's in the system path.
LINK_DIR="$WORK/gl-link"
rm -rf "$LINK_DIR"
mkdir -p "$LINK_DIR"
GL_SOURCE="Mesa (no TrimUI SDK libraries were provided)"
if [ -n "$GL_DIR" ] && [ -d "$GL_DIR/lib" ]; then
    found=0
    for lib in GLESv2 EGL; do
        cand=$(find "$GL_DIR/lib" -maxdepth 1 \( -type f -o -type l \) -name "lib$lib.so*" 2> /dev/null | sort | head -n 1)
        if [ -n "$cand" ]; then
            ln -sf "$(readlink -f "$cand")" "$LINK_DIR/lib$lib.so"
            found=$((found + 1))
        fi
    done
    # The vendor libraries' own dependencies (libsrv_um, libIMGegl…) are there
    # too; the linker may want to read them.
    if [ "$found" -eq 2 ]; then
        GL_SOURCE="TrimUI SDK ($(cd "$GL_DIR/lib" && ls libGLESv2.so* libEGL.so* 2> /dev/null | tr '\n' ' '))"
    else
        rm -f "$LINK_DIR"/*
        echo "::warning title=PPSSPP::GL_DIR has no libGLESv2/libEGL pair; linking against Mesa's (libGLESv2.so.2, libEGL.so.1), which the handheld may name differently"
    fi
fi
say "GLES/EGL: $GL_SOURCE"

# ── PPSSPP ───────────────────────────────────────────────────────────────────

SRC="$WORK/ppsspp-$VERSION"
if [ ! -d "$SRC/.git" ]; then
    rm -rf "$SRC"
    quietly clone git clone --depth 1 --branch "$PPSSPP_TAG" https://github.com/hrydgard/ppsspp.git "$SRC"
    # Shallow submodules first; a submodule pinned to a commit its server will
    # not serve shallowly falls back to a full fetch.
    if ! git -C "$SRC" submodule update --init --recursive --depth 1 -j 8 > "$LOG_DIR/submodules.log" 2>&1; then
        quietly submodules-full git -C "$SRC" submodule update --init --recursive -j 8
    else
        say "submodules: ok"
    fi
fi
COMMIT=$(git -C "$SRC" rev-parse HEAD)
say "PPSSPP $PPSSPP_TAG at $COMMIT"

BUILD="$WORK/build-$VERSION"
rm -rf "$BUILD"
# Cortex-A53 (A133P). No rpath: the launcher sets LD_LIBRARY_PATH to the
# bundled libraries, then the firmware's.
ARCH_FLAGS="-march=armv8-a+crc -mtune=cortex-a53"
LINK_FLAGS="-L$LINK_DIR -Wl,--allow-shlib-undefined -Wl,--as-needed"
quietly ppsspp-configure cmake -S "$SRC" -B "$BUILD" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_PREFIX_PATH="$SDL_PREFIX" \
    -DSDL2_DIR="$SDL_PREFIX/lib/cmake/SDL2" \
    -DCMAKE_C_FLAGS="$ARCH_FLAGS" -DCMAKE_CXX_FLAGS="$ARCH_FLAGS" \
    -DCMAKE_EXE_LINKER_FLAGS="$LINK_FLAGS" \
    -DCMAKE_LIBRARY_PATH="$LINK_DIR" \
    -DCMAKE_SKIP_RPATH=ON \
    -DUSING_GLES2=ON -DUSING_EGL=OFF -DUSING_FBDEV=OFF \
    -DVULKAN=OFF -DUSING_X11_VULKAN=OFF -DUSE_WAYLAND_WSI=OFF -DUSE_VULKAN_DISPLAY_KHR=OFF \
    -DUSE_DISCORD=OFF -DUSE_SYSTEM_FFMPEG=OFF -DUSE_SYSTEM_LIBZIP=OFF \
    -DHEADLESS=OFF -DUNITTEST=OFF -DUSE_ASAN=OFF -DUSE_UBSAN=OFF
quietly ppsspp-build cmake --build "$BUILD" --target PPSSPPSDL -j "$JOBS"

BIN="$BUILD/PPSSPPSDL"
[ -f "$BIN" ] || die "the build made no PPSSPPSDL"

# ── Checks: the binary must start on the handheld ────────────────────────────

NEEDED=$(readelf -d "$BIN" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p')
bad=""
for lib in $NEEDED; do
    case "$lib" in
        libSDL2-2.0.so.0 | libGLESv2.so* | libEGL.so* | libz.so.1 | libstdc++.so.6 | libgcc_s.so.1 | \
            libc.so.6 | libm.so.6 | libpthread.so.0 | libdl.so.2 | librt.so.1 | ld-linux-aarch64.so.1) ;;
        *) bad="$bad $lib" ;;
    esac
done
[ -z "$bad" ] || die "PPSSPPSDL needs libraries the handheld does not have:$bad"
glibc=$(objdump -T "$BIN" | grep -o 'GLIBC_[0-9.]*' | sed 's/GLIBC_//' | sort -t. -k1,1n -k2,2n | tail -n 1)
if [ -n "$glibc" ] && ! version_le "$glibc" "$GLIBC_MAX"; then
    die "PPSSPPSDL asks for glibc $glibc; the handheld has $GLIBC_MAX"
fi
say "NEEDED: $(echo $NEEDED); newest glibc symbol: ${glibc:-none}"

# ── Package ──────────────────────────────────────────────────────────────────

STAGE="$WORK/stage"
rm -rf "$STAGE"
mkdir -p "$STAGE/PPSSPP/lib"
cp "$BIN" "$STAGE/PPSSPP/PPSSPPSDL_$PLATFORM"
strip --strip-unneeded "$STAGE/PPSSPP/PPSSPPSDL_$PLATFORM"
chmod 0755 "$STAGE/PPSSPP/PPSSPPSDL_$PLATFORM"
# PPSSPP copies its assets beside the binary when it builds; the source tree's
# are the same files.
if [ -d "$BUILD/assets" ]; then
    cp -R "$BUILD/assets" "$STAGE/PPSSPP/assets"
else
    cp -R "$SRC/assets" "$STAGE/PPSSPP/assets"
fi
for need in ppge_atlas.zim ui_atlas.zim compat.ini; do
    [ -f "$STAGE/PPSSPP/assets/$need" ] || say "note: assets/$need is missing"
done
[ -d "$STAGE/PPSSPP/assets/flash0" ] || die "the assets folder has no flash0 (PSP fonts)"
if echo "$NEEDED" | grep -qx 'libstdc++.so.6'; then
    cp -L "$($CXX -print-file-name=libstdc++.so.6)" "$STAGE/PPSSPP/lib/libstdc++.so.6"
fi
gxx=$($CXX --version | sed -n 1p)
{
    echo "PPSSPP $VERSION for TrimUI $PLATFORM (Smart Pro, Brick, Hammer), built by JukaHub"
    echo "Upstream:  https://github.com/hrydgard/ppsspp tag $PPSSPP_TAG, commit $COMMIT"
    echo "Compiler:  $gxx, Ubuntu 20.04 arm64 (glibc 2.31), $ARCH_FLAGS"
    echo "SDL2:      $SDL2_VERSION headers; the handheld's own libSDL2-2.0.so.0 at run time"
    echo "GLES/EGL:  $GL_SOURCE"
    echo "NEEDED:    $(echo $NEEDED)"
    echo "Bundled:   $(cd "$STAGE/PPSSPP/lib" && ls | tr '\n' ' ')"
    echo "Built:     $(date -u +%Y-%m-%dT%H:%M:%SZ) from JukaHub ${SOURCE_COMMIT:-unknown}"
    echo "License:   GPL-2.0-or-later; the source is the upstream commit above."
} > "$STAGE/PPSSPP/BUILDINFO.txt"
cat "$STAGE/PPSSPP/BUILDINFO.txt"

ZIP="$OUT_DIR/PPSSPP-$PLATFORM.zip"
rm -f "$ZIP"
(cd "$STAGE" && zip -q -r -X "$ZIP" PPSSPP)
echo "$VERSION" > "$OUT_DIR/ppsspp-version.txt"
unzip -l "$ZIP" > /dev/null || die "the package does not read back"
say "packaged $(du -h "$ZIP" | cut -f1) at $ZIP"
