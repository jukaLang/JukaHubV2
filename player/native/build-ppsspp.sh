#!/bin/sh
# Build JukaHub's own PPSSPP for the A133P TrimUI handhelds (Smart Pro, Brick,
# Hammer: "tg5040", PowerVR GE8300) from an upstream PPSSPP release tag.
#
# SDL: built against SDL2 only. Upstream 1.21 moved to SDL3, which the TrimUI
# firmware does not have and whose display back ends do not drive its screen;
# the workflow builds 1.20.3, and this script refuses a tree that does not
# build against SDL2.
#
# Which PPSSPP: 1.20.3, the release TrimUI builds exist for (spruceUI's, which
# ben16w/minui-psp packages and JukaHub installs for PSP games). Juka,
# 2026-11-20: "use 1.20.3 which exists for Trimui". The build is a second
# source of that same version: .github/workflows/ppsspp-trimui.yml runs this
# script when it or the workflow changes, or on a press, and publishes the
# result as the release "ppsspp-tg5040" of this repository. ppsspp_managed.go
# asks both sources; on the same version it keeps minui-psp's (device patches)
# unless JukaHub's is the one installed, and it takes JukaHub's when minui-psp's
# release is missing or did not start here (same verification, same launcher,
# same fallback to the firmware's PPSSPP when a build does not start).
#
# No X11, no Wayland: the handhelds have neither. The SDL2 below is only a link
# target (the binary runs on the firmware's own SDL2), configured with the
# desktop back ends off, so it neither looks for X11 headers (focal's Mesa
# development packages pull libx11-dev in, and SDL's X11 check then stopped
# the build on a missing Xext.h) nor puts X11 into SDL_config.h, where
# SDL_syswm.h would include X11 headers into PPSSPP's own build.
#
# It runs as root inside the Ubuntu 20.04 arm64 build root
# (.github/scripts/build-root.sh build ppsspp), native, with network:
#
#   PPSSPP_TAG   the upstream tag, e.g. v1.20.3 (required)
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
# One change is made to upstream's source: a save state on request (SIGUSR1),
# for JukaHub's universal resume — see "JukaHub's one change" below. A tree
# it does not fit is built unchanged, and the package says so.
#
# The package mirrors what ppsspp_managed.go's ppssppDiscoverLayout looks for:
#   PPSSPP/PPSSPPSDL_tg5040   the binary
#   PPSSPP/assets/            PPSSPP's assets (flash0, atlases, compat.ini,
#                             gamecontrollerdb.txt)
#   PPSSPP/lib/               bundled libraries
#   PPSSPP/BUILDINFO.txt      tag, commit, compiler, GL source, NEEDED list,
#                             and whether the save on request went in
#   PPSSPP/jukahub-resume.txt "save-on-request", only when it did
set -eu

PPSSPP_TAG="${PPSSPP_TAG:?set PPSSPP_TAG to an upstream tag such as v1.20.3}"
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
    *) die "PPSSPP_TAG must look like v1.20.3, not '$PPSSPP_TAG'" ;;
esac
VERSION="${PPSSPP_TAG#v}"

mkdir -p "$LOG_DIR" "$OUT_DIR" "$WORK"

# quietly NAME CMD...: output to LOG_DIR/NAME.log, one line in the job log, the
# end of the log on failure.
quietly() {
    q_name="$1"
    shift
    attempt "$q_name" "$@" || die "$q_name failed (full log: $LOG_DIR/$q_name.log)"
}

# attempt NAME CMD...: quietly, but a failure is returned instead of ending
# the script.
attempt() {
    a_name="$1"
    shift
    a_log="$LOG_DIR/$a_name.log"
    if "$@" > "$a_log" 2>&1; then
        say "$a_name: ok"
        return 0
    fi
    echo "::group::$a_name (last 80 lines)"
    tail -n 80 "$a_log"
    echo "::endgroup::"
    say "$a_name failed (full log: $a_log)"
    return 1
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
    # The handhelds have no X11, no Wayland, no KMS/DRM display, no desktop GL
    # and no sound servers: their SDL2 draws through the GPU vendor's EGL on
    # the framebuffer and plays through ALSA. This copy is only linked against,
    # so every desktop back end is off (header comment); dummy and offscreen
    # stay, which is all a link target needs. The API, and so the symbols
    # PPSSPP links to, is the same whatever back ends a build has.
    quietly sdl2-configure cmake -S "$WORK/SDL2-$SDL2_VERSION" -B "$WORK/sdl2-build" -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$SDL_PREFIX" -DCMAKE_INSTALL_LIBDIR=lib \
        -DSDL_SHARED=ON -DSDL_STATIC=OFF -DSDL_TEST=OFF -DSDL_RPATH=OFF \
        -DSDL_X11=OFF -DSDL_WAYLAND=OFF -DSDL_KMSDRM=OFF -DSDL_RPI=OFF -DSDL_VIVANTE=OFF \
        -DSDL_DIRECTFB=OFF -DSDL_VULKAN=OFF -DSDL_OPENGL=OFF -DSDL_OPENGLES=ON \
        -DSDL_PULSEAUDIO=OFF -DSDL_PIPEWIRE=OFF -DSDL_JACK=OFF -DSDL_SNDIO=OFF \
        -DSDL_ESD=OFF -DSDL_NAS=OFF -DSDL_ARTS=OFF -DSDL_DBUS=OFF -DSDL_IBUS=OFF
    quietly sdl2-build cmake --build "$WORK/sdl2-build" -j "$JOBS"
    quietly sdl2-install cmake --install "$WORK/sdl2-build"
    # Belt and braces: a back end that still slipped into the configuration
    # would bring its headers into PPSSPP's build through SDL_config.h.
    [ -f "$SDL_PREFIX/include/SDL2/SDL_config.h" ] || die "the SDL2 link target installed no SDL_config.h"
    if grep -Eq '^#define SDL_VIDEO_DRIVER_(X11|WAYLAND) ' "$SDL_PREFIX/include/SDL2/SDL_config.h"; then
        rm -rf "$SDL_PREFIX"
        die "the SDL2 link target was configured with X11 or Wayland (see $LOG_DIR/sdl2-configure.log)"
    fi
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
# Upstream moved the SDL front end to SDL3 for 1.21 (hrydgard/ppsspp#22956). The
# handheld's display only works through the firmware's own SDL2, so a tree that
# no longer builds against SDL2 is refused here with a clear message; the
# workflow builds 1.20.3, which does.
if ! grep -v '^[[:space:]]*#' "$SRC/CMakeLists.txt" |
    grep -Eq 'find_package[[:space:]]*\([[:space:]]*SDL2([^_[:alnum:]]|$)|SDL2::SDL2([^_[:alnum:]]|$)|SDL2_LIBRAR'; then
    die "PPSSPP $PPSSPP_TAG builds its SDL front end with SDL3 only; the TrimUI firmware has SDL2 (build v1.20.3)"
fi

# ── JukaHub's one change: a save state on request ────────────────────────────
#
# Universal resume (player/instantresume.go, ppsspp_resume.go): before a power
# off, or at a battery about to give out, JukaHub saves the game that is
# running. Upstream PPSSPP can load a state at start (--state=) but cannot be
# asked for one from outside, so this build adds exactly that: SIGUSR1 asks
# the running game for a save state at $JUKAHUB_PPSSPP_SAVE_TO — a file in
# JukaHub's data folder, never one of the player's slots — which PPSSPP's own
# SaveState::Save writes on its next frame; "<file>.done" then holds "ok" or
# "fail". Without the variable a request does nothing. The handler is set by a
# static initialiser, before SDL installs its own (SIGINT and SIGTERM only, so
# their handling is unchanged). JukaHub waits for the answer, then asks PPSSPP
# to quit as before, and starts the game from that state next time.
#
# The change goes in by text at one anchor, the first line of NativeFrame in
# UI/NativeApp.cpp, which every front end calls once a frame. A tree where
# that line is not there exactly once, or where the changed source does not
# build, is built as upstream left it; the package says which (BUILDINFO.txt's
# Resume line), and only a binary with jukahub-resume.txt beside it is ever
# sent the signal (SIGUSR1 ends a program that does not handle it).
RESUME_ANCHOR='void NativeFrame(GraphicsContext *graphicsContext) {'
RESUME_FILE="$SRC/UI/NativeApp.cpp"
RESUME_BLOCK="$WORK/jukahub-resume.cpp"
cat > "$RESUME_BLOCK" << 'EOF'
// ---- JukaHub: a save state on request -------------------------------------
// Added by JukaHub's build (github.com/jukaLang/JukaHubV2,
// player/native/build-ppsspp.sh). SIGUSR1 asks for a save state of the game
// running now at $JUKAHUB_PPSSPP_SAVE_TO; "<file>.done" says "ok" or "fail"
// once PPSSPP has written it. JukaHub asks before a power off or at a
// critical battery, and starts the game from it with --state= next time.
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <string>
#include "Core/SaveState.h"

static volatile std::sig_atomic_t g_jukahubSaveRequested = 0;

static void JukaHubOnSaveSignal(int) {
	g_jukahubSaveRequested = 1;
}

namespace {
struct JukaHubSaveSignalSetup {
	JukaHubSaveSignalSetup() {
		std::signal(SIGUSR1, JukaHubOnSaveSignal);
	}
} g_jukahubSaveSignalSetup;
}  // namespace

static void JukaHubWriteSaveAnswer(const std::string &target, bool ok) {
	const std::string answer = target + ".done";
	FILE *f = std::fopen(answer.c_str(), "w");
	if (!f)
		return;
	std::fputs(ok ? "ok\n" : "fail\n", f);
	std::fclose(f);
}

static void JukaHubResumeFrame() {
	if (!g_jukahubSaveRequested)
		return;
	g_jukahubSaveRequested = 0;
	const char *to = std::getenv("JUKAHUB_PPSSPP_SAVE_TO");
	if (!to || !*to)
		return;
	const std::string target(to);
	SaveState::Save(Path(target), -1, [target](SaveState::Status status, auto &&...) {
		JukaHubWriteSaveAnswer(target, status == SaveState::Status::SUCCESS);
	});
}
// ---- end JukaHub -----------------------------------------------------------

EOF

# resume_anchors FILE: how many lines of FILE are the anchor (trailing blanks
# and a CR aside).
resume_anchors() {
    awk -v anchor="$RESUME_ANCHOR" '
        { line = $0; sub(/[ \t\r]+$/, "", line) }
        line == anchor { n++ }
        END { print n + 0 }
    ' "$1"
}

# resume_patch: puts the block above before NativeFrame and its call first in
# it. A tree already changed (a reused work folder) is left as it is.
resume_patch() {
    [ -f "$RESUME_FILE" ] || return 1
    grep -q 'JukaHubResumeFrame();' "$RESUME_FILE" && return 0
    [ "$(resume_anchors "$RESUME_FILE")" = 1 ] || return 1
    cp "$RESUME_FILE" "$RESUME_FILE.jukahub-orig"
    if ! awk -v anchor="$RESUME_ANCHOR" -v block="$RESUME_BLOCK" '
        { line = $0; sub(/[ \t\r]+$/, "", line) }
        line == anchor && !done {
            while ((getline b < block) > 0) print b
            print
            print "\tJukaHubResumeFrame();"
            done = 1
            next
        }
        { print }
    ' "$RESUME_FILE.jukahub-orig" > "$RESUME_FILE"; then
        cp "$RESUME_FILE.jukahub-orig" "$RESUME_FILE"
        return 1
    fi
    grep -q 'JukaHubResumeFrame();' "$RESUME_FILE"
}

# resume_unpatch: the file as upstream has it.
resume_unpatch() {
    if [ -f "$RESUME_FILE.jukahub-orig" ]; then
        cp "$RESUME_FILE.jukahub-orig" "$RESUME_FILE"
    fi
}

RESUME=0
RESUME_NOTE="none: JukaHub's change was not applied"
if resume_patch; then
    RESUME=1
    RESUME_NOTE="save-on-request (JukaHub's change: SIGUSR1 writes a state to \$JUKAHUB_PPSSPP_SAVE_TO)"
    say "save on request: added to UI/NativeApp.cpp"
else
    resume_unpatch
    RESUME_NOTE="none: the anchor line was not found once in UI/NativeApp.cpp"
    echo "::warning title=PPSSPP::JukaHub's save-on-request change found no single NativeFrame line in UI/NativeApp.cpp; this build cannot save a game for a power off"
fi

BUILD="$WORK/build-$VERSION"
# Cortex-A53 (A133P). No rpath: the launcher sets LD_LIBRARY_PATH to the
# bundled libraries, then the firmware's.
ARCH_FLAGS="-march=armv8-a+crc -mtune=cortex-a53"
LINK_FLAGS="-L$LINK_DIR -Wl,--allow-shlib-undefined -Wl,--as-needed"

# ppsspp_build SUFFIX: configure and build PPSSPPSDL in a fresh BUILD; the
# logs are ppsspp-configure$SUFFIX and ppsspp-build$SUFFIX.
ppsspp_build() {
    rm -rf "$BUILD"
    attempt "ppsspp-configure$1" cmake -S "$SRC" -B "$BUILD" -G Ninja \
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
        -DHEADLESS=OFF -DUNITTEST=OFF -DUSE_ASAN=OFF -DUSE_UBSAN=OFF &&
        attempt "ppsspp-build$1" cmake --build "$BUILD" --target PPSSPPSDL -j "$JOBS"
}

if ! ppsspp_build ""; then
    [ "$RESUME" = 1 ] || die "PPSSPP did not build (see the log above)"
    # The change is what failed, most likely: upstream as tagged instead, so
    # the release still gets a PPSSPP — one that cannot save on request.
    echo "::warning title=PPSSPP::PPSSPP with JukaHub's save-on-request change did not build; building it as upstream left it"
    resume_unpatch
    RESUME=0
    RESUME_NOTE="none: PPSSPP with JukaHub's change did not build"
    ppsspp_build "-upstream" || die "PPSSPP did not build, with or without JukaHub's change (see the logs above)"
fi

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

# A load test in the root: the dynamic loader resolves every NEEDED library and
# symbol (the SDL2 built above, Mesa's GLES/EGL, the root's libstdc++) and
# PPSSPP prints its version without opening a window. A warning only — the
# handheld's libraries are what count, and JukaHub checks those before it
# switches — but a binary that cannot even load here is worth seeing early.
if smoke=$(LD_LIBRARY_PATH="$SDL_PREFIX/lib" SDL_VIDEODRIVER=dummy SDL_AUDIODRIVER=dummy \
    timeout 30 "$BIN" --version 2>&1); then
    say "load test: $(printf '%s\n' "$smoke" | tail -n 1)"
else
    printf '%s\n' "$smoke" | tail -n 20
    echo "::warning title=PPSSPP::PPSSPPSDL --version did not run in the build root (see above); the package is published anyway and the handheld decides"
fi

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
# The flag JukaHub looks for before it sends the save request (ppsspp_resume.go).
if [ "$RESUME" = 1 ]; then
    printf '%s\n' 'save-on-request' > "$STAGE/PPSSPP/jukahub-resume.txt"
fi
LICENSE_NOTE="GPL-2.0-or-later; the source is the upstream commit above."
if [ "$RESUME" = 1 ]; then
    LICENSE_NOTE="GPL-2.0-or-later; the source is the upstream commit above plus the change player/native/build-ppsspp.sh makes, in JukaHub ${SOURCE_COMMIT:-unknown}."
fi
gxx=$($CXX --version | sed -n 1p)
{
    echo "PPSSPP $VERSION for TrimUI $PLATFORM (Smart Pro, Brick, Hammer), built by JukaHub"
    echo "Upstream:  https://github.com/hrydgard/ppsspp tag $PPSSPP_TAG, commit $COMMIT"
    echo "Resume:    $RESUME_NOTE"
    echo "Compiler:  $gxx, Ubuntu 20.04 arm64 (glibc 2.31), $ARCH_FLAGS"
    echo "SDL2:      $SDL2_VERSION headers; the handheld's own libSDL2-2.0.so.0 at run time"
    echo "GLES/EGL:  $GL_SOURCE"
    echo "NEEDED:    $(echo $NEEDED)"
    echo "Bundled:   $(cd "$STAGE/PPSSPP/lib" && ls | tr '\n' ' ')"
    echo "Built:     $(date -u +%Y-%m-%dT%H:%M:%SZ) from JukaHub ${SOURCE_COMMIT:-unknown}"
    echo "License:   $LICENSE_NOTE"
} > "$STAGE/PPSSPP/BUILDINFO.txt"
cat "$STAGE/PPSSPP/BUILDINFO.txt"

ZIP="$OUT_DIR/PPSSPP-$PLATFORM.zip"
rm -f "$ZIP"
(cd "$STAGE" && zip -q -r -X "$ZIP" PPSSPP)
echo "$VERSION" > "$OUT_DIR/ppsspp-version.txt"
unzip -l "$ZIP" > /dev/null || die "the package does not read back"
say "packaged $(du -h "$ZIP" | cut -f1) at $ZIP"
