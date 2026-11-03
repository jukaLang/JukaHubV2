#!/bin/sh
# clock — the time, the date, or how long the device has been up.
#
# Actions: now, date, both, uptime. Every action prints one short line, so it
# fits a toast. Optional custom variable: --clock_24h=0 for a 12-hour clock.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

h24=1
for arg in "$@"; do
    case "$arg" in
        --clock_24h=*) h24=${arg#*=} ;;
    esac
done
case "$h24" in
    0|false|no|off) h24=0 ;;
    *) h24=1 ;;
esac

if [ "$h24" -eq 1 ]; then
    clock_fmt='%H:%M'
else
    clock_fmt='%I:%M %p'
fi

case "$action" in
    now)
        date "+$clock_fmt"
        ;;
    date)
        date '+%a %d %b %Y'
        ;;
    both)
        date "+%a %d %b · $clock_fmt"
        ;;
    uptime)
        if [ -r /proc/uptime ]; then
            read -r up _rest < /proc/uptime
            up=${up%%.*}
            days=$((up / 86400))
            hours=$(((up % 86400) / 3600))
            mins=$(((up % 3600) / 60))
            if [ "$days" -gt 0 ]; then
                printf 'Up %dd %dh %dm\n' "$days" "$hours" "$mins"
            elif [ "$hours" -gt 0 ]; then
                printf 'Up %dh %dm\n' "$hours" "$mins"
            else
                printf 'Up %dm\n' "$mins"
            fi
        else
            printf 'Uptime unavailable on this system\n'
        fi
        ;;
    *)
        printf 'Unknown action: %s (try now, date, both, uptime)\n' "$action" >&2
        exit 1
        ;;
esac
