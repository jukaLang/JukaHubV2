#!/bin/sh
# tips — handheld tips, and battery advice that reads the battery first.
#
# Actions: tip, battery.
#   tip      one tip about getting more out of JukaHub, at random
#   battery  the charge level and the setting most worth changing at it
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

tips='Settings › Power & battery: the battery saver steps in by itself at 20%.
Settings › Display & theme: lower the brightness to make a charge last longer.
Settings › Display & theme: Reduce motion is calmer and lighter on the battery.
Settings › General: switch Weather off if you never look at it, and nothing polls.
Theme presets › Total conversion repaints and rearranges the whole interface.
Settings › Network › Phone companion lets you type on your phone instead.
Settings › Games & emulators: Video snaps preview your games in the library.
Plugins you switch on put their buttons in the Start menu, under Plugins.
Settings › Power & battery: a shorter screensaver delay saves the most power.
Settings › Privacy & data: saved browser sign-ins can expire on their own.'

numeric() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
    esac
    return 0
}

random_line() {
    count=$(printf '%s\n' "$1" | wc -l | tr -d ' ')
    numeric "$count" || count=1
    seed=$(date +%s%N 2>/dev/null)
    case "$seed" in
        ''|*[!0-9]*) seed=$(date +%s) ;;
    esac
    # Keep the last nine digits: awk holds numbers as doubles, and a full
    # nanosecond stamp loses exactly the digits two quick presses differ in.
    if [ "${#seed}" -gt 9 ]; then
        seed=${seed#"${seed%?????????}"}
    fi
    if command -v awk >/dev/null 2>&1; then
        n=$(awk -v n="$count" -v s="$seed" 'BEGIN { srand(s); printf "%d\n", int(rand() * n) + 1 }')
    else
        # Leading zeros would make sh read the number as octal.
        s=${seed#"${seed%%[!0]*}"}
        [ -n "$s" ] || s=0
        n=$((s % count + 1))
    fi
    printf '%s\n' "$1" | sed -n "${n}p"
}

# battery prints "<percent> <status>" for the first battery the kernel lists,
# or nothing on a machine without one.
battery() {
    for dir in /sys/class/power_supply/*; do
        [ -r "$dir/capacity" ] || continue
        read -r cap <"$dir/capacity"
        numeric "$cap" || continue
        status=Unknown
        if [ -r "$dir/status" ]; then
            read -r status <"$dir/status"
        fi
        printf '%s %s\n' "$cap" "$status"
        return 0
    done
}

case "$action" in
    tip)
        random_line "$tips"
        ;;
    battery)
        reading=$(battery)
        if [ -z "$reading" ]; then
            printf 'No battery here: plugged in for good, nothing to save.\n'
            exit 0
        fi
        cap=${reading%% *}
        status=${reading#* }
        case "$status" in
            Charging|Full|'Not charging')
                printf 'Battery %s%% and charging: a good time for updates.\n' "$cap"
                ;;
            *)
                if [ "$cap" -le 15 ]; then
                    printf 'Battery %s%%: save your game and plug in soon.\n' "$cap"
                elif [ "$cap" -le 30 ]; then
                    printf 'Battery %s%%: Power mode › Battery Saver buys you time.\n' "$cap"
                elif [ "$cap" -le 60 ]; then
                    printf 'Battery %s%%: dim the screen a step to stretch it.\n' "$cap"
                else
                    printf 'Battery %s%%: plenty left. Enjoy!\n' "$cap"
                fi
                ;;
        esac
        ;;
    *)
        printf 'Unknown action: %s (try tip, battery)\n' "$action" >&2
        exit 1
        ;;
esac
