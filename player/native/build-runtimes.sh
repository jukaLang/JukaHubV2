#!/bin/sh
# Build the two runtime packages JukaHub ships beside the app, natively on an
# aarch64 Linux machine:
#
#   JukaHub-AndroidRT-linux-arm64.zip   androidrt/     the Android-app runtime
#                                                      (../androidrt: loader,
#                                                      launch.sh, surface-probe,
#                                                      props.txt.example)
#   JukaHub-Wine-linux-arm64.zip        wine-runtime/  Box64 for the Windows-app
#                                                      runtime (box64/box64, the
#                                                      x86_64 libraries it
#                                                      installs for itself in
#                                                      box64/x64lib) and
#                                                      wine.json, the Wine build
#                                                      it runs
#
# Each package is one folder holding BUILDINFO.txt (what was built, from what,
# and what was checked) and SHA256SUMS ("<sha256>  <path>" for every other file
# in the folder). JukaHub installs a package only when every file is listed and
# matches (../relpack.go), and every TrimUI archive also carries androidrt/
# beside the binary (Apps/JukaHub/androidrt).
#
# The release builds AndroidRT in the Remote Play helper job
# (.github/workflows/remote-play-helpers.yml) and the Box64 pack in a job of
# its own (.github/workflows/wine-runtime.yml), each on an arm64 runner inside
# an Ubuntu 20.04 root (.github/scripts/build-root.sh makes it and runs this
# script there), whose glibc 2.31 is older than the Smart Pro's 2.33, so the
# binaries start there. The same commands work on any aarch64 Linux machine
# whose glibc is no newer than the handheld's (a 20.04 arm64 container works).
#
#   build-runtimes.sh androidrt        build AndroidRT with ../androidrt/build-tsp.sh,
#                                      check it and package it
#   build-runtimes.sh wine             build Box64 from its pinned commit, check
#                                      it, write wine.json for the pinned Wine
#                                      build and package them
#   build-runtimes.sh wine-check [ZIP] advisory: start the pinned Wine under the
#                                      pack's Box64 (`wine --version`)
#   build-runtimes.sh smoke            advisory: AndroidRT's host smoke suite
#   build-runtimes.sh verify ZIP       check a package: one folder, every file
#                                      listed in its SHA256SUMS and matching,
#                                      the files JukaHub needs present
#   build-runtimes.sh unpack ZIP DEST  verify, then put the package's folder at
#                                      DEST (the TrimUI archives' androidrt/)
#
# androidrt also cross-builds (CROSS=aarch64-linux-gnu-, as build-tsp.sh does);
# the loader cannot be started then, which BUILDINFO.txt says. wine builds on
# aarch64 only.
#
# Packages go to OUT_DIR (default ./bin, i.e. player/native/bin, where
# release.sh and package-sdcard.sh look for them). Every step writes its
# output to LOG_DIR (one log per step) and the console shows one line for it;
# a failed step prints the end of its log and stops, and the reason also goes
# to LOG_DIR/failure.txt, which the CI job puts in its summary. An earlier
# package in OUT_DIR is removed first, so a failed build never leaves one
# behind for the packaging scripts to ship.
#
# Needs: sh, readelf, sha256sum, zip, unzip, od, find and sort; androidrt also
# gcc and binutils (build-tsp.sh); wine also git, cmake, ninja or make,
# python3, strip and curl or wget. verify and unpack need only sha256sum, od
# and one of unzip, bsdtar, python3 or busybox.

set -eu

NATIVE_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PLAYER_DIR="$(dirname -- "$NATIVE_DIR")"
ANDROIDRT_DIR="$PLAYER_DIR/androidrt"
OUT_DIR="${OUT_DIR:-$NATIVE_DIR/bin}"
WORK_DIR="${WORK_DIR:-/tmp/jukahub-runtime-build}"
LOG_DIR="${LOG_DIR:-/tmp/jukahub-runtime-logs}"
# The newest glibc symbol version a binary may need: the build root's (2.31),
# below the Smart Pro's 2.33 (firmware 1.0.4 and later).
GLIBC_MAX="${GLIBC_MAX:-2.31}"
if [ -z "${JOBS:-}" ]; then
    JOBS="$(nproc 2>/dev/null || echo 4)"
fi
READELF="${READELF:-readelf}"
STRIP="${STRIP:-strip}"

ANDROIDRT_ZIP=JukaHub-AndroidRT-linux-arm64.zip
WINE_ZIP=JukaHub-Wine-linux-arm64.zip

# Box64, the x86_64 emulator the Windows-app runtime runs Wine with. The newest
# stable release, fetched by the full id of the commit its tag names ("Bumped
# version to v0.4.4", 2026-08-02) and checked against all of it, the way
# build-helpers.sh fetches chiaki-ng: no distribution's digest of the v0.4.4
# tarball could be confirmed when this was pinned, and a commit id pins every
# file of the tree anyway. A generic ARM64 build (box64's docs/COMPILE.md): ARM_DYNAREC,
# RelWithDebInfo. No BOX32: box64's README says Wine's WoW64 build runs 32-bit
# Windows programs in Box64-only environments, and that is the Wine below.
BOX64_REPO="${BOX64_REPO:-https://github.com/ptitSeb/box64.git}"
BOX64_TAG="${BOX64_TAG:-v0.4.4}"
BOX64_VERSION="${BOX64_VERSION:-0.4.4}"
BOX64_COMMIT="${BOX64_COMMIT:-2f130fab1d6e1a4ee8a71dc60cfdfcc839ad192a}"

# The Wine build the pack runs, recorded in wine.json for the handheld to
# download: Kron4ek's Wine 10.0 amd64 WoW64 build (64-bit Wine that runs 32-bit
# Windows programs without 32-bit Linux libraries). Wine 10.0 is pinned on
# purpose: Wine 11.x aborts on the handheld's 4.9 kernel. The digest is the one
# Kron4ek's release notes publish for this file, and an independent copy
# (termux-box-packages' "rootfs_wine_10.0", "pinned upstream build,
# unmodified") lists the same; the size is measured from the verified download.
WINE_VERSION="${WINE_VERSION:-10.0}"
WINE_TARBALL="${WINE_TARBALL:-wine-10.0-amd64-wow64.tar.xz}"
WINE_URL="${WINE_URL:-https://github.com/Kron4ek/Wine-Builds/releases/download/10.0/wine-10.0-amd64-wow64.tar.xz}"
WINE_SHA256="${WINE_SHA256:-aeebbbf239e548f0136f1cd72a2e109dd9a572a8f703da3c09fa40d74d5c255f}"

# Libraries a binary in either package may need: the C library and its parts,
# the loader and libgcc_s, which every handheld has. Anything else would mean a
# dependency was linked dynamically.
ALLOWED_NEEDED="libc.so.6 libm.so.6 libdl.so.2 libpthread.so.0 librt.so.1 libgcc_s.so.1 ld-linux-aarch64.so.1"

usage() {
    echo "usage: $0 androidrt | wine | wine-check [ZIP] | smoke | verify ZIP | unpack ZIP DEST" >&2
    exit 2
}

# ── Logging: one line per step, the log's end when a step fails ─────────

STAGE="setup"
# Set by start_logs: only a build keeps its reason for stopping there.
FAILURE_FILE=""
say() {
    STAGE="$*"
    printf '\n==> %s\n' "$*"
}
die() {
    printf 'build-runtimes: %s\n' "$*" >&2
    if [ -n "$FAILURE_FILE" ]; then
        printf 'build-runtimes: %s\n' "$*" >> "$FAILURE_FILE" 2>/dev/null || true
    fi
    exit 1
}
on_exit() {
    rc=$?
    if [ "$rc" -ne 0 ] && [ ! -s "$FAILURE_FILE" ]; then
        printf 'build-runtimes: stopped (exit %s) during "%s"; the job log above says why\n' "$rc" "$STAGE" \
            >> "$FAILURE_FILE" 2>/dev/null || true
    fi
}
# work_dirs creates LOG_DIR and WORK_DIR and makes them absolute: the steps
# below change directory.
work_dirs() {
    mkdir -p "$LOG_DIR" "$WORK_DIR"
    LOG_DIR="$(cd "$LOG_DIR" && pwd)"
    WORK_DIR="$(cd "$WORK_DIR" && pwd)"
}
# start_logs prepares a build: the work folders, OUT_DIR, fresh logs.
start_logs() {
    work_dirs
    mkdir -p "$OUT_DIR"
    OUT_DIR="$(cd "$OUT_DIR" && pwd)"
    rm -f "$LOG_DIR"/*.log "$LOG_DIR"/*.txt
    FAILURE_FILE="$LOG_DIR/failure.txt"
    trap on_exit EXIT
}

# The compiler probes a configure prints, left out of what a failure shows.
PROBES='^-- (Performing Test|Looking for|Check size of|Check if|Checking whether|Check whether|Detecting C|Check for working)'

show_failure() {
    echo "---- the end of $1 ----" >&2
    if grep -q '^FAILED: ' "$1"; then
        sed -n '/^FAILED: /,$p' "$1" | head -n 150 >&2
    else
        grep -Ev "$PROBES" "$1" | tail -n 60 >&2
    fi
}

# quietly NAME CMD... runs CMD with its output in LOG_DIR/NAME.log and prints
# one line for it; on failure it shows the end of that log, keeps it in
# LOG_DIR/failure.txt and stops. CMD must be one program (or env/sh running
# one), since it runs as an if condition, where set -e is off.
quietly() {
    q_name="$1"
    shift
    q_log="$LOG_DIR/$q_name.log"
    q_start="$(date +%s)"
    if "$@" > "$q_log" 2>&1; then
        echo "    $q_name: done in $(( $(date +%s) - q_start )) s"
        return 0
    fi
    show_failure "$q_log" 2>&1 | tee "${FAILURE_FILE:-/dev/null}" >&2
    die "$q_name failed (whole log: $q_log)"
}

# ── Small tools ──────────────────────────────────────────────────────────

sha256_of() {
    sha256sum "$1" | cut -d' ' -f1
}

size_of() {
    wc -c < "$1" | tr -d ' '
}

# version_le A B: A <= B for dotted version numbers (no sort -V: busybox).
version_le() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)" = "$2" ]
}

# elf_machine FILE prints the ELF e_machine bytes ("b7 00" is AArch64, "3e 00"
# x86-64), or nothing for a file that is not ELF. od, not readelf: verify and
# unpack run where no binutils are installed.
elf_machine() {
    if [ "$(od -An -tx1 -N4 "$1" 2>/dev/null | tr -d ' \n')" != "7f454c46" ]; then
        return 0
    fi
    od -An -tx1 -j18 -N2 "$1" | sed 's/^ *//; s/ *$//'
}

# glibc_max FILE prints the newest GLIBC_x.y version FILE needs.
glibc_max() {
    "$READELF" -V "$1" | grep -o 'GLIBC_[0-9][0-9.]*' | sed 's/^GLIBC_//' |
        sort -u -t. -k1,1n -k2,2n -k3,3n | tail -n 1
}

needed_libs() {
    "$READELF" -d "$1" | sed -n 's/.*(NEEDED).*\[\(.*\)\].*/\1/p'
}

interpreter() {
    "$READELF" -l "$1" | sed -n 's/.*Requesting program interpreter: \(.*\)\]$/\1/p'
}

# check_elf NAME FILE checks what a handheld binary must be: AArch64, the
# standard glibc loader, nothing NEEDED beyond ALLOWED_NEEDED, no glibc symbol
# version newer than GLIBC_MAX and none private to one glibc build. It prints
# the binary's BUILDINFO block.
check_elf() {
    ce_name="$1"
    ce_file="$2"
    [ -f "$ce_file" ] || die "$ce_name was not built"
    [ "$(elf_machine "$ce_file")" = "b7 00" ] || die "$ce_name is not an AArch64 ELF file"
    ce_interp="$(interpreter "$ce_file")"
    [ "$ce_interp" = /lib/ld-linux-aarch64.so.1 ] ||
        die "$ce_name asks for the loader '${ce_interp:-none}', not /lib/ld-linux-aarch64.so.1"
    ce_needed="$(needed_libs "$ce_file" | tr '\n' ' ' | sed 's/ $//')"
    for ce_lib in $ce_needed; do
        case " $ALLOWED_NEEDED " in
            *" $ce_lib "*) ;;
            *) die "$ce_name needs $ce_lib, which the handheld may not have: a dependency was linked dynamically" ;;
        esac
    done
    if "$READELF" -V "$ce_file" | grep -q 'GLIBC_PRIVATE'; then
        die "$ce_name needs GLIBC_PRIVATE symbols, which only the glibc it was built against has"
    fi
    ce_max="$(glibc_max "$ce_file")"
    if [ -z "$ce_max" ] || ! version_le "$ce_max" "$GLIBC_MAX"; then
        die "$ce_name needs GLIBC_${ce_max:-?}, newer than GLIBC_$GLIBC_MAX"
    fi
    echo ""
    echo "$ce_name"
    echo "  sha256:     $(sha256_of "$ce_file")"
    echo "  size:       $(size_of "$ce_file") bytes"
    echo "  needs:      $ce_needed"
    echo "  glibc max:  GLIBC_$ce_max (limit GLIBC_$GLIBC_MAX)"
}

# can_run_aarch64: whether this machine runs the binaries it builds.
can_run_aarch64() {
    case "$(uname -m)" in
        aarch64|arm64) return 0 ;;
    esac
    return 1
}

# start_once NAME OUT CMD... starts CMD once with an empty environment and every
# symbol bound up front, so a missing library or symbol fails here and not on
# the handheld. Any exit status of the program itself passes (a usage message
# is fine); the dynamic loader's errors, a crash, a hang or an exec failure
# do not. It prints "exit N: <first line of output>".
start_once() {
    so_name="$1"
    so_out="$2"
    shift 2
    so_rc=0
    if command -v timeout >/dev/null 2>&1; then
        timeout 30 env -i LD_BIND_NOW=1 "$@" > "$so_out" 2>&1 || so_rc=$?
    else
        env -i LD_BIND_NOW=1 "$@" > "$so_out" 2>&1 || so_rc=$?
    fi
    if grep -Eq 'error while loading shared libraries|symbol lookup error|version .GLIBC_|cannot execute binary file|Exec format error|required file not found' "$so_out"; then
        sed -n '1,10p' "$so_out" >&2
        die "$so_name does not start: the dynamic loader refused it"
    fi
    case "$so_rc" in
        124) die "$so_name did not finish within 30 s of starting" ;;
        126|127) sed -n '1,10p' "$so_out" >&2; die "$so_name could not be executed (exit $so_rc)" ;;
    esac
    if [ "$so_rc" -gt 128 ]; then
        sed -n '1,10p' "$so_out" >&2
        die "$so_name crashed when started (signal $((so_rc - 128)))"
    fi
    echo "exit $so_rc: $(sed -n '1p' "$so_out" | cut -c1-160)"
}

# download FILE SHA256 URL: FILE from URL, only if its content has that digest.
# A copy already there is reused when it matches.
download() {
    dl_dest="$1"
    dl_sum="$2"
    dl_url="$3"
    if [ -f "$dl_dest" ] && [ "$(sha256_of "$dl_dest")" = "$dl_sum" ]; then
        return 0
    fi
    rm -f "$dl_dest" "$dl_dest.part"
    echo "    fetching $dl_url"
    # Bounded: a stalled server must cost a retry, not the job's timeout.
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 3 --retry-delay 5 --connect-timeout 30 --max-time 900 -o "$dl_dest.part" "$dl_url" ||
            die "could not download $dl_url"
    else
        wget -q --tries=3 --timeout=30 -O "$dl_dest.part" "$dl_url" || die "could not download $dl_url"
    fi
    dl_have="$(sha256_of "$dl_dest.part")"
    if [ "$dl_have" != "$dl_sum" ]; then
        rm -f "$dl_dest.part"
        die "$dl_url has sha256 $dl_have, not the pinned $dl_sum"
    fi
    mv "$dl_dest.part" "$dl_dest"
}

# ── Packages: SHA256SUMS, zip, verify ────────────────────────────────────

# list_files DIR: every regular file under DIR, relative and sorted, except
# DIR/SHA256SUMS and the provenance file JukaHub writes into an installed copy.
list_files() {
    ( cd "$1" && find . -type f -print ) | sed 's|^\./||' |
        grep -vx -e 'SHA256SUMS' -e '\.jukahub-pack\.json' | LC_ALL=C sort
}

# write_sums DIR lists every file of the package folder DIR in DIR/SHA256SUMS.
# JukaHub reads "<sha256>  <path>" with exactly two fields, so a name with a
# space (or anything unusual) is refused here.
write_sums() {
    ws_dir="$1"
    list_files "$ws_dir" > "$WORK_DIR/package-files.txt"
    [ -s "$WORK_DIR/package-files.txt" ] || die "the package folder $ws_dir is empty"
    while IFS= read -r ws_f; do
        case "$ws_f" in
            *[!A-Za-z0-9._+/-]*) die "the package would hold a file whose name JukaHub's check cannot read: $ws_f" ;;
        esac
    done < "$WORK_DIR/package-files.txt"
    ( cd "$ws_dir" && while IFS= read -r ws_f; do sha256sum "$ws_f"; done ) \
        < "$WORK_DIR/package-files.txt" > "$ws_dir/SHA256SUMS.tmp"
    mv "$ws_dir/SHA256SUMS.tmp" "$ws_dir/SHA256SUMS"
}

# make_zip STAGE ROOT ZIP writes ZIP holding STAGE/ROOT as one folder, file
# modes kept (JukaHub's unzip keeps the executable bit), no links followed
# into anything else, no extra attributes (uid, times beyond the DOS stamp).
make_zip() {
    rm -f "$3" "$3.part"
    ( cd "$1" && zip -q -X -r "$3.part" "$2" ) || die "could not write $3"
    mv "$3.part" "$3"
}

# extract_zip ZIP DEST with whatever this machine has; prints the tool's name.
# Only unzip and bsdtar restore file modes.
extract_zip() {
    if command -v unzip >/dev/null 2>&1; then
        unzip -qq -o "$1" -d "$2" >&2 && echo unzip && return 0
        return 1
    fi
    if command -v bsdtar >/dev/null 2>&1; then
        bsdtar -xf "$1" -C "$2" >&2 && echo bsdtar && return 0
        return 1
    fi
    if command -v python3 >/dev/null 2>&1; then
        python3 -I -m zipfile -e "$1" "$2" >&2 && echo python3 && return 0
        return 1
    fi
    if command -v busybox >/dev/null 2>&1; then
        busybox unzip -q -o "$1" -d "$2" >&2 && echo busybox && return 0
        return 1
    fi
    echo "no unzip, bsdtar, python3 or busybox to unpack $1 with" >&2
    return 1
}

# The executables of each package: they must keep their mode in the zip.
package_executables() {
    case "$1" in
        androidrt) echo "loader launch.sh surface-probe" ;;
        wine-runtime) echo "box64/box64" ;;
    esac
}

# verify_tree TREE KIND checks an unpacked package folder; KIND is androidrt
# or wine-runtime. Prints why it fails and returns 1.
verify_tree() {
    vt_tree="$1"
    vt_kind="$2"
    vt_tmp="$3"
    if [ ! -f "$vt_tree/SHA256SUMS" ]; then
        echo "the package has no SHA256SUMS"
        return 1
    fi
    vt_bad="$(grep -Evn '^[0-9a-f]{64}  [A-Za-z0-9._+/-]+$' "$vt_tree/SHA256SUMS" | sed -n 1p)"
    if [ -n "$vt_bad" ]; then
        echo "SHA256SUMS has a line JukaHub cannot read: $vt_bad"
        return 1
    fi
    vt_odd="$(find "$vt_tree" ! -type f ! -type d -print | sed -n 1p)"
    if [ -n "$vt_odd" ]; then
        echo "the package holds something that is not a regular file: ${vt_odd#"$vt_tree"/}"
        return 1
    fi
    list_files "$vt_tree" > "$vt_tmp/have.txt"
    sed 's/^[0-9a-f]*  //' "$vt_tree/SHA256SUMS" | LC_ALL=C sort > "$vt_tmp/listed.txt"
    if [ -n "$(uniq -d "$vt_tmp/listed.txt")" ]; then
        echo "SHA256SUMS lists a file twice: $(uniq -d "$vt_tmp/listed.txt" | sed -n 1p)"
        return 1
    fi
    if ! cmp -s "$vt_tmp/have.txt" "$vt_tmp/listed.txt"; then
        vt_extra="$(comm -23 "$vt_tmp/have.txt" "$vt_tmp/listed.txt" | sed -n 1p)"
        vt_gone="$(comm -13 "$vt_tmp/have.txt" "$vt_tmp/listed.txt" | sed -n 1p)"
        if [ -n "$vt_extra" ]; then
            echo "$vt_extra is not in SHA256SUMS"
        else
            echo "SHA256SUMS lists $vt_gone, which the package does not have"
        fi
        return 1
    fi
    if ! ( cd "$vt_tree" && sha256sum -c SHA256SUMS > "$vt_tmp/check.txt" 2>&1 ); then
        echo "a file does not match SHA256SUMS: $(grep -v ': OK$' "$vt_tmp/check.txt" | sed -n 1p)"
        return 1
    fi
    case "$vt_kind" in
        androidrt) vt_need="loader launch.sh surface-probe props.txt.example BUILDINFO.txt" ;;
        wine-runtime) vt_need="box64/box64 wine.json BUILDINFO.txt" ;;
        *) echo "unknown package folder $vt_kind"; return 1 ;;
    esac
    for vt_f in $vt_need; do
        if [ ! -s "$vt_tree/$vt_f" ]; then
            echo "the package has no $vt_f"
            return 1
        fi
    done
    for vt_f in $(package_executables "$vt_kind"); do
        if [ "$(elf_machine "$vt_tree/$vt_f")" != "" ] && [ "$(elf_machine "$vt_tree/$vt_f")" != "b7 00" ]; then
            echo "$vt_f is not an AArch64 program"
            return 1
        fi
    done
    case "$vt_kind" in
        androidrt)
            [ "$(elf_machine "$vt_tree/loader")" = "b7 00" ] || { echo "loader is not an AArch64 ELF program"; return 1; }
            ;;
        wine-runtime)
            [ "$(elf_machine "$vt_tree/box64/box64")" = "b7 00" ] || { echo "box64/box64 is not an AArch64 ELF program"; return 1; }
            if [ -z "$(find "$vt_tree/box64/x64lib" -type f -print 2>/dev/null | sed -n 1p)" ]; then
                echo "the package has no box64/x64lib libraries"
                return 1
            fi
            for vt_key in '"version"' '"asset"' '"url"' '"sha256"' '"size"'; do
                if ! grep -q "$vt_key" "$vt_tree/wine.json"; then
                    echo "wine.json has no $vt_key"
                    return 1
                fi
            done
            ;;
    esac
    return 0
}

# check_zip ZIP DIR unpacks ZIP into the empty folder DIR and checks it; on
# success it prints the package folder's name (androidrt or wine-runtime).
check_zip() {
    cz_zip="$1"
    cz_dir="$2"
    if [ ! -f "$cz_zip" ]; then
        echo "no package at $cz_zip" >&2
        return 1
    fi
    mkdir -p "$cz_dir/x" "$cz_dir/work"
    # Absolute: verify_tree changes into the package folder.
    cz_dir="$(cd "$cz_dir" && pwd)"
    if ! cz_tool="$(extract_zip "$cz_zip" "$cz_dir/x")"; then
        echo "$cz_zip does not unpack" >&2
        return 1
    fi
    cz_top="$(ls -A "$cz_dir/x")"
    case "$cz_top" in
        androidrt|wine-runtime) ;;
        *)
            echo "$cz_zip must hold one folder, androidrt/ or wine-runtime/; it holds: $(echo "$cz_top" | tr '\n' ' ')" >&2
            return 1
            ;;
    esac
    if ! cz_why="$(verify_tree "$cz_dir/x/$cz_top" "$cz_top" "$cz_dir/work")"; then
        echo "$cz_zip: $cz_why" >&2
        return 1
    fi
    for cz_exe in $(package_executables "$cz_top"); do
        case "$cz_tool" in
            unzip|bsdtar)
                if [ ! -x "$cz_dir/x/$cz_top/$cz_exe" ]; then
                    echo "$cz_zip: $cz_top/$cz_exe lost its executable bit in the zip" >&2
                    return 1
                fi
                ;;
            *) chmod 755 "$cz_dir/x/$cz_top/$cz_exe" ;;
        esac
    done
    echo "$cz_top"
}

verify_zip() {
    vz_zip="$1"
    vz_tmp="$(mktemp -d "${TMPDIR:-/tmp}/jukahub-verify.XXXXXX")" || exit 1
    vz_rc=0
    if vz_kind="$(check_zip "$vz_zip" "$vz_tmp")"; then
        echo "$vz_zip: $vz_kind/ verified ($(wc -l < "$vz_tmp/x/$vz_kind/SHA256SUMS" | tr -d ' ') files match SHA256SUMS)"
    else
        vz_rc=1
    fi
    rm -rf "$vz_tmp"
    return "$vz_rc"
}

# unpack_zip ZIP DEST: the checked package folder becomes DEST, replacing what
# was there. Unpacked beside DEST, so the last step is one rename.
unpack_zip() {
    uz_zip="$1"
    uz_dest="$2"
    uz_parent="$(dirname -- "$uz_dest")"
    mkdir -p "$uz_parent"
    uz_tmp="$(mktemp -d "$uz_parent/.jukahub-unpack.XXXXXX")" || exit 1
    if ! uz_kind="$(check_zip "$uz_zip" "$uz_tmp")"; then
        rm -rf "$uz_tmp"
        return 1
    fi
    rm -rf "$uz_dest"
    if ! mv "$uz_tmp/x/$uz_kind" "$uz_dest"; then
        rm -rf "$uz_tmp"
        echo "could not move the package into $uz_dest" >&2
        return 1
    fi
    rm -rf "$uz_tmp"
    echo "$uz_zip: $uz_kind/ verified and unpacked into $uz_dest"
}

# ── Provenance ───────────────────────────────────────────────────────────

source_commit() {
    if [ -n "${SOURCE_COMMIT:-}" ]; then
        echo "$SOURCE_COMMIT"
    else
        git -C "$PLAYER_DIR" rev-parse HEAD 2>/dev/null || echo unknown
    fi
}

source_line() {
    sl_commit="$(source_commit)"
    if [ -n "${SOURCE_KEY:-}" ]; then
        echo "source:     JukaHub @ $sl_commit (sources $SOURCE_KEY)"
    else
        echo "source:     JukaHub @ $sl_commit"
    fi
}

# ── AndroidRT ────────────────────────────────────────────────────────────

build_androidrt() {
    stage="$WORK_DIR/androidrt-stage"
    tree="$stage/androidrt"
    rm -f "$OUT_DIR/$ANDROIDRT_ZIP"
    rm -rf "$stage"
    mkdir -p "$stage"

    say "AndroidRT (logs in $LOG_DIR)"
    commit="$(source_commit)"
    # What `loader --version` names: the JukaHub commit it was built from.
    version="${ANDROIDRT_VERSION:-}"
    if [ -z "$version" ]; then
        case "$commit" in
            unknown|'') version=dev ;;
            *) version="$(printf '%s' "$commit" | cut -c1-12)" ;;
        esac
    fi
    quietly androidrt-build env ANDROIDRT_OUT="$tree" ANDROIDRT_VERSION="$version" \
        sh "$ANDROIDRT_DIR/build-tsp.sh"
    cc_used="$(sed -n 's/^== AndroidRT .*(CC=\(.*\)) ==$/\1/p' "$LOG_DIR/androidrt-build.log" | sed -n 1p)"

    say "Checking AndroidRT"
    for f in loader launch.sh surface-probe props.txt.example; do
        [ -f "$tree/$f" ] || die "build-tsp.sh did not produce $f"
    done
    # NDK code reads its stack canary at tpidr_el0 + 0x28, which the runner
    # pins with apak_tls (build-tsp.sh refuses another offset; here a missing
    # symbol is said too).
    off="$("$READELF" -sW "$tree/loader" | awk '$NF == "apak_tls" { print $2; exit }')"
    case "$off" in
        0000000000000000) tls="apak_tls at offset 0 of the static TLS block (the NDK stack canary at tpidr_el0+0x28)" ;;
        "") tls="NOT VERIFIED: apak_tls is not in the loader's symbol table"
            echo "warning: $tls" >&2 ;;
        *) die "apak_tls is at TLS offset 0x$off, not 0: NDK code built with -fstack-protector would abort" ;;
    esac
    loader_block="$(check_elf loader "$tree/loader")"
    probe_block="$(check_elf surface-probe "$tree/surface-probe")"
    sh -n "$tree/launch.sh" || die "launch.sh is not a valid shell script"
    if can_run_aarch64; then
        start_loader="$(start_once loader "$WORK_DIR/androidrt-start.txt" "$tree/loader" --version)"
        start_probe="$(start_once surface-probe "$WORK_DIR/androidrt-probe.txt" "$tree/surface-probe")"
    else
        start_loader="not started: cross-built on $(uname -m)"
        start_probe="$start_loader"
    fi
    echo "    loader --version: $start_loader"
    echo "    surface-probe:    $start_probe"

    {
        echo "JukaHub AndroidRT (the Android-app runtime, player/androidrt)"
        echo "built:      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
        source_line
        echo "version:    $version (what loader --version prints)"
        echo "compiler:   ${cc_used:-unknown}: $(${cc_used:-cc} --version 2>/dev/null | sed -n 1p)"
        echo "build libc: $(ldd --version 2>/dev/null | sed -n 1p)"
        echo "TLS:        $tls"
        echo "started:    loader --version -> $start_loader"
        echo "            surface-probe -> $start_probe"
        echo "$loader_block"
        echo "$probe_block"
    } > "$tree/BUILDINFO.txt"
    chmod 755 "$tree/loader" "$tree/launch.sh" "$tree/surface-probe"
    chmod 644 "$tree/props.txt.example" "$tree/BUILDINFO.txt"

    say "Packaging $ANDROIDRT_ZIP"
    write_sums "$tree"
    make_zip "$stage" androidrt "$stage/$ANDROIDRT_ZIP"
    verify_zip "$stage/$ANDROIDRT_ZIP" > "$LOG_DIR/androidrt-verify.log" 2>&1 ||
        { cat "$LOG_DIR/androidrt-verify.log" >&2; die "the package just written does not verify"; }
    mv "$stage/$ANDROIDRT_ZIP" "$OUT_DIR/$ANDROIDRT_ZIP"
    cat "$tree/BUILDINFO.txt"
    echo ""
    echo "Wrote $OUT_DIR/$ANDROIDRT_ZIP ($(size_of "$OUT_DIR/$ANDROIDRT_ZIP") bytes)"
}

# The host smoke suite (../androidrt/tests/smoke) on this machine. Advisory in
# CI: it was written on x86_64, so a failure on arm64 is information, never a
# reason to hold back a package. LOG_DIR/smoke-summary.txt has the result.
smoke() {
    work_dirs
    sm_log="$LOG_DIR/smoke.log"
    sm_rc=0
    SMOKE_WORK="${SMOKE_WORK:-$WORK_DIR/androidrt-smoke}" \
        sh "$ANDROIDRT_DIR/tests/smoke/run-smoke.sh" > "$sm_log" 2>&1 || sm_rc=$?
    sm_line="$(grep -E '^== smoke:' "$sm_log" | tail -n 1)"
    {
        if [ -n "$sm_line" ]; then
            echo "$sm_line (exit $sm_rc)"
        else
            echo "the suite stopped before its summary line (exit $sm_rc); the end of its output:"
            tail -n 30 "$sm_log"
        fi
        grep -E '^FAIL' "$sm_log" | head -n 40
    } > "$LOG_DIR/smoke-summary.txt"
    cat "$LOG_DIR/smoke-summary.txt"
    return "$sm_rc"
}

# ── Box64 + wine.json ────────────────────────────────────────────────────

fetch_box64() {
    src="$1"
    if [ ! -d "$src/.git" ]; then
        rm -rf "$src"
        mkdir -p "$src"
        git -C "$src" init -q
        git -C "$src" remote add origin "$BOX64_REPO"
    fi
    if [ "$(git -C "$src" rev-parse HEAD 2>/dev/null || true)" != "$BOX64_COMMIT" ]; then
        quietly box64-fetch git -C "$src" fetch -q --depth 1 origin "$BOX64_COMMIT"
        quietly box64-checkout git -C "$src" checkout -q --detach "$BOX64_COMMIT"
    fi
    git -C "$src" reset -q --hard "$BOX64_COMMIT"
    git -C "$src" clean -q -fdx
    [ "$(git -C "$src" rev-parse HEAD)" = "$BOX64_COMMIT" ] || die "the box64 checkout is not at $BOX64_COMMIT"
}

# tag_check prints whether the upstream tag still names the pinned commit. Only
# a note: the build uses the commit either way.
tag_check() {
    tc_refs="$(git ls-remote "$BOX64_REPO" "refs/tags/$BOX64_TAG" "refs/tags/$BOX64_TAG^{}" 2>/dev/null || true)"
    tc_peeled="$(printf '%s\n' "$tc_refs" | awk -v t="refs/tags/$BOX64_TAG^{}" '$2 == t { print $1 }')"
    tc_plain="$(printf '%s\n' "$tc_refs" | awk -v t="refs/tags/$BOX64_TAG" '$2 == t { print $1 }')"
    tc_id="${tc_peeled:-$tc_plain}"
    if [ -z "$tc_id" ]; then
        echo "could not read $BOX64_TAG upstream"
    elif [ "$tc_id" = "$BOX64_COMMIT" ]; then
        echo "upstream $BOX64_TAG names this commit"
    else
        echo "WARNING: upstream $BOX64_TAG now names $tc_id; this build used the pinned commit"
    fi
}

build_wine_pack() {
    can_run_aarch64 || die "the Box64 pack is built natively on aarch64 Linux (this is $(uname -m))"
    src="$WORK_DIR/box64-src"
    build="$WORK_DIR/box64-build"
    inst="$WORK_DIR/box64-install"
    stage="$WORK_DIR/wine-stage"
    tree="$stage/wine-runtime"
    rm -f "$OUT_DIR/$WINE_ZIP"
    rm -rf "$stage" "$build" "$inst"
    mkdir -p "$tree/box64/x64lib"

    say "Box64 $BOX64_TAG ($BOX64_COMMIT, logs in $LOG_DIR)"
    fetch_box64 "$src"
    tags="$(tag_check)"
    echo "    $tags"
    tree_id="$(git -C "$src" rev-parse 'HEAD^{tree}')"
    # gcc 10 when it is installed (the CI job adds it from Ubuntu 20.04's
    # universe): nearer than 20.04's gcc 9 to the compilers box64 is developed
    # with. No -march or -mcpu here: box64's generic ARM64 build, which the
    # Smart Pro's Cortex-A53 runs.
    cc="${BOX64_CC:-}"
    if [ -z "$cc" ]; then
        if command -v gcc-10 >/dev/null 2>&1; then cc=gcc-10; else cc=cc; fi
    fi
    if command -v ninja >/dev/null 2>&1; then gen=Ninja; else gen="Unix Makefiles"; fi
    quietly box64-configure cmake -S "$src" -B "$build" -G "$gen" \
        -DARM_DYNAREC=ON -DCMAKE_BUILD_TYPE=RelWithDebInfo \
        -DCMAKE_C_COMPILER="$cc" -DCMAKE_POLICY_VERSION_MINIMUM=3.5
    echo "    configured, after $(grep -cE "$PROBES" "$LOG_DIR/box64-configure.log") compiler probes"
    quietly box64-build cmake --build "$build" -j "$JOBS"
    # What box64's own install rules put in place, x86_64 libraries included,
    # into a staging root: those are exactly what it means to find beside
    # itself (BOX64_LD_LIBRARY_PATH on the handheld).
    inst_note="installed by box64's install rules"
    if ! DESTDIR="$inst" cmake --install "$build" > "$LOG_DIR/box64-install.log" 2>&1; then
        inst_note="box64's install step failed (box64-install.log); copied from its source tree's x64lib/"
        echo "    warning: $inst_note" >&2
    fi

    say "Staging the Box64 pack"
    bin="$build/box64"
    [ -f "$bin" ] || bin="$(find "$inst" -type f -name box64 -print 2>/dev/null | sed -n 1p)"
    [ -n "$bin" ] && [ -f "$bin" ] || die "the box64 build left no box64 binary"
    cp "$bin" "$tree/box64/box64"
    # RelWithDebInfo's DWARF is most of the file: dropped, the symbol table
    # (function names in box64's crash reports) kept.
    "$STRIP" --strip-debug "$tree/box64/box64" || die "could not strip box64"
    chmod 755 "$tree/box64/box64"
    libs="$(find "$inst" -type f -path '*/box64-x86_64-linux-gnu/*' -name '*.so*' -print 2>/dev/null | LC_ALL=C sort)"
    if [ -z "$libs" ]; then
        libs="$(find "$src/x64lib" -maxdepth 1 -type f -name '*.so*' -print 2>/dev/null | LC_ALL=C sort)"
        [ -n "$libs" ] || die "found no x86_64 libraries in box64's install or its x64lib/ folder"
        case "$inst_note" in
            installed*) inst_note="box64's install put no libraries in a box64-x86_64-linux-gnu folder; copied from its source tree's x64lib/" ;;
        esac
    fi
    for lib in $libs; do
        name="$(basename "$lib")"
        [ ! -e "$tree/box64/x64lib/$name" ] || die "two x86_64 libraries are named $name"
        cp -L "$lib" "$tree/box64/x64lib/$name"
        chmod 644 "$tree/box64/x64lib/$name"
        [ "$(elf_machine "$tree/box64/x64lib/$name")" = "3e 00" ] || die "x64lib/$name is not an x86-64 ELF file"
    done
    # The default per-program settings box64 installs as /etc/box64.box64rc;
    # the handheld has no /etc copy, so JukaHub can name this one with
    # BOX64_RCFILE. And box64's licence (MIT), which a copy must carry.
    rc="$(find "$inst" -type f -name 'box64.box64rc' -print 2>/dev/null | sed -n 1p)"
    [ -n "$rc" ] || rc="$(find "$src/system" -maxdepth 1 -type f -name 'box64.box64rc' -print 2>/dev/null | sed -n 1p)"
    if [ -n "$rc" ]; then
        cp "$rc" "$tree/box64/box64.box64rc"
        chmod 644 "$tree/box64/box64.box64rc"
    fi
    if [ -f "$src/LICENSE" ]; then
        cp "$src/LICENSE" "$tree/box64/LICENSE"
        chmod 644 "$tree/box64/LICENSE"
    fi
    arch_flags="$(cat "$build"/CMakeFiles/box64.dir/flags.make "$build"/build.ninja 2>/dev/null |
        grep -oE -- '-m(arch|cpu|tune)=[^ ]+' | LC_ALL=C sort -u | tr '\n' ' ' | sed 's/ $//')"
    case " $arch_flags " in
        *native*) die "box64 was compiled for the build machine's own CPU ($arch_flags), which the handheld's may not run" ;;
    esac

    say "Checking Box64"
    box_block="$(check_elf box64/box64 "$tree/box64/box64")"
    version_out="$WORK_DIR/box64-version.txt"
    start_box="$(start_once box64 "$version_out" "$tree/box64/box64" --version)"
    case "$start_box" in
        "exit 0:"*) ;;
        *) sed -n '1,10p' "$version_out" >&2; die "box64 --version did not succeed ($start_box)" ;;
    esac
    box_version="$(grep -F -m1 "$BOX64_VERSION" "$version_out" | cut -c1-200)"
    if [ -z "$box_version" ]; then
        sed -n '1,10p' "$version_out" >&2
        die "box64 --version does not name version $BOX64_VERSION: this is not the pinned release"
    fi
    echo "    box64 --version: $box_version"

    say "Wine $WINE_VERSION ($WINE_TARBALL)"
    download "$WORK_DIR/$WINE_TARBALL" "$WINE_SHA256" "$WINE_URL"
    wine_size="$(size_of "$WORK_DIR/$WINE_TARBALL")"
    echo "    $WINE_TARBALL: sha256 $WINE_SHA256, $wine_size bytes"
    printf '{"version":"%s","asset":"%s","url":"%s","sha256":"%s","size":%s}\n' \
        "$WINE_VERSION" "$WINE_TARBALL" "$WINE_URL" "$WINE_SHA256" "$wine_size" > "$tree/wine.json"
    chmod 644 "$tree/wine.json"

    {
        echo "JukaHub Windows-app runtime: Box64 (and the Wine build it runs, in wine.json)"
        echo "built:      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
        source_line
        echo "box64:      $BOX64_REPO $BOX64_TAG @ $BOX64_COMMIT (fetched by commit id; tree $tree_id)"
        echo "            $tags"
        echo "options:    -DARM_DYNAREC=ON -DCMAKE_BUILD_TYPE=RelWithDebInfo (generic ARM64; no BOX32: Wine's"
        echo "            WoW64 build runs 32-bit Windows programs under Box64 alone), DWARF stripped"
        # (No apostrophe inside ${...:-...}: bash would look for its pair.)
        echo "arch flags: ${arch_flags:-none, so the compiler default: generic armv8-a}"
        echo "compiler:   $cc: $($cc --version 2>/dev/null | sed -n 1p)"
        echo "build libc: $(ldd --version 2>/dev/null | sed -n 1p)"
        echo "started:    box64 --version -> $box_version"
        echo "x64lib:     $inst_note; the handheld points BOX64_LD_LIBRARY_PATH here"
        echo "wine:       Kron4ek/Wine-Builds $WINE_VERSION, $WINE_TARBALL ($WINE_URL)"
        echo "            sha256 $WINE_SHA256 (pinned: Kron4ek's release notes; the same in"
        echo "            termux-box-packages' unmodified copy), $wine_size bytes (measured)"
        echo "            Wine $WINE_VERSION on purpose: Wine 11.x aborts on the handheld's 4.9 kernel"
        echo "$box_block"
        echo ""
        echo "box64/x64lib"
        for lib in "$tree"/box64/x64lib/*; do
            echo "  $(basename "$lib"): sha256 $(sha256_of "$lib"), $(size_of "$lib") bytes"
        done
        if [ -f "$tree/box64/box64.box64rc" ]; then
            echo ""
            echo "box64/box64.box64rc: box64's default per-program settings (BOX64_RCFILE)"
        fi
    } > "$tree/BUILDINFO.txt"
    chmod 644 "$tree/BUILDINFO.txt"

    say "Packaging $WINE_ZIP"
    write_sums "$tree"
    make_zip "$stage" wine-runtime "$stage/$WINE_ZIP"
    verify_zip "$stage/$WINE_ZIP" > "$LOG_DIR/wine-verify.log" 2>&1 ||
        { cat "$LOG_DIR/wine-verify.log" >&2; die "the package just written does not verify"; }
    mv "$stage/$WINE_ZIP" "$OUT_DIR/$WINE_ZIP"
    # The build tree (debug info included) is no longer needed; the staged
    # pack stays for wine-check.
    rm -rf "$build" "$inst"
    cat "$tree/BUILDINFO.txt"
    echo ""
    echo "Wrote $OUT_DIR/$WINE_ZIP ($(size_of "$OUT_DIR/$WINE_ZIP") bytes)"
}

# Advisory: does the pinned Wine start under this Box64 on aarch64 Linux? The
# pack from `wine` (or ZIP) and the Wine tarball (downloaded again if it is not
# in WORK_DIR) are unpacked and `box64 wine --version` runs once, bounded.
# LOG_DIR/wine-check.txt has the result.
wine_check() {
    work_dirs
    can_run_aarch64 || die "the Wine check runs on aarch64 Linux (this is $(uname -m))"
    check="$WORK_DIR/wine-check"
    rm -rf "$check"
    mkdir -p "$check/home" "$check/wine"
    pack="$WORK_DIR/wine-stage/wine-runtime"
    if [ -n "${1:-}" ]; then
        unpack_zip "$1" "$check/wine-runtime" > /dev/null || die "the pack $1 does not verify"
        pack="$check/wine-runtime"
    fi
    [ -x "$pack/box64/box64" ] || die "no Box64 pack at $pack (run '$0 wine' first, or name its zip)"
    download "$WORK_DIR/$WINE_TARBALL" "$WINE_SHA256" "$WINE_URL"
    if command -v timeout >/dev/null 2>&1; then to="timeout 300"; else to=""; fi
    $to tar -xJf "$WORK_DIR/$WINE_TARBALL" -C "$check/wine" || die "could not unpack $WINE_TARBALL"
    winedir="$(find "$check/wine" -mindepth 1 -maxdepth 1 -type d -print | sed -n 1p)"
    [ -n "$winedir" ] && [ -f "$winedir/bin/wine" ] || die "$WINE_TARBALL holds no bin/wine"
    unpacked_kib="$(du -sk "$winedir" | cut -f1)"
    out="$LOG_DIR/wine-check.log"
    rc=0
    if command -v timeout >/dev/null 2>&1; then to="timeout 180"; else to=""; fi
    # BOX64_LOG=1: box64 says which libraries it wraps natively and which it
    # emulates, which is what a failure here needs.
    $to env -i PATH=/usr/bin:/bin HOME="$check/home" LANG=C.UTF-8 \
        BOX64_LD_LIBRARY_PATH="$pack/box64/x64lib" BOX64_LOG=1 WINEDEBUG=-all \
        "$pack/box64/box64" "$winedir/bin/wine" --version > "$out" 2>&1 || rc=$?
    printed="$(grep -oE 'wine-[0-9][0-9A-Za-z.+~-]*' "$out" | tail -n 1)"
    {
        if [ "$printed" = "wine-$WINE_VERSION" ]; then
            echo "box64 $(basename "$winedir")/bin/wine --version printed $printed (exit $rc): this Box64 starts Wine $WINE_VERSION on aarch64 Linux."
        else
            echo "box64 $(basename "$winedir")/bin/wine --version did not print wine-$WINE_VERSION (exit $rc${printed:+, printed $printed}); the end of its output:"
            tail -n 25 "$out"
        fi
        echo "Wine $WINE_VERSION unpacks to $((unpacked_kib / 1024)) MiB."
    } > "$LOG_DIR/wine-check.txt"
    cat "$LOG_DIR/wine-check.txt"
    # Hundreds of MiB the runner's disk can use for something else.
    rm -rf "$check/wine"
    [ "$printed" = "wine-$WINE_VERSION" ]
}

case "${1:-}:$#" in
    androidrt:1) start_logs; build_androidrt ;;
    wine:1)
        # Refused before anything is created or removed.
        can_run_aarch64 || die "the Box64 pack is built natively on aarch64 Linux (this is $(uname -m))"
        start_logs
        build_wine_pack
        ;;
    wine-check:1) wine_check ;;
    wine-check:2) wine_check "$2" ;;
    smoke:1) smoke ;;
    verify:2) verify_zip "$2" ;;
    unpack:3) unpack_zip "$2" "$3" ;;
    *) usage ;;
esac
