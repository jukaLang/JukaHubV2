#!/bin/sh
# Reversible boot acceleration for the verified stock TG5040 firmware only.
# Installed by JukaHub OS mode; run "restore" from the SD card to undo it.

BASE=${0%/*}
[ "$BASE" != "$0" ] || BASE=.
BASE=$(CDPATH= cd "$BASE" 2>/dev/null && pwd -P) || exit 1
APP=${BASE%/stock_fastboot}
case "$APP" in
    /mnt/SDCARD/Apps/JukaHub | /mnt/SDCARD/Apps/JukaHub-Trimui) ;;
    *) echo 'stock-fastboot: unexpected app location' >&2; exit 1 ;;
esac

RUN=/etc/init.d/runtrimui
RUN_OLD=$APP/data/stock-runtrimui-init.sh
EX=/mnt/SDCARD/System/starts/ex_init.sh
EX_OLD=/mnt/SDCARD/System/starts/ex_init.jukahub-original
EARLY=/etc/init.d/jukahub-earlysd
EARLY_LINK=/etc/rc.d/S41jukahub-earlysd
PREFETCH=/etc/init.d/jukahub-earlyprefetch
PREFETCH_LINK=/etc/rc.d/S42jukahub-earlyprefetch
BOOST=/mnt/SDCARD/System/starts/00-jukahub-bootboost.sh
EX_WANT=/tmp/jukahub-ex-init-want.$$
BOOST_WANT=/tmp/jukahub-boost-want.$$
PREFETCH_WANT=/tmp/jukahub-prefetch-want.$$
RUN_ORIGINAL_SHA=a907221999794038941afceb9571315a69ce884d2f58c14701ef06a08b4535c3
RUN_PATCHED_SHA=5cc2dce4809fef80d13b2d5fe31a74000fb913eb41c02f56bd6624c5426a1121
EARLY_SHA=2e8dd37ea529ae41247954f15e624ee20413a7727a22a3925c34c382187cc55b
PREFETCH_SHA=f54015266fe638e3da137a36b81cff6265edc94d1f9d5bf0d502e0fef8af0a1f
EX_ORIGINAL_SHA=292f803627e5aa3a69abb92de63deb770a7d966bf8ce6ae29cf3a31dfa252001
EX_LEGACY_SHA=6ceacabdaeeffdb774e801d7fd2ffd545199ddf0fb104ef4b3a2db18ba14dcca
BOOST_LEGACY_SHA=f4de121254fceeea84b53b4d1f1648feef997c2dbaf434b70a68000921f041be
EX_V1_SHA=3a053ce721ca40b96a32a793c15523db9dc8402451e70c3869f4224e75efc564
BOOST_V1_SHA=a4c5d93471fbce594570da4db8448dd0298fc8f16174b1dc79daeb7e28729bb0

trap 'rm -f "$EX_WANT" "$BOOST_WANT" "$PREFETCH_WANT"' 0
fail() { echo "stock-fastboot: $*" >&2; exit 1; }
digest() { sha256sum "$1" 2>/dev/null | cut -d ' ' -f1; }
original_run() { [ "$(digest "$1")" = "$RUN_ORIGINAL_SHA" ]; }
original_ex() { [ "$(digest "$1")" = "$EX_ORIGINAL_SHA" ]; }
legacy_ex() { [ "$(digest "$1")" = "$EX_LEGACY_SHA" ]; }
legacy_boost() { [ "$(digest "$1")" = "$BOOST_LEGACY_SHA" ]; }
v1_ex() { [ "$(digest "$1")" = "$EX_V1_SHA" ]; }
v1_boost() { [ "$(digest "$1")" = "$BOOST_V1_SHA" ]; }
same() { [ -f "$1" ] && cmp -s "$1" "$2"; }
installed() {
    original_run "$RUN_OLD" && original_ex "$EX_OLD" &&
        same "$RUN" "$BASE/runtrimui-patched" && same "$EX" "$EX_WANT" &&
        same "$EARLY" "$BASE/jukahub-earlysd" && same "$PREFETCH" "$PREFETCH_WANT" &&
        same "$BOOST" "$BOOST_WANT" &&
        [ "$(readlink "$EARLY_LINK" 2>/dev/null)" = ../init.d/jukahub-earlysd ] &&
        [ "$(readlink "$PREFETCH_LINK" 2>/dev/null)" = ../init.d/jukahub-earlyprefetch ]
}

copy_boot_file() {
    dst=$2
    tmp=$dst.jukahub-new
    cp "$1" "$tmp" || fail "cannot write $tmp"
    chmod 755 "$tmp" || fail "cannot set permissions on $tmp"
    mv -f "$tmp" "$dst" || fail "cannot replace $dst"
}

prepare() {
    [ "$(id -u)" = 0 ] || fail 'root is required'
    grep -q 'sun50iw10p1' /proc/cmdline 2>/dev/null || fail 'this is not the verified Smart Pro SoC'
    [ "$(cat /etc/model 2>/dev/null)" = TG5040 ] || fail 'this is not the verified Smart Pro model'
    [ "$(cat /sys/block/mmcblk1/device/type 2>/dev/null)" = SD ] || fail 'the app card is not the removable SD card'
    sed "s|@APP_DIR@|$APP|g" "$BASE/ex_init-wrapper" >"$EX_WANT" || fail 'cannot prepare the update wrapper'
    sed "s|@APP_DIR@|$APP|g" "$BASE/00-jukahub-bootboost.sh" >"$BOOST_WANT" || fail 'cannot prepare the CPU boost'
    sed "s|@APP_DIR@|$APP|g" "$BASE/jukahub-earlyprefetch" >"$PREFETCH_WANT" || fail 'cannot prepare the early read-ahead'
    [ -f "$BASE/jukahub-earlysd" ] && [ -f "$BASE/jukahub-earlyprefetch" ] && [ -f "$BASE/runtrimui-patched" ] || fail 'package files are missing'
    [ "$(digest "$BASE/runtrimui-patched")" = "$RUN_PATCHED_SHA" ] || fail 'launcher patch in package is damaged'
    [ "$(digest "$BASE/jukahub-earlysd")" = "$EARLY_SHA" ] || fail 'early SD hook in package is damaged'
    [ "$(digest "$BASE/jukahub-earlyprefetch")" = "$PREFETCH_SHA" ] || fail 'early read-ahead hook in package is damaged'
}

install() {
    [ -f "$APP/os/os.json" ] && grep -q '"enabled"[[:space:]]*:[[:space:]]*true' "$APP/os/os.json" || fail 'JukaHub OS mode is off'
    [ -f "$APP/launch.sh" ] && [ -f "$APP/JukaHub" ] || fail 'JukaHub is incomplete'
    [ -f /mnt/SDCARD/System/starts/jukahub-autostart.sh ] || fail 'JukaHub boot hook is missing'

    # Recognize only the inspected firmware and our own prior files. A
    # firmware update or another mod keeps control of its own boot scripts.
    { original_run "$RUN" || same "$RUN" "$BASE/runtrimui-patched"; } || fail 'stock launcher init is unfamiliar; left untouched'
    { original_ex "$EX" || legacy_ex "$EX" || v1_ex "$EX" || same "$EX" "$EX_WANT"; } || fail 'TRIMUI_EX startup is unfamiliar; left untouched'
    [ ! -e "$RUN_OLD" ] || original_run "$RUN_OLD" || fail 'stock launcher backup is unfamiliar'
    [ ! -e "$EX_OLD" ] || original_ex "$EX_OLD" || fail 'TRIMUI_EX backup is unfamiliar'
    { [ ! -e "$RUN_OLD" ] && original_run "$RUN"; } || [ -e "$RUN_OLD" ] || fail 'stock launcher backup is missing'
    { [ ! -e "$EX_OLD" ] && original_ex "$EX"; } || [ -e "$EX_OLD" ] || fail 'TRIMUI_EX backup is missing'
    { [ ! -e "$EARLY" ] || same "$EARLY" "$BASE/jukahub-earlysd"; } || fail 'early SD hook is unfamiliar'
    { [ ! -e "$PREFETCH" ] || same "$PREFETCH" "$PREFETCH_WANT"; } || fail 'early read-ahead hook is unfamiliar'
    { [ ! -e "$BOOST" ] || legacy_boost "$BOOST" || v1_boost "$BOOST" || same "$BOOST" "$BOOST_WANT"; } || fail 'CPU boost hook is unfamiliar'
    if [ -e "$EARLY_LINK" ] || [ -L "$EARLY_LINK" ]; then
        [ "$(readlink "$EARLY_LINK")" = ../init.d/jukahub-earlysd ] || fail 'early SD startup link is unfamiliar'
    fi
    if [ -e "$PREFETCH_LINK" ] || [ -L "$PREFETCH_LINK" ]; then
        [ "$(readlink "$PREFETCH_LINK")" = ../init.d/jukahub-earlyprefetch ] || fail 'early read-ahead startup link is unfamiliar'
    fi
    if installed; then
        echo 'stock-fastboot: installed'
        return 0
    fi

    mkdir -p "$APP/data" || fail 'cannot make the backup folder'
    if [ ! -e "$RUN_OLD" ]; then cp -p "$RUN" "$RUN_OLD" || fail 'cannot back up stock launcher'; fi
    if [ ! -e "$EX_OLD" ]; then cp -p "$EX" "$EX_OLD" || fail 'cannot back up TRIMUI_EX startup'; fi
    same "$EARLY" "$BASE/jukahub-earlysd" || copy_boot_file "$BASE/jukahub-earlysd" "$EARLY"
    if [ ! -L "$EARLY_LINK" ]; then ln -s ../init.d/jukahub-earlysd "$EARLY_LINK" || fail 'cannot enable early SD mount'; fi
    same "$PREFETCH" "$PREFETCH_WANT" || copy_boot_file "$PREFETCH_WANT" "$PREFETCH"
    if [ ! -L "$PREFETCH_LINK" ]; then ln -s ../init.d/jukahub-earlyprefetch "$PREFETCH_LINK" || fail 'cannot enable early read-ahead'; fi
    same "$RUN" "$BASE/runtrimui-patched" || copy_boot_file "$BASE/runtrimui-patched" "$RUN"
    same "$EX" "$EX_WANT" || copy_boot_file "$EX_WANT" "$EX"
    same "$BOOST" "$BOOST_WANT" || copy_boot_file "$BOOST_WANT" "$BOOST"
    sync
    echo 'stock-fastboot: installed; next power-on uses the faster path'
}

restore() {
    bad=0
    if same "$EX" "$EX_WANT" || legacy_ex "$EX" || v1_ex "$EX"; then
        if original_ex "$EX_OLD"; then
            copy_boot_file "$EX_OLD" "$EX"
            rm -f "$EX_OLD"
        else
            echo 'stock-fastboot: TRIMUI_EX backup is missing; kept its wrapper' >&2
            bad=1
        fi
    elif [ -e "$EX_OLD" ] && ! original_ex "$EX"; then
        echo 'stock-fastboot: TRIMUI_EX startup changed; kept it' >&2
        bad=1
    fi
    if same "$RUN" "$BASE/runtrimui-patched"; then
        if original_run "$RUN_OLD"; then
            copy_boot_file "$RUN_OLD" "$RUN"
        else
            echo 'stock-fastboot: stock launcher backup is missing; kept its patch' >&2
            bad=1
        fi
    elif [ -e "$RUN_OLD" ] && ! original_run "$RUN"; then
        echo 'stock-fastboot: stock launcher init changed; kept it' >&2
        bad=1
    fi
    if same "$BOOST" "$BOOST_WANT" || legacy_boost "$BOOST" || v1_boost "$BOOST"; then
        rm -f "$BOOST" || bad=1
    elif [ -e "$BOOST" ]; then
        echo 'stock-fastboot: CPU boost hook changed; kept it' >&2
        bad=1
    fi
    if [ -L "$PREFETCH_LINK" ] && [ "$(readlink "$PREFETCH_LINK")" = ../init.d/jukahub-earlyprefetch ]; then
        rm -f "$PREFETCH_LINK" || bad=1
    elif [ -e "$PREFETCH_LINK" ] || [ -L "$PREFETCH_LINK" ]; then
        echo 'stock-fastboot: early read-ahead startup link changed; kept it' >&2
        bad=1
    fi
    if same "$PREFETCH" "$PREFETCH_WANT"; then
        rm -f "$PREFETCH" || bad=1
    elif [ -e "$PREFETCH" ]; then
        echo 'stock-fastboot: early read-ahead hook changed; kept it' >&2
        bad=1
    fi
    if [ -L "$EARLY_LINK" ] && [ "$(readlink "$EARLY_LINK")" = ../init.d/jukahub-earlysd ]; then
        rm -f "$EARLY_LINK" || bad=1
    elif [ -e "$EARLY_LINK" ] || [ -L "$EARLY_LINK" ]; then
        echo 'stock-fastboot: early SD startup link changed; kept it' >&2
        bad=1
    fi
    if same "$EARLY" "$BASE/jukahub-earlysd"; then
        rm -f "$EARLY" || bad=1
    elif [ -e "$EARLY" ]; then
        echo 'stock-fastboot: early SD hook changed; kept it' >&2
        bad=1
    fi
    sync
    [ "$bad" = 0 ] || exit 1
    echo 'stock-fastboot: restored stock startup; JukaHub stays installed'
}

prepare
case "${1:-}" in
    install) install ;;
    restore) restore ;;
    status)
        if installed; then
            echo 'stock-fastboot: installed'
        else
            echo 'stock-fastboot: stock or partial startup'
        fi
        ;;
    *) fail 'use: install | restore | status' ;;
esac
