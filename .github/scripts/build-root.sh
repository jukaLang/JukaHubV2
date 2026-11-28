#!/bin/sh
# The Ubuntu 20.04 (glibc 2.31) arm64 build root in which the release builds
# JukaHub's handheld-native parts, and the commands that build them there. These
# workflows run it, each on arm64 runners of its own, side by side:
#
#   remote-play-helpers.yml  the PS Remote Play helpers and AndroidRT, which
#                            every TrimUI archive carries
#   wine-runtime.yml         the Box64 pack of the Windows-app runtime, a
#                            release asset of its own
#   ppsspp-trimui.yml        JukaHub's own PPSSPP for the tg5040 handhelds,
#                            checked daily, the rolling release "ppsspp-tg5040"
#   retroarch-cores.yml      RetroArch cores the firmware's glibc can open, on
#                            four runners, the rolling release "cores-aarch64"
#
# Why an arm64 runner, why a 20.04 root and why debootstrap rather than a
# container image: see remote-play-helpers.yml.
#
#   build-root.sh create            debootstrap the root at ROOTFS and install
#                                   what the builds need
#   build-root.sh build helpers     player/native/build-helpers.sh, native mode
#   build-root.sh build androidrt   player/native/build-runtimes.sh androidrt
#   build-root.sh build box64       gcc 10, then player/native/build-runtimes.sh
#                                   wine
#   build-root.sh build ppsspp      player/native/build-ppsspp.sh for the tag in
#                                   PPSSPP_TAG (GL_DIR: the TrimUI SDK's GLES
#                                   libraries, inside the checkout)
#   build-root.sh build cores       player/native/build-cores.sh for the cores
#                                   named in CORES (default: all), into
#                                   player/native/bin/cores
#   build-root.sh check androidrt   advisory: AndroidRT's host smoke suite
#   build-root.sh check box64       advisory: the pinned Wine under the new Box64
#   build-root.sh key PART          the name of the artifact an earlier run kept
#                                   PART under (helpers, androidrt or box64): a
#                                   prefix and a hash of what PART is built from
#   build-root.sh commit androidrt  "<commit> <unix time>" of the newest commit
#                                   that changed what AndroidRT is built from
#   build-root.sh release           unmount what create mounted
#
# Everything that decides what a build produces is in this file or in the
# scripts it runs, never in the workflows, so each key hashes this file and the
# part's own sources and leaves the workflow files out. While the workflow was
# in the keys, every edit to it (a step's wording, a line of a report) threw
# away all three builds an earlier run had kept, and the next release rebuilt
# them all.
#
# create, build, check and release run on the runner as a user with
# passwordless sudo, and mount the checkout (GITHUB_WORKSPACE, else this
# repository) at /src inside the root. key needs only git and sha256sum.

set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
REPO="$(CDPATH= cd -- "$HERE/../.." && pwd)"
WORKSPACE="${GITHUB_WORKSPACE:-$REPO}"
ROOTFS="${ROOTFS:-/srv/jukahub-focal}"
# TEST-15: one dated Ubuntu archive snapshot for debootstrap and every apt
# pocket. The snapshot service serves arm64 under /ubuntu/<id>, the same tree
# ports.ubuntu.com serves live. Focal's public pockets froze in 2025-05, so
# this snapshot is also the last state a fresh focal build could ever see.
# Set SNAPSHOT to move the root deliberately; a malformed id stops the build
# instead of silently falling back to a moving mirror.
SNAPSHOT="${SNAPSHOT:-20250530T000000Z}"
case "$SNAPSHOT" in
    [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z) ;;
    *) echo "build-root: SNAPSHOT must be YYYYMMDDTHHMMSSZ" >&2; exit 2 ;;
esac
MIRROR="http://snapshot.ubuntu.com/ubuntu/$SNAPSHOT"
# The logs of create's steps, on the runner (the builds keep theirs inside
# the root, under /tmp).
LOG_DIR="${RUNNER_TEMP:-/tmp}/jukahub-build-root-logs"
if [ -z "${JOBS:-}" ]; then
    JOBS="$(nproc 2>/dev/null || echo 4)"
fi

usage() {
    echo "usage: $0 create | build helpers|androidrt|box64|ppsspp|cores|stt|llm | check androidrt|box64 | key helpers|androidrt|box64|stt|llm | commit androidrt | release" >&2
    exit 2
}

# What AndroidRT's package is made from: its sources, build-tsp.sh and
# launch.sh (copied into the package), the script that checks and packages it,
# this file and .gitattributes; not its smoke tests or its README. Both its
# reuse key (part_sources) and the commit it is named after (part_commit) read
# this list.
ANDROIDRT_SOURCES="player/androidrt/src player/androidrt/build-tsp.sh player/androidrt/launch.sh player/native/build-runtimes.sh .github/scripts/build-root.sh .gitattributes"

# in_root [NAME=VALUE...] CMD...: CMD inside the root, as root, with nothing
# from the runner's environment but HOME, LANG, PATH and the pairs given.
in_root() {
    sudo chroot "$ROOTFS" /usr/bin/env -i HOME=/root LANG=C.UTF-8 \
        DEBIAN_FRONTEND=noninteractive \
        PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin "$@"
}

# quietly NAME CMD...: CMD's output goes to LOG_DIR/NAME.log and the job log
# shows one line for it; a failure shows the end of that log and stops.
quietly() {
    q_name="$1"
    shift
    q_log="$LOG_DIR/$q_name.log"
    q_start="$(date +%s)"
    if "$@" > "$q_log" 2>&1; then
        echo "    $q_name: done in $(( $(date +%s) - q_start )) s"
        return 0
    fi
    echo "---- the end of $q_log ----" >&2
    tail -n 40 "$q_log" >&2
    echo "build-root: $q_name failed" >&2
    exit 1
}

# ── create ───────────────────────────────────────────────────────────────

create() {
    mkdir -p "$LOG_DIR"
    echo "==> Ubuntu 20.04 arm64 build root at $ROOTFS (logs in $LOG_DIR)"
    # Nothing here reads man pages: skip man-db's index rebuild.
    sudo rm -f /var/lib/man-db/auto-update 2> /dev/null || true
    if ! command -v debootstrap > /dev/null 2>&1; then
        quietly runner-apt-update sudo apt-get -o Acquire::Retries=3 update -q
        quietly runner-debootstrap sudo apt-get -o Acquire::Retries=3 install -y -q \
            --no-install-recommends debootstrap
    fi
    # Naming the keyring makes an unsigned or wrongly signed archive fatal;
    # without it debootstrap only warns and carries on.
    quietly debootstrap sudo debootstrap --variant=minbase --arch=arm64 \
        --include=ca-certificates \
        --keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg \
        focal "$ROOTFS" "$MIRROR"
    # The root lives for one job. dpkg need not sync every file it unpacks to
    # disk (Ubuntu's own container images skip that too), apt needs no
    # translated package descriptions, and no build reads the manuals, the
    # documentation or the message translations, which are a good share of the
    # files to unpack.
    printf '%s\n' force-unsafe-io \
        'path-exclude=/usr/share/doc/*' 'path-include=/usr/share/doc/*/copyright' \
        'path-exclude=/usr/share/man/*' 'path-exclude=/usr/share/info/*' \
        'path-exclude=/usr/share/locale/*' \
        | sudo tee "$ROOTFS/etc/dpkg/dpkg.cfg.d/jukahub-build-root" > /dev/null
    printf '%s\n' 'Acquire::Languages "none";' \
        'Acquire::Check-Valid-Until "false";' \
        | sudo tee "$ROOTFS/etc/apt/apt.conf.d/99jukahub-build-root" > /dev/null
    # The security and bug-fix updates too: OpenSSL is linked statically, so
    # whatever libssl-dev holds is what ships. Focal's public update pockets
    # froze when its standard support ended (2025-05-31), so a fresh build
    # links the same OpenSSL as a reused one (TEST-16).
    printf '%s\n' \
        "deb $MIRROR focal main universe" \
        "deb $MIRROR focal-updates main universe" \
        "deb $MIRROR focal-security main universe" \
        | sudo tee "$ROOTFS/etc/apt/sources.list" > /dev/null
    sudo cp -L /etc/resolv.conf "$ROOTFS/etc/resolv.conf"
    sudo mkdir -p "$ROOTFS/src"
    sudo mount --bind "$WORKSPACE" "$ROOTFS/src"
    sudo mount -t proc proc "$ROOTFS/proc"
    sudo mount --bind /dev "$ROOTFS/dev"
    quietly apt-update in_root apt-get -o Acquire::Retries=3 update -q
    # zip and unzip: build-runtimes.sh packages and checks the runtimes.
    quietly build-packages in_root apt-get -o Acquire::Retries=3 install -y -q \
        --no-install-recommends \
        build-essential ca-certificates curl file git libssl-dev make \
        ninja-build patchelf perl pkg-config python3 python3-pip \
        python3-setuptools python3-wheel unzip xz-utils zip
    # 20.04's pip predates the wheel tags current cmake and grpcio-tools
    # publish (manylinux_2_17), and would fall back to compiling them. Its
    # wheel is hashed from PyPI like the tools below (TEST-15). The pin goes
    # through a requirements file inside the root: --hash is a
    # per-requirement option only a requirements file may carry, and on the
    # command line pip reads 'pip==24.0 --hash=sha256:...' as one requirement
    # and rejects it ("Invalid requirement").
    cr_pip_pin=/tmp/jukahub-pip-upgrade.txt
    printf '%s\n' 'pip==24.0 --hash=sha256:ba0d021a166865d2265246961bec0152ff124de910c5cc39f1156ce3fa7c69dc' \
        | sudo tee "$ROOTFS$cr_pip_pin" > /dev/null
    quietly pip in_root python3 -m pip install -q --no-cache-dir --only-binary=:all: \
        --require-hashes -r "$cr_pip_pin"
    # CMake, and the grpcio-tools protoc that player/native/protoc-wrapper.py
    # runs for nanopb: exact versions, wheels only (no source builds, no
    # setup.py), each wheel hashed from PyPI against Python 3.8/aarch64 in
    # /src/player/native/helper-build-requirements.txt. Box64 builds with the
    # same CMake.
    quietly python-tools in_root python3 -m pip install -q --no-cache-dir --only-binary=:all: \
        --require-hashes -r /src/player/native/helper-build-requirements.txt
    if ! cr_cmake="$(in_root cmake --version)"; then
        echo "build-root: the pinned CMake does not run in the root" >&2
        exit 1
    fi
    echo "    $(printf '%s\n' "$cr_cmake" | sed -n 1p)"
    in_root python3 -c 'import grpc_tools.protoc, google.protobuf; print("    protoc wrapper ready")'
}

# ── build and check ──────────────────────────────────────────────────────

# What a runtime package's BUILDINFO.txt names as its source.
source_commit() {
    if [ -n "${GITHUB_SHA:-}" ]; then
        echo "$GITHUB_SHA"
    else
        git -C "$REPO" rev-parse HEAD 2> /dev/null || echo unknown
    fi
}

build() {
    case "$1" in
        helpers)
            in_root JOBS="$JOBS" JUKAHUB_HELPERS_NATIVE=1 DEPS_PREFIX=/opt/jukahub-deps \
                BUILD_ROOT_SNAPSHOT="$SNAPSHOT" \
                /bin/sh /src/player/native/build-helpers.sh
            ;;
        androidrt)
            # Named and dated after the newest commit that changed AndroidRT's
            # sources, not after the tip being built, so an unchanged AndroidRT
            # rebuilds to the same zip byte for byte. git runs here, on the
            # runner: inside the root it may refuse the mounted checkout.
            bd_src="$(part_commit androidrt 2> /dev/null || true)"
            in_root SOURCE_COMMIT="$(source_commit)" SOURCE_KEY="$(part_key androidrt 2> /dev/null || true)" \
                BUILD_ROOT_SNAPSHOT="$SNAPSHOT" \
                ANDROIDRT_COMMIT="${bd_src%% *}" SOURCE_DATE_EPOCH="${bd_src#* }" \
                OUT_DIR=/src/player/native/bin LOG_DIR=/tmp/jukahub-runtime-logs/androidrt \
                /bin/sh /src/player/native/build-runtimes.sh androidrt
            ;;
        box64)
            # gcc 10 from 20.04's universe, nearer than its default gcc 9 to
            # the compilers box64 is developed with; signed by the archive like
            # the rest of the root. Optional: without it Box64 builds with gcc
            # 9, and since the reuse key stands for the gcc 10 build, the step
            # output reusable=false then keeps that pack out of later runs.
            mkdir -p "$LOG_DIR"
            if in_root apt-get -o Acquire::Retries=3 install -y -q --no-install-recommends gcc-10 \
                > "$LOG_DIR/gcc-10.log" 2>&1; then
                bd_cc=gcc-10
                echo "    gcc-10: installed"
            else
                bd_cc=cc
                tail -n 20 "$LOG_DIR/gcc-10.log"
                echo "::warning title=Box64::gcc-10 could not be installed in the build root; building Box64 with gcc 9, and not keeping that pack for later runs"
                if [ -n "${GITHUB_OUTPUT:-}" ]; then
                    echo "reusable=false" >> "$GITHUB_OUTPUT"
                fi
            fi
            in_root JOBS="$JOBS" BOX64_CC="$bd_cc" SOURCE_COMMIT="$(source_commit)" \
                BUILD_ROOT_SNAPSHOT="$SNAPSHOT" \
                SOURCE_KEY="$(part_key box64 2> /dev/null || true)" \
                OUT_DIR=/src/player/native/bin LOG_DIR=/tmp/jukahub-runtime-logs/wine \
                /bin/sh /src/player/native/build-runtimes.sh wine
            ;;
        ppsspp)
            # GL_DIR is a path on the runner inside the checkout; the root sees
            # the checkout at /src.
            bd_gl=""
            if [ -n "${GL_DIR:-}" ] && [ -d "$GL_DIR" ]; then
                bd_gl="/src/${GL_DIR#"$WORKSPACE"/}"
            fi
            in_root JOBS="$JOBS" PPSSPP_TAG="${PPSSPP_TAG:?set PPSSPP_TAG}" GL_DIR="$bd_gl" \
                SOURCE_COMMIT="$(source_commit)" OUT_DIR=/src/player/native/bin \
                LOG_DIR=/tmp/jukahub-ppsspp-logs /bin/sh /src/player/native/build-ppsspp.sh
            ;;
        cores)
            in_root JOBS="$JOBS" CORES="${CORES:-}" SOURCE_COMMIT="$(source_commit)" \
                OUT_DIR=/src/player/native/bin/cores LOG_DIR=/tmp/jukahub-cores-logs \
                /bin/sh /src/player/native/build-cores.sh
            ;;
        stt)
            in_root JOBS="$JOBS" /bin/sh /src/player/native/build-stt.sh
            ;;
        llm)
            in_root JOBS="$JOBS" /bin/sh /src/player/native/build-llm.sh
            ;;
        *) usage ;;
    esac
}

check() {
    case "$1" in
        androidrt)
            in_root LOG_DIR=/tmp/jukahub-runtime-logs/androidrt \
                /bin/sh /src/player/native/build-runtimes.sh smoke
            ;;
        box64)
            in_root LOG_DIR=/tmp/jukahub-runtime-logs/wine \
                /bin/sh /src/player/native/build-runtimes.sh wine-check
            ;;
        *) usage ;;
    esac
}

# ── key ──────────────────────────────────────────────────────────────────

# part_sources PART prints git's index entries ("<mode> <blob id> <stage>",
# a tab, the path) for every file PART is built from. A blob id hashes the
# committed content, so every checkout of a commit gives the same lines,
# whatever line endings it has on disk.
part_sources() {
    case "$1" in
        helpers)
            # player/native: the helper sources, CMakeLists.txt, build-helpers.sh
            # with its pinned versions and checksums, the Python tool pins, and
            # the Moonlight helper (moonlight-stream.c, its self-test clip
            # moonlight-selftest.h264 and its CMake project
            # player/native/moonlight/, all under player/native, so listed by
            # the same pathspec); not its host tests, its READMEs,
            # build-runtimes.sh (the runtime packages) or the PPSSPP and
            # RetroArch-core builds, which are releases of their own.
            git -C "$REPO" ls-files -s -- player/native ':(exclude)player/native/tests' \
                ':(exclude)player/native/*.md' ':(exclude)player/native/build-runtimes.sh' \
                ':(exclude)player/native/build-stt.sh' ':(exclude)player/native/build-llm.sh' \
                ':(exclude)player/native/package-llm.sh' \
                ':(exclude)player/native/build-ppsspp.sh' ':(exclude)player/native/build-cores.sh' \
                .github/scripts/build-root.sh .gitattributes
            ;;
        stt)
            git -C "$REPO" ls-files -s -- player/native/build-stt.sh player/native/vosk-cli.c \
                player/native/helper-build-requirements.txt \
                .github/scripts/build-root.sh .gitattributes
            ;;
        llm)
            git -C "$REPO" ls-files -s -- player/native/build-llm.sh \
                player/native/helper-build-requirements.txt \
                .github/scripts/build-root.sh .gitattributes
            ;;
        androidrt)
            # ANDROIDRT_SOURCES, a list of plain paths.
            # shellcheck disable=SC2086
            git -C "$REPO" ls-files -s -- $ANDROIDRT_SOURCES
            ;;
        box64)
            # build-runtimes.sh pins all of it (the Box64 commit, its build
            # options, the Wine build and its SHA-256); the CMake it builds
            # with comes from the Python tool pins.
            git -C "$REPO" ls-files -s -- player/native/build-runtimes.sh \
                player/native/helper-build-requirements.txt \
                .github/scripts/build-root.sh .gitattributes
            ;;
        *) usage ;;
    esac
}

part_key() {
    case "$1" in
        helpers) pk_prefix=remote-play-helpers pk_anchor=player/native/build-helpers.sh ;;
        androidrt) pk_prefix=androidrt-pack pk_anchor=player/androidrt/build-tsp.sh ;;
        box64) pk_prefix=wine-runtime-pack pk_anchor=player/native/build-runtimes.sh ;;
        stt) pk_prefix=offline-stt pk_anchor=player/native/build-stt.sh ;;
        llm) pk_prefix=offline-llm pk_anchor=player/native/build-llm.sh ;;
        *) usage ;;
    esac
    pk_sources="$(part_sources "$1")" || return 1
    # A listing without the part's own build script, or without this file,
    # is not what the key stands for (an incomplete checkout, a file not
    # committed yet).
    for pk_file in "$pk_anchor" .github/scripts/build-root.sh; do
        if ! printf '%s\n' "$pk_sources" | cut -f2 | grep -qxF "$pk_file"; then
            echo "build-root: git does not list $pk_file" >&2
            return 1
        fi
    done
    printf '%s-%s\n' "$pk_prefix" "$(printf '%s\n' "$pk_sources" | sha256sum | cut -c1-16)"
}

# part_commit PART prints "<commit id> <unix time>" of the newest commit that
# changed what PART is built from. It needs the history: the AndroidRT job
# checks out every commit (without old file contents). In a shallow checkout
# the tip seems to add every file, and the answer is the tip, true for that
# one build only.
part_commit() {
    case "$1" in
        androidrt)
            # shellcheck disable=SC2086
            pc_line="$(git -C "$REPO" log -1 --format='%H %ct' -- $ANDROIDRT_SOURCES)" || return 1
            ;;
        *) usage ;;
    esac
    case "$pc_line" in
        *' '*) ;;
        *) echo "build-root: git names no commit for $1's sources" >&2; return 1 ;;
    esac
    printf '%s\n' "$pc_line"
}

# ── release ──────────────────────────────────────────────────────────────

release() {
    for rl_mount in dev proc src; do
        sudo umount "$ROOTFS/$rl_mount" 2> /dev/null || true
    done
}

cmd="${1:-}"
[ $# -eq 0 ] || shift
case "$cmd:$#" in
    create:0) create ;;
    build:1) build "$1" ;;
    check:1) check "$1" ;;
    key:1) part_key "$1" ;;
    commit:1) part_commit "$1" ;;
    release:0) release ;;
    *) usage ;;
esac
