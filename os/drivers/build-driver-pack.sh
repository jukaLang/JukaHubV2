#!/usr/bin/env bash
# ---------------------------------------------------------------------------
#  Build a JukaHub OS kernel-module layer for ONE TrimUI handheld and kernel.
#
#  A module loads only into the kernel it was built for. This script builds
#  the candidate modules in modules.txt against a kernel tree configured with
#  the DEVICE'S OWN configuration (from dump-kernel-facts.sh), and stops
#  rather than ship something that cannot be trusted. What has to match, and
#  what is checked here:
#    - the release (uname -r): the tree must build exactly that release;
#    - the configuration: the device's config.gz, and enabling a candidate
#      must not change anything built into the kernel (else it is skipped);
#    - vermagic ("4.9.191 SMP preempt mod_unload aarch64"): compared with
#      the stock module's from the facts;
#    - MODVERSIONS symbol CRCs, when the kernel uses them: every symbol a
#      built module shares with the saved stock modules must have the same
#      CRC. A different CRC means the tree is not the source the firmware
#      was built from (TrimUI has published none for these models), and the
#      kernel would refuse the module ("disagrees about version of symbol");
#    - the compiler: only warned about (the facts' GCC vs the cross GCC).
#  JukaHub then checks vermagic again on the device and loads nothing that
#  does not fit (player/jukaos.go), and the layer itself names its model and
#  kernel ("devices", "kernel"), so it is refused on any other handheld.
#
#  Usage:
#    build-driver-pack.sh --facts DIR --kernel-src DIR --out DIR [--jobs N]
#  Environment: CROSS_COMPILE (default aarch64-linux-gnu-), MODULES_LIST
#  (default modules.txt beside this script), ALLOW_CONFIG_DRIFT=1 (carry on
#  when the tree's olddefconfig changes built-in options; for experiments).
#  Output: DIR/drivers-<device>-<release>/ (layer.json, modules/, BUILD-INFO.txt)
#  - copy it into Apps/JukaHub/os/layers/ on the card.
# ---------------------------------------------------------------------------
set -euo pipefail
export LC_ALL=C

die() { echo "build-driver-pack: $*" >&2; exit 1; }
note() { echo "build-driver-pack: $*"; INFO+=("$*"); }
INFO=()

FACTS="" KSRC="" OUT="" JOBS=$(nproc 2>/dev/null || echo 2)
while [ $# -gt 0 ]; do
    case "$1" in
        --facts) FACTS=$2; shift 2 ;;
        --kernel-src) KSRC=$2; shift 2 ;;
        --out) OUT=$2; shift 2 ;;
        --jobs) JOBS=$2; shift 2 ;;
        *) die "unknown argument $1" ;;
    esac
done
[ -n "$FACTS" ] && [ -n "$KSRC" ] && [ -n "$OUT" ] || die "usage: $0 --facts DIR --kernel-src DIR --out DIR"
HERE=$(cd "$(dirname "$0")" && pwd)
LIST=${MODULES_LIST:-$HERE/modules.txt}
CROSS=${CROSS_COMPILE:-aarch64-linux-gnu-}
for tool in make zcat modinfo modprobe "${CROSS}gcc" "${CROSS}strip"; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is needed"
done

# ------------------------------------------------------------- the facts
fact() { sed -n "s/^$1=//p" "$FACTS/facts.txt" | head -n 1; }
[ -f "$FACTS/facts.txt" ] || die "no facts.txt in $FACTS (run dump-kernel-facts.sh on the device)"
[ "$(fact FORMAT)" = jukahub-kernel-facts-v1 ] || die "$FACTS/facts.txt is not from dump-kernel-facts.sh"
RELEASE=$(fact RELEASE) MODEL=$(fact MODEL) DEVICE=$(fact DEVICE) VERMAGIC=$(fact VERMAGIC)
[[ "$RELEASE" =~ ^[A-Za-z0-9._+-]+$ ]] || die "odd kernel release '$RELEASE'"
[[ "$DEVICE" =~ ^trimui-[a-z-]+$ ]] || die "the facts do not name a TrimUI model (DEVICE=$DEVICE)"
[ "$(fact MACHINE)" = aarch64 ] || die "only aarch64 kernels are supported"
[ -f "$FACTS/config.gz" ] || die "the device's kernel exposes no config.gz: without its exact configuration no module can be built"
[ "$(fact CONFIG_MODULES)" = y ] || die "the device's kernel cannot load modules (CONFIG_MODULES=$(fact CONFIG_MODULES))"
[ "$(fact CONFIG_MODULE_SIG_FORCE)" != y ] || die "the device's kernel loads only modules signed with TrimUI's key"
MODVERSIONS=$(fact CONFIG_MODVERSIONS)

K=$(cd "$KSRC" && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/driver-pack.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
mk() { make --no-print-directory -C "$K" ARCH=arm64 CROSS_COMPILE="$CROSS" LOCALVERSION= "$@"; }
yset() { grep -E '^CONFIG_[A-Za-z0-9_]+=y$' "$1" | sort || :; }
# Every value the built-in kernel is made with (numbers and strings too:
# NR_CPUS, HZ, ...), not the modules (=m) nor what only names the compiler.
kset() {
    grep -E '^CONFIG_[A-Za-z0-9_]+=' "$1" | grep -v -E '=m$' |
        grep -v -E '^CONFIG_(CC_VERSION_TEXT|GCC_VERSION|CLANG_VERSION|AS_VERSION|LD_VERSION|LLD_VERSION)=' | sort || :
}
# (diff exits 1 on a difference: `|| :` keeps set -e/pipefail from ending the
# script inside these $(...) before the message.)

# ------------------------------------------------- the device's own config
zcat "$FACTS/config.gz" >"$WORK/device.config"
cp "$WORK/device.config" "$K/.config"
mk -s olddefconfig >/dev/null
cp "$K/.config" "$WORK/base.config"
built=$(mk -s kernelrelease)
[ "$built" = "$RELEASE" ] || die "this tree builds kernel $built, the device runs $RELEASE: use the device's own kernel source"
if ! diff -q <(kset "$WORK/device.config") <(kset "$WORK/base.config") >/dev/null; then
    drift=$(diff <(kset "$WORK/device.config") <(kset "$WORK/base.config") | grep '^[<>]' | head -n 12 | tr '\n' ' ' || :)
    [ "${ALLOW_CONFIG_DRIFT:-0}" = 1 ] || die "this tree does not take the device's config as it is (built-in options differ: $drift): it is not the firmware's source"
    note "WARNING: built-in options differ from the device's ($drift)"
fi
want_gcc=$(fact GCC | grep -o '[0-9][0-9]*[.][0-9][0-9]*' | head -n 1 || :)
have_gcc=$("${CROSS}gcc" -dumpfullversion -dumpversion 2>/dev/null | head -n 1 || :)
[ "${want_gcc%%.*}" = "${have_gcc%%.*}" ] || note "WARNING: the kernel was built with gcc ${want_gcc:-?}, this is ${CROSS}gcc $have_gcc"

# ----------------------------------------------------- enable the candidates
kver=$(mk -s kernelversion)
ver_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" = "$2" ]; }
WANT=()
while read -r mod sym since _; do
    case "$mod" in "" | \#*) continue ;; esac
    have=$(sed -n "s/^$sym=//p" "$WORK/device.config")
    if [ "$have" = y ] || [ "$have" = m ]; then
        note "$mod: the firmware's kernel has it already ($sym=$have)"; continue
    fi
    if ! ver_ge "$kver" "$since"; then
        note "$mod: not in a $kver kernel (in the tree since $since)"; continue
    fi
    cp "$K/.config" "$WORK/before.config"
    "$K/scripts/config" --file "$K/.config" --module "${sym#CONFIG_}"
    mk -s olddefconfig >/dev/null
    if [ "$(sed -n "s/^$sym=//p" "$K/.config")" != m ]; then
        note "$mod: $sym cannot be a module in this tree (missing dependencies)"
        cp "$WORK/before.config" "$K/.config"; continue
    fi
    if ! diff -q <(yset "$WORK/before.config") <(yset "$K/.config") >/dev/null; then
        changed=$(diff <(yset "$WORK/before.config") <(yset "$K/.config") | grep '^[<>]' | head -n 6 | tr '\n' ' ' || :)
        note "$mod: skipped, it would change the built-in kernel ($changed)"
        cp "$WORK/before.config" "$K/.config"; continue
    fi
    WANT+=("$mod")
done <"$LIST"
[ ${#WANT[@]} -gt 0 ] || die "no candidate module can be built for this kernel"

# ---------------------------------------------------------------- build
# Modules need the kernel's own symbol table (Module.symvers), so the kernel
# is built too; only the modules are shipped.
mk -j"$JOBS" modules

# The wanted modules and what they need that the device does not have.
# Names are compared the kernel's way: dashes as underscores.
stock() {
    grep -q -E "(^|[ /])($1|${1//_/-})[.]ko( |\$)" "$FACTS/lib-modules.txt" 2>/dev/null ||
        grep -q "^$1 " "$FACTS/modules.txt" 2>/dev/null
}
find_ko() {
    local f
    f=$(find "$K" -name "$1.ko" -type f | head -n 1)
    [ -n "$f" ] || f=$(find "$K" -name "${1//_/-}.ko" -type f | head -n 1)
    printf '%s' "$f"
}
PACK=()
QUEUE=()
for mod in "${WANT[@]}"; do QUEUE+=("${mod//-/_}"); done
while [ ${#QUEUE[@]} -gt 0 ]; do
    mod=${QUEUE[0]}
    QUEUE=("${QUEUE[@]:1}")
    case " ${PACK[*]-} " in *" $mod "*) continue ;; esac
    ko=$(find_ko "$mod")
    [ -n "$ko" ] || { note "$mod: built, but no $mod.ko found"; continue; }
    PACK+=("$mod")
    for dep in $(modinfo -F depends "$ko" | tr ',' ' '); do
        stock "${dep//-/_}" || QUEUE+=("${dep//-/_}")
    done
done

# ---------------------------------------------------------------- check
# A module without __versions makes modprobe fail: no CRCs, not the end of
# the script (set -e/pipefail).
crcs() { modprobe --dump-modversions "$1" 2>/dev/null | awk '{print $2, $1}' | sort || :; }
STOCK_CRCS="$WORK/stock.crcs"
: >"$STOCK_CRCS"
for s in "$FACTS"/ko/*.ko; do [ -f "$s" ] && crcs "$s" >>"$STOCK_CRCS"; done
sort -u -o "$STOCK_CRCS" "$STOCK_CRCS"
[ -n "$VERMAGIC" ] || note "WARNING: the facts hold no stock module's vermagic; only the release is compared"
if [ "$MODVERSIONS" = y ] && [ ! -s "$STOCK_CRCS" ]; then
    # The device's kernel still refuses a module whose CRCs differ.
    note "WARNING: no stock module CRCs in the facts (ko/); the kernel's own check at load is the only one"
fi
NAME=drivers-$DEVICE-$RELEASE
DEST=$OUT/$NAME
rm -rf "$DEST"
mkdir -p "$DEST/modules"
for mod in "${PACK[@]}"; do
    ko=$(find_ko "$mod")
    vm=$(modinfo -F vermagic "$ko")
    [ "${vm%% *}" = "$RELEASE" ] || die "$mod: vermagic '$vm' is not for $RELEASE"
    if [ -n "$VERMAGIC" ] && [ "$vm" != "$VERMAGIC" ]; then
        die "$mod: vermagic '$vm' differs from the stock modules' '$VERMAGIC'"
    fi
    if [ "$MODVERSIONS" = y ]; then
        shared=$(join <(crcs "$ko") "$STOCK_CRCS" || :)
        bad=$(printf '%s\n' "$shared" | awk 'NF == 3 && $2 != $3 {print $1}' | head -n 5 | tr '\n' ' ')
        [ -z "$bad" ] || die "$mod: symbol CRCs differ from the firmware's ($bad): this tree is not the firmware's kernel source"
        [ -n "$shared" ] || note "WARNING: $mod: no symbol shared with the saved stock modules, CRCs not compared"
    fi
    cp "$ko" "$DEST/modules/$(basename "$ko")"
    "${CROSS}strip" --strip-debug "$DEST/modules/$(basename "$ko")"
done

# ---------------------------------------------------------------- layer
# Load order: a module after the ones in the pack it depends on.
ORDER=()
LEFT=("${PACK[@]}")
while [ ${#LEFT[@]} -gt 0 ]; do
    progress=0
    NEXT=()
    for mod in "${LEFT[@]}"; do
        # ls exits 2 when one of the two names is missing (hid_sony is
        # hid-sony.ko): `|| :` or set -e/pipefail ends the script here.
        f=$(ls "$DEST/modules/$mod.ko" "$DEST/modules/${mod//_/-}.ko" 2>/dev/null | head -n 1 || :)
        [ -n "$f" ] || die "$mod: its .ko is missing from the pack"
        ready=1
        for dep in $(modinfo -F depends "$f" | tr ',' ' '); do
            case " ${LEFT[*]} " in *" ${dep//-/_} "*) ready=0 ;; esac
        done
        if [ $ready = 1 ]; then
            ORDER+=("modules/$(basename "$f")")
            progress=1
        else
            NEXT+=("$mod")
        fi
    done
    [ $progress = 1 ] || die "the modules depend on each other in a circle: ${LEFT[*]}"
    LEFT=("${NEXT[@]+"${NEXT[@]}"}")
done
devices="\"$DEVICE\", \"$MODEL\""
[ "$MODEL" = TG3040 ] && devices="$devices, \"trimui-hammer\""
modules=$(printf '"%s", ' "${ORDER[@]}")
cat >"$DEST/layer.json" <<EOF
{
  "name": "Kernel drivers ($RELEASE)",
  "description": "Modules built for exactly this handheld's kernel $RELEASE: ${WANT[*]}. Loaded when JukaHub OS starts, unloaded when it ends.",
  "version": "$(date -u +%Y%m%d)",
  "arch": "aarch64",
  "kind": "drivers",
  "devices": [$devices],
  "kernel": "$RELEASE",
  "modules": [${modules%, }]
}
EOF
{
    echo "JukaHub OS driver pack $NAME, built $(date -u '+%Y-%m-%d %H:%M UTC')"
    echo "kernel tree: $K ($(git -C "$K" describe --always --dirty 2>/dev/null || echo 'not a git tree'))"
    echo "compiler: ${CROSS}gcc $have_gcc (the kernel: gcc ${want_gcc:-unknown})"
    echo "vermagic: ${VERMAGIC:-not saved}; MODVERSIONS: ${MODVERSIONS:-unknown}"
    echo "modules: ${ORDER[*]}"
    printf '%s\n' "${INFO[@]+"${INFO[@]}"}"
} >"$DEST/BUILD-INFO.txt"
echo "build-driver-pack: $DEST"
