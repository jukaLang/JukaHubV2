#!/bin/sh
# Build RetroArch cores that load on the TrimUI firmware, for the rolling
# release "cores-aarch64" (.github/workflows/retroarch-cores.yml).
#
# Why: the libretro buildbot builds its linux/aarch64 nightlies on a newer
# Ubuntu than the handheld runs. In November 2026 its mgba_libretro.so needed
# GLIBC_2.35 from libm.so.6, and the stock Smart Pro (glibc 2.33 since firmware
# 1.0.4; its libm defines up to GLIBC_2.32) cannot open it: RetroArch logs
# "version `GLIBC_2.35' not found" and quits. RetroArch's own Online Updater
# ("Update Installed Cores") installs those nightlies without any check, and
# stock firmware runs Game Boy, Game Boy Color and Game Boy Advance all on
# mGBA, so one update leaves three consoles unable to start a game. JukaHub's
# guard (player/elfcompat.go) already refuses such builds before they replace
# anything, but it had nothing to put in place of one that was already there.
#
# These builds are that: each core's upstream libretro source at its newest
# commit, compiled natively in the Ubuntu 20.04 (glibc 2.31) arm64 build root,
# with libstdc++ and libgcc linked in statically where the core's build passes
# LDFLAGS on (the firmware's libstdc++ is older than the root's).
# player/corefix.go installs one when a core on the card cannot load.
#
# It runs as root inside the build root (.github/scripts/build-root.sh build
# cores), native, with network:
#
#   CORES     space-separated names from the table below (default: all of them)
#   OUT_DIR   gets, per core: <core>_libretro.so.zip (the binary at the zip's
#             root, as the buildbot ships it), <core>.crc32 (the binary's
#             CRC32, which the index states) and <core>.buildinfo (source
#             commit, compiler, NEEDED, newest GLIBC/GLIBCXX); for a core with
#             a companion file built from the same tree (FreeJ2ME-Plus: the
#             Java half the core starts, see build_j2me_jar) also that file
#             and <core>.files ("<sha256>  <name>" per file, the form
#             sha256sum writes), which the publish job turns into index lines
#   JOBS, LOG_DIR, SOURCE_COMMIT
#
#   build-cores.sh list     prints the table's core names, one per line
#   build-cores.sh groups   prints the runner groups the workflow builds, one
#                           line of space-separated cores per runner
#
# Every build is checked before it counts: an aarch64 libretro core that needs
# only libraries the handheld has, no glibc newer than GLIBC_MAX and, when it
# links libstdc++ dynamically, nothing newer than GLIBCXX_MAX/CXXABI_MAX; the
# dynamic loader opens it here with every symbol resolved, and it answers
# retro_api_version with 1. A build that fails a check counts as a failed
# attempt, and the next attempt runs. A core with no attempt left fails this
# script; with several CORES the others are still built and the script fails
# at the end.
set -eu

OUT_DIR="${OUT_DIR:-/src/player/native/bin/cores}"
LOG_DIR="${LOG_DIR:-/tmp/jukahub-cores-logs}"
WORK="${WORK:-/tmp/jukahub-cores}"
if [ -z "${JOBS:-}" ]; then
    JOBS="$(nproc 2> /dev/null || echo 4)"
fi
# The newest glibc a core may ask for: what the build root has. The Smart Pro
# has 2.33, but its libm stops at GLIBC_2.32, and the root makes 2.31 the
# ceiling anyway.
GLIBC_MAX=2.31
# For a core that links libstdc++.so.6: the TrimUI SDK's GCC 7.5. The
# firmware's libstdc++ is older than the root's (gcc 9/10); the handheld's own
# check has the last word and refuses a build its libstdc++ cannot load.
GLIBCXX_MAX=3.4.24
CXXABI_MAX=1.3.11

# core|repository|folder the build is in|makefile, or "cmake"|extra arguments
#
# Make builds run `make -f <makefile> platform=unix` as libretro-super's linux
# recipes do. mGBA's libretro tree moved to CMake in September 2026
# (libretro/mgba 89404771c9); its row builds the mgba_libretro target with the
# flags of upstream's libretro CI. A build whose extras fail is tried once more
# without them, from a clean tree.
#
# FreeJ2ME-Plus (J2ME phone games) is a small C bridge in src/libretro that
# starts `java -jar <RetroArch system folder>/<name>-lr.jar` and talks to it
# over pipes; the jar is built from the same commit (build_j2me_jar), since the
# two halves only work as a pair. TARGET_NAME pins the file name its devel
# branch uses (an older tree called it freej2me_libretro.so).
CORE_TABLE='
mgba|https://github.com/libretro/mgba.git|.|cmake|-DLIBMGBA_ONLY=ON -DBUILD_LIBRETRO=ON
gambatte|https://github.com/libretro/gambatte-libretro.git|.|Makefile|
gpsp|https://github.com/libretro/gpsp.git|.|Makefile|
snes9x|https://github.com/libretro/snes9x.git|libretro|Makefile|
snes9x2005|https://github.com/libretro/snes9x2005.git|.|Makefile|
fceumm|https://github.com/libretro/libretro-fceumm.git|.|Makefile.libretro|
nestopia|https://github.com/libretro/nestopia.git|libretro|Makefile|
picodrive|https://github.com/libretro/picodrive.git|.|Makefile.libretro|
genesis_plus_gx|https://github.com/libretro/Genesis-Plus-GX.git|.|Makefile.libretro|
pcsx_rearmed|https://github.com/libretro/pcsx_rearmed.git|.|Makefile.libretro|
mednafen_pce_fast|https://github.com/libretro/beetle-pce-fast-libretro.git|.|Makefile|
mednafen_wswan|https://github.com/libretro/beetle-wswan-libretro.git|.|Makefile|
mednafen_ngp|https://github.com/libretro/beetle-ngp-libretro.git|.|Makefile|
fbneo|https://github.com/libretro/FBNeo.git|src/burner/libretro|Makefile|
freej2me_plus|https://github.com/TASEmulators/freej2me-plus.git|src/libretro|Makefile|TARGET_NAME=freej2me_plus
wasm4|https://github.com/aduros/wasm4.git|runtimes/native|cmake|-DLIBRETRO=ON -DBUILD_TESTS=OFF -DBUILD_TOOLS=OFF
'

# WASM-4 (wasm4.org fantasy-console carts, player/wasm4*.go) is upstream's
# runtimes/native CMake project built for its wasm4_libretro target, the
# libretro buildbot's own recipe: LIBRETRO=ON is the flag its .gitlab-ci.yml
# and AmberELEC's package pass. The project may also configure the standalone
# player's sound library (cubeb); the other two flags keep cubeb from asking
# for googletest, and the retry without the extras still runs.

# The workflow's runners: the Game Boy cores together (the ones a broken
# stock card needs first), the small cores together (FreeJ2ME-Plus's bridge is
# one C file; its jar adds a JDK and Ant to that runner's root), and the two
# long builds on runners of their own. Every core in the table is in exactly
# one group.
CORE_GROUPS='
mgba gambatte gpsp snes9x snes9x2005
fceumm nestopia picodrive genesis_plus_gx mednafen_pce_fast mednafen_wswan mednafen_ngp freej2me_plus wasm4
pcsx_rearmed
fbneo
'

say() { printf 'build-cores: %s\n' "$*"; }
die() {
    printf 'build-cores: %s\n' "$*" >&2
    exit 1
}

core_names() {
    printf '%s\n' "$CORE_TABLE" | sed -n 's/^\([a-z0-9_]*\)|.*/\1/p'
}

case "${1:-}" in
    list)
        core_names
        exit 0
        ;;
    groups)
        printf '%s\n' "$CORE_GROUPS" | sed '/^[[:space:]]*$/d'
        exit 0
        ;;
    "") ;;
    *) die "usage: build-cores.sh [list|groups]" ;;
esac

# version_le A B: A <= B for dotted version numbers.
version_le() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)" = "$2" ]
}

# newest_version FAMILY FILE: the newest FAMILY_x.y.z symbol version FILE asks for.
newest_version() {
    objdump -T "$2" | grep -o "$1_[0-9][0-9.]*" | sed "s/^$1_//" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1
}

mkdir -p "$LOG_DIR" "$OUT_DIR" "$WORK"

CC=gcc
CXX=g++
export CC CXX
COMPILER="$($CXX --version | sed -n 1p)"
say "compiler: $COMPILER"
# zlib: a few cores link the system one, which the firmware has.
if ! apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends zlib1g-dev \
    > "$LOG_DIR/apt-packages.log" 2>&1; then
    tail -n 20 "$LOG_DIR/apt-packages.log"
    die "could not install zlib1g-dev in the build root"
fi

# check_build CORE FILE: the checks above; prints why a build fails them and
# returns 1. Sets c_needed, c_glibc, c_glibcxx for the build info.
check_build() {
    c_core="$1"
    c_bin="$2"
    if ! readelf -h "$c_bin" 2> /dev/null | grep -q 'AArch64'; then
        say "$c_core: not an aarch64 binary"
        return 1
    fi
    c_needed="$(readelf -d "$c_bin" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p')"
    if [ -z "$c_needed" ]; then
        say "$c_core: names no libraries at all (not a dynamically linked core)"
        return 1
    fi
    c_bad=""
    for c_lib in $c_needed; do
        case "$c_lib" in
            libc.so.6 | libm.so.6 | libpthread.so.0 | libdl.so.2 | librt.so.1 | libgcc_s.so.1 | \
                libstdc++.so.6 | libz.so.1 | ld-linux-aarch64.so.1) ;;
            *) c_bad="$c_bad $c_lib" ;;
        esac
    done
    if [ -n "$c_bad" ]; then
        say "$c_core: needs libraries the handheld does not have:$c_bad"
        return 1
    fi
    c_glibc="$(newest_version GLIBC "$c_bin")"
    if [ -z "$c_glibc" ]; then
        say "$c_core: asks for no glibc symbol version (an unexpected build)"
        return 1
    fi
    if ! version_le "$c_glibc" "$GLIBC_MAX"; then
        say "$c_core: asks for glibc $c_glibc; the ceiling is $GLIBC_MAX"
        return 1
    fi
    c_glibcxx="$(newest_version GLIBCXX "$c_bin")"
    if [ -n "$c_glibcxx" ] && ! version_le "$c_glibcxx" "$GLIBCXX_MAX"; then
        say "$c_core: links libstdc++ and asks for GLIBCXX_$c_glibcxx; the ceiling is $GLIBCXX_MAX"
        return 1
    fi
    c_cxxabi="$(newest_version CXXABI "$c_bin")"
    if [ -n "$c_cxxabi" ] && ! version_le "$c_cxxabi" "$CXXABI_MAX"; then
        say "$c_core: links libstdc++ and asks for CXXABI_$c_cxxabi; the ceiling is $CXXABI_MAX"
        return 1
    fi
    # The loader resolves every library and symbol now (ctypes opens with
    # RTLD_NOW), and a libretro core answers retro_api_version with 1. Only
    # the last line counts: a core may print something while it loads.
    if ! python3 -c 'import ctypes, sys
lib = ctypes.CDLL(sys.argv[1])
print(lib.retro_api_version())' "$c_bin" > "$LOG_DIR/$c_core/load-test.out" 2> "$LOG_DIR/$c_core/load-test.log"; then
        tail -n 5 "$LOG_DIR/$c_core/load-test.log"
        say "$c_core: the dynamic loader cannot open it here"
        return 1
    fi
    c_api="$(tail -n 1 "$LOG_DIR/$c_core/load-test.out")"
    if [ "$c_api" != "1" ]; then
        tail -n 5 "$LOG_DIR/$c_core/load-test.log"
        say "$c_core: retro_api_version says '$c_api', not 1"
        return 1
    fi
    return 0
}

# j2me_jdk: a JDK 8 and Ant in the build root, installed the first time a core
# needs them; J2ME_JDK is its JAVA_HOME. FreeJ2ME-Plus's build.xml compiles
# with an old source/target level that JDK 9 and newer refuse, and a JDK 8
# build also keeps the jar on Java 8's class library: a newer javac without
# --release links calls such as ByteBuffer.flip() to methods Java 8 does not
# have. The JDK goes in first, on its own, so Ant's need for a Java runtime is
# already met and apt does not add the root's default Java beside it.
J2ME_JDK=""
j2me_jdk() {
    [ -z "$J2ME_JDK" ] || return 0
    if ! apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends openjdk-8-jdk-headless \
        > "$LOG_DIR/apt-jdk8.log" 2>&1; then
        tail -n 20 "$LOG_DIR/apt-jdk8.log"
        say "could not install openjdk-8-jdk-headless in the build root"
        return 1
    fi
    if ! apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends ant \
        > "$LOG_DIR/apt-ant.log" 2>&1; then
        tail -n 20 "$LOG_DIR/apt-ant.log"
        say "could not install ant in the build root"
        return 1
    fi
    for j_home in /usr/lib/jvm/java-8-openjdk-arm64 /usr/lib/jvm/java-8-openjdk-*; do
        if [ -x "$j_home/bin/javac" ]; then
            J2ME_JDK="$j_home"
            break
        fi
    done
    [ -n "$J2ME_JDK" ] || { say "no JDK 8 javac under /usr/lib/jvm"; return 1; }
    say "JDK for the J2ME jar: $("$J2ME_JDK/bin/javac" -version 2>&1 | sed -n 1p)"
    return 0
}

# build_j2me_jar CORE SRC BIN: FreeJ2ME-Plus's Java half, from the tree at SRC
# that built BIN. The bridge runs `java -jar <system folder>/<name>`, and the
# name is the one BIN carries (freej2me_plus-lr.jar on devel, freej2me-lr.jar
# before the rename), so the jar is published under that name whatever Ant
# called it. Checks: a zip with a Main-Class, and no class newer than Java 8
# (class file version 52), so any Java from 8 up runs it. Writes the jar and
# CORE.files to OUT_DIR, before the core's .crc32: a core whose jar did not
# build is not published.
build_j2me_jar() {
    j_core="$1"
    j_src="$2"
    j_bin="$3"
    j_log="$LOG_DIR/$j_core"
    j_name="$(grep -ao '[A-Za-z0-9_.]*-lr\.jar' "$j_bin" | sed -n 1p)"
    case "$j_name" in
        [A-Za-z]*-lr.jar) ;;
        *)
            say "$j_core: the core names no <name>-lr.jar to start (found '$j_name')"
            return 1
            ;;
    esac
    case "$j_name" in
        *[!A-Za-z0-9_.-]* | *..*)
            say "$j_core: unexpected jar name '$j_name'"
            return 1
            ;;
    esac
    j2me_jdk || return 1
    if [ ! -f "$j_src/build.xml" ]; then
        say "$j_core: no build.xml in the source tree"
        return 1
    fi
    if ! (cd "$j_src" && JAVA_HOME="$J2ME_JDK" PATH="$J2ME_JDK/bin:$PATH" ant) > "$j_log/ant.log" 2>&1; then
        echo "::group::$j_core ant log (last 60 lines)"
        tail -n 60 "$j_log/ant.log"
        echo "::endgroup::"
        say "$j_core: ant did not build the jars"
        return 1
    fi
    # The -lr jar Ant wrote: the one named as the core wants it (in build/,
    # where build.xml puts its jars, before any copy elsewhere in the tree),
    # else the only one there is.
    j_jar=""
    if [ -f "$j_src/build/$j_name" ]; then
        j_jar="$j_src/build/$j_name"
    else
        j_jar="$(find "$j_src" -path "$j_src/.git" -prune -o -type f -name "$j_name" -print | sed -n 1p)"
    fi
    if [ -z "$j_jar" ]; then
        j_all="$(find "$j_src" -path "$j_src/.git" -prune -o -type f -name '*-lr.jar' -print)"
        if [ "$(printf '%s\n' "$j_all" | sed '/^$/d' | wc -l)" -ne 1 ]; then
            say "$j_core: ant built no single *-lr.jar (found: $(echo $j_all))"
            return 1
        fi
        j_jar="$j_all"
    fi
    if ! unzip -tq "$j_jar" > /dev/null 2>&1; then
        say "$j_core: $(basename "$j_jar") does not read back as a zip"
        return 1
    fi
    if ! unzip -p "$j_jar" META-INF/MANIFEST.MF 2> /dev/null | grep -qi '^Main-Class:'; then
        say "$j_core: $(basename "$j_jar") has no Main-Class (java -jar cannot start it)"
        return 1
    fi
    if ! j_newest="$(python3 -c 'import struct, sys, zipfile
newest = 0
with zipfile.ZipFile(sys.argv[1]) as z:
    for name in z.namelist():
        # A multi-release library keeps newer classes for newer Javas
        # under META-INF/versions/, and module-info is for Java 9 and up:
        # Java 8 reads neither.
        if name.startswith("META-INF/versions/") or name.endswith("module-info.class"):
            continue
        if name.endswith(".class"):
            head = z.read(name)[:8]
            if len(head) < 8 or head[:4] != b"\xca\xfe\xba\xbe":
                sys.exit("not a class file: " + name)
            newest = max(newest, struct.unpack(">H", head[6:8])[0])
if newest == 0:
    sys.exit("no classes")
print(newest)' "$j_jar" 2> "$j_log/classcheck.log")"; then
        tail -n 5 "$j_log/classcheck.log"
        say "$j_core: could not read the classes in $(basename "$j_jar")"
        return 1
    fi
    if [ "$j_newest" -gt 52 ]; then
        say "$j_core: $(basename "$j_jar") has classes for Java $((j_newest - 44)); Java 8 (52) is the most it may need"
        return 1
    fi
    cp "$j_jar" "$OUT_DIR/$j_name" || return 1
    j_sum="$(sha256sum "$OUT_DIR/$j_name" | cut -d' ' -f1)" || return 1
    case "$j_sum" in
        "" | *[!0-9a-f]*) j_sum="" ;;
    esac
    if [ "${#j_sum}" -ne 64 ]; then
        say "$j_core: could not take the SHA-256 of $j_name"
        return 1
    fi
    printf '%s  %s\n' "$j_sum" "$j_name" > "$OUT_DIR/$j_core.files" || return 1
    j_files="$j_name (sha256 $j_sum, classes for Java 8, built with $("$J2ME_JDK/bin/javac" -version 2>&1 | sed -n 1p))"
    say "$j_core: $j_name $(du -h "$OUT_DIR/$j_name" | cut -f1), sha256 $j_sum"
    return 0
}

# build_one CORE: builds, checks and packages one core; non-zero on any failure
# (the caller goes on with the next one). It runs as an `if` condition, where
# set -e is off: every step that can fail is checked by hand.
build_one() {
    b_core="$1"
    b_row="$(printf '%s\n' "$CORE_TABLE" | grep "^$b_core|" | head -n 1)"
    [ -n "$b_row" ] || { say "$b_core: not in the table"; return 1; }
    b_repo="$(printf '%s' "$b_row" | cut -d'|' -f2)"
    b_dir="$(printf '%s' "$b_row" | cut -d'|' -f3)"
    b_makefile="$(printf '%s' "$b_row" | cut -d'|' -f4)"
    b_extra="$(printf '%s' "$b_row" | cut -d'|' -f5)"
    b_log="$LOG_DIR/$b_core"
    mkdir -p "$b_log"
    b_src="$WORK/src-$b_core"
    rm -rf "$b_src"
    if ! git clone --depth 1 --recurse-submodules --shallow-submodules "$b_repo" "$b_src" \
        > "$b_log/clone.log" 2>&1; then
        # A submodule pinned to a commit its server will not serve shallowly.
        rm -rf "$b_src"
        if ! git clone --recurse-submodules "$b_repo" "$b_src" > "$b_log/clone-full.log" 2>&1; then
            tail -n 20 "$b_log/clone-full.log"
            say "$b_core: could not clone $b_repo"
            return 1
        fi
    fi
    b_commit="$(git -C "$b_src" rev-parse HEAD)" || return 1
    b_mk="$b_src/$b_dir"
    if [ "$b_makefile" != cmake ]; then
        # The table's makefile, else the usual libretro names.
        for b_try in "$b_makefile" Makefile.libretro Makefile; do
            if [ -f "$b_mk/$b_try" ]; then
                b_makefile="$b_try"
                break
            fi
        done
        [ -f "$b_mk/$b_makefile" ] || { say "$b_core: no makefile in $b_dir"; return 1; }
    elif [ ! -f "$b_mk/CMakeLists.txt" ]; then
        say "$b_core: no CMakeLists.txt in $b_dir"
        return 1
    fi
    say "$b_core: $b_repo at $b_commit ($b_dir/$b_makefile ${b_extra:-})"

    # Attempts, first to last: the table's extras with libstdc++ linked in
    # (its symbols kept out of the core's exports), the extras without that (a
    # static archive that is not PIC fails at the link; a make build keeps its
    # objects, so only the link runs again), then — from a clean tree — no
    # extras at all. Each build is checked before it counts.
    b_target="$b_core"_libretro.so
    b_bin=""
    b_how=""
    # "-" stands for the generic retry after the extras; with no extras the
    # first pass already was the generic build.
    for b_args in "$b_extra" -; do
        if [ "$b_args" = - ]; then
            [ -n "$b_extra" ] || break
            git -C "$b_src" clean -fdxq > /dev/null 2>&1 || true
            echo "::warning title=$b_core::the build with '$b_extra' failed; trying without it"
            b_args=""
        fi
        for b_static in yes no; do
            find "$b_mk" -maxdepth 2 -name "$b_target" -type f -exec rm -f {} + 2> /dev/null || true
            b_name="build-${b_args:+extras-}static-$b_static"
            if [ "$b_makefile" = cmake ]; then
                # CMake reads LDFLAGS when it configures: a fresh build folder
                # per attempt.
                rm -rf "$b_mk/build-jukahub"
                # shellcheck disable=SC2086 # the extras are separate arguments
                if [ "$b_static" = yes ]; then
                    (cd "$b_mk" && LDFLAGS="-static-libstdc++ -static-libgcc -Wl,--exclude-libs,ALL" \
                        cmake -S . -B build-jukahub -G Ninja -DCMAKE_BUILD_TYPE=Release $b_args &&
                        cmake --build build-jukahub --target "$b_core"_libretro -j "$JOBS") \
                        > "$b_log/$b_name.log" 2>&1
                else
                    (cd "$b_mk" && unset LDFLAGS &&
                        cmake -S . -B build-jukahub -G Ninja -DCMAKE_BUILD_TYPE=Release $b_args &&
                        cmake --build build-jukahub --target "$b_core"_libretro -j "$JOBS") \
                        > "$b_log/$b_name.log" 2>&1
                fi
            elif [ "$b_static" = yes ]; then
                # shellcheck disable=SC2086 # the extras are separate make arguments
                (cd "$b_mk" && LDFLAGS="-static-libstdc++ -static-libgcc -Wl,--exclude-libs,ALL" \
                    make -f "$b_makefile" platform=unix -j "$JOBS" $b_args) > "$b_log/$b_name.log" 2>&1
            else
                # shellcheck disable=SC2086
                (cd "$b_mk" && unset LDFLAGS && make -f "$b_makefile" platform=unix -j "$JOBS" $b_args) \
                    > "$b_log/$b_name.log" 2>&1
            fi
            b_status=$?
            b_found="$(find "$b_mk" -maxdepth 2 -name "$b_target" -type f | head -n 1)"
            if [ "$b_status" -ne 0 ] || [ -z "$b_found" ]; then
                say "$b_core: $b_name did not build (log: $b_log/$b_name.log)"
                continue
            fi
            if check_build "$b_core" "$b_found"; then
                b_bin="$b_found"
                b_how="${b_args:-no extras}, libstdc++ $([ "$b_static" = yes ] && echo 'static if linked' || echo 'shared if linked')"
                break
            fi
            say "$b_core: $b_name built but failed the checks"
        done
        [ -z "$b_bin" ] || break
    done
    if [ -z "$b_bin" ]; then
        b_last="$(ls -t "$b_log"/build-*.log 2> /dev/null | head -n 1)"
        if [ -n "$b_last" ]; then
            echo "::group::$b_core build log (last 60 lines)"
            tail -n 60 "$b_last"
            echo "::endgroup::"
        fi
        say "$b_core: no build passed"
        return 1
    fi
    if [ -n "$c_glibcxx" ]; then
        echo "::warning title=$b_core::links libstdc++.so.6 (needs GLIBCXX_$c_glibcxx); the handheld refuses it if the firmware's is older"
    fi
    # A companion file from the same tree, before anything of this core is
    # packaged: without it the core is not published.
    j_files=""
    rm -f "$OUT_DIR/$b_core.files"
    case "$b_core" in
        freej2me_plus)
            build_j2me_jar "$b_core" "$b_src" "$b_bin" || return 1
            ;;
    esac

    # ── Package ──────────────────────────────────────────────────────────────
    b_stage="$WORK/stage-$b_core"
    rm -rf "$b_stage"
    mkdir -p "$b_stage" || return 1
    cp "$b_bin" "$b_stage/$b_target" || return 1
    strip --strip-unneeded "$b_stage/$b_target" || return 1
    chmod 0755 "$b_stage/$b_target" || return 1
    b_crc="$(python3 -c 'import sys, zlib
data = open(sys.argv[1], "rb").read()
print("%08x" % (zlib.crc32(data) & 0xffffffff))' "$b_stage/$b_target")" || return 1
    case "$b_crc" in
        [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
        *)
            say "$b_core: bad checksum '$b_crc'"
            return 1
            ;;
    esac
    b_zip="$OUT_DIR/$b_target.zip"
    rm -f "$b_zip"
    (cd "$b_stage" && zip -q -X "$b_zip" "$b_target") || return 1
    if ! unzip -tq "$b_zip" > /dev/null || [ "$(unzip -Z1 "$b_zip")" != "$b_target" ]; then
        say "$b_core: the zip does not read back as one $b_target"
        rm -f "$b_zip"
        return 1
    fi
    {
        echo "core:      $b_core"
        echo "upstream:  $b_repo commit $b_commit"
        echo "build:     $b_dir/$b_makefile ($b_how)"
        echo "compiler:  $COMPILER, Ubuntu 20.04 arm64 (glibc 2.31)"
        echo "NEEDED:    $(echo $c_needed)"
        echo "glibc:     $c_glibc; libstdc++: ${c_glibcxx:+GLIBCXX_$c_glibcxx}${c_glibcxx:-not linked}"
        echo "crc32:     $b_crc"
        if [ -n "$j_files" ]; then
            echo "with:      $j_files"
        fi
        echo "built:     $(date -u +%Y-%m-%dT%H:%M:%SZ) by JukaHub ${SOURCE_COMMIT:-unknown}"
        echo
    } > "$OUT_DIR/$b_core.buildinfo" || return 1
    # The checksum last: the publish job takes a core whose .crc32 is there.
    printf '%s\n' "$b_crc" > "$OUT_DIR/$b_core.crc32" || return 1
    say "$b_core: packaged $(du -h "$b_zip" | cut -f1), crc $b_crc, glibc $c_glibc${c_glibcxx:+, GLIBCXX_$c_glibcxx}"
    rm -rf "$b_src" "$b_stage"
    return 0
}

want="${CORES:-}"
[ -n "$(printf '%s' "$want" | tr -d '[:space:]')" ] || want="$(core_names)"
failed=""
built=0
for core in $want; do
    case "$core" in
        *[!a-z0-9_]*) die "unexpected core name '$core'" ;;
    esac
    if build_one "$core"; then
        built=$((built + 1))
    else
        failed="$failed $core"
    fi
done
say "built $built core(s)${failed:+; failed:$failed}"
[ -z "$failed" ] || exit 1
