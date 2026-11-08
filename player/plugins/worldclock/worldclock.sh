#!/bin/sh
# worldclock — what time is it over there?
#
# Actions: cities, pacific, utc.
#   cities   Tokyo, Berlin, London and New York
#   pacific  Sydney, Tokyo and Los Angeles
#   utc      Coordinated Universal Time
#
# The zones are POSIX TZ rules (daylight saving included), so no time zone
# database is needed: busybox, GNU and Git for Windows date all read them.
# Optional custom variable: --worldclock_24h=0 for a 12-hour clock.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

h24=1
for arg in "$@"; do
    case "$arg" in
        --worldclock_24h=*) h24=${arg#*=} ;;
    esac
done
case "$h24" in
    0|false|no|off) fmt='%I:%M %p' ;;
    *) fmt='%H:%M' ;;
esac

at() {
    TZ=$1 date "+$fmt"
}

case "$action" in
    cities)
        printf 'Tokyo %s · Berlin %s · London %s · New York %s\n' \
            "$(at 'JST-9')" \
            "$(at 'CET-1CEST,M3.5.0,M10.5.0/3')" \
            "$(at 'GMT0BST,M3.5.0/1,M10.5.0')" \
            "$(at 'EST5EDT,M3.2.0,M11.1.0')"
        ;;
    pacific)
        printf 'Sydney %s · Tokyo %s · Los Angeles %s\n' \
            "$(at 'AEST-10AEDT,M10.1.0,M4.1.0/3')" \
            "$(at 'JST-9')" \
            "$(at 'PST8PDT,M3.2.0,M11.1.0')"
        ;;
    utc)
        printf 'UTC %s\n' "$(at 'UTC0')"
        ;;
    *)
        printf 'Unknown action: %s (try cities, pacific, utc)\n' "$action" >&2
        exit 1
        ;;
esac
