#!/bin/sh
APP=@APP_DIR@
[ -f "$APP/os/os.json" ] && grep -q '"enabled"[[:space:]]*:[[:space:]]*true' "$APP/os/os.json" 2>/dev/null || exit 0
[ ! -e "$APP/.autostart-pending" ] || exit 0
[ ! -e "$APP/.autostart-keep-menu" ] || exit 0
grep -q '"fastBoot"[[:space:]]*:[[:space:]]*false' "$APP/os/os.json" 2>/dev/null && exit 0
(
    trap '' HUP
    CPU=/sys/devices/system/cpu/cpufreq/policy0
    [ -r "$CPU/scaling_governor" ] && [ -w "$CPU/scaling_governor" ] || exit 0
    n=0
    while [ "$n" -lt 150 ]; do
        [ -n "$(pidof JukaHub 2>/dev/null)" ] && break
        sleep 0.1
        n=$((n + 1))
    done
    [ "$n" -lt 150 ] || exit 0
    old=
    read -r old <"$CPU/scaling_governor"
    [ "$old" = ondemand ] || exit 0
    echo performance >"$CPU/scaling_governor" 2>/dev/null || exit 0
    read -r begin _ </proc/uptime
    n=0
    while [ ! -s /tmp/jukahub-first-frame ] && [ "$n" -lt 60 ]; do
        sleep 0.1
        n=$((n + 1))
    done
    current=
    read -r current <"$CPU/scaling_governor"
    [ "$current" = performance ] && echo "$old" >"$CPU/scaling_governor" 2>/dev/null
    read -r end _ </proc/uptime
    printf '%s boosted-to %s restored frame=%s\n' "$begin" "$end" "$([ -s /tmp/jukahub-first-frame ] && echo yes || echo no)" >/tmp/jukahub-bootboost.log
) </dev/null >/dev/null 2>&1 &
exit 0
