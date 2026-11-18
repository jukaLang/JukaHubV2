#!/bin/sh
# sysinfo — System monitor: free memory, free space, battery level, chip
# temperature and uptime.
#
# Actions: status, memory, disk, power, heat. Anything the system does not report is
# said plainly instead of failing, so the button never looks broken on a
# machine without /proc or a battery.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

# human turns a size in kilobytes into a short form: 2097152 -> 2.0G.
human() {
    k=$1
    if [ "$k" -ge 1048576 ]; then
        whole=$((k / 1048576))
        tenth=$(((k % 1048576) * 10 / 1048576))
        printf '%d.%dG' "$whole" "$tenth"
    elif [ "$k" -ge 1024 ]; then
        printf '%dM' "$((k / 1024))"
    else
        printf '%dK' "$k"
    fi
}

numeric() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
    esac
    return 0
}

# The memory fields are read with the shell's own read, not with awk: some
# busybox builds reject a regex pattern outright, and /proc/meminfo is just
# "Key:  number unit" lines. A missing file leaves the answer empty, which the
# caller reports as unavailable instead of as an error.
free_ram_kb() {
    avail=''
    spare=''
    while read -r key value _rest; do
        case "$key" in
            MemAvailable:) avail=$value ;;
            MemFree:) [ -n "$spare" ] || spare=$value ;;
        esac
    done 2>/dev/null < /proc/meminfo
    kb=$avail
    [ -n "$kb" ] || kb=$spare
    numeric "$kb" && printf '%s' "$kb"
}

free_disk_kb() {
    kb=''
    if command -v df >/dev/null 2>&1 && command -v awk >/dev/null 2>&1; then
        kb=$(df -k . 2>/dev/null | awk 'NR==2 {print $4}' 2>/dev/null)
    fi
    numeric "$kb" && printf '%s' "$kb"
}

battery_line() {
    for f in /sys/class/power_supply/*/capacity; do
        [ -r "$f" ] || continue
        read -r cap <"$f"
        numeric "$cap" || continue
        printf 'Battery %s%%' "$cap"
        return 0
    done
    printf 'No battery data'
}

heat_line() {
    out=''
    for f in /sys/class/thermal/thermal_zone0/temp /sys/class/thermal/thermal_zone1/temp; do
        [ -r "$f" ] || continue
        read -r milli <"$f"
        numeric "$milli" || continue
        [ "$milli" -gt 0 ] || continue
        out="CPU $((milli / 1000)) °C"
        break
    done
    if [ -r /proc/uptime ]; then
        read -r up _idle </proc/uptime
        secs=${up%%.*}
        if numeric "$secs"; then
            mins=$((secs / 60))
            d=$((mins / 1440)); h=$((mins % 1440 / 60)); m=$((mins % 60))
            if [ "$d" -gt 0 ]; then
                span="${d}d ${h}h ${m}m"
            elif [ "$h" -gt 0 ]; then
                span="${h}h ${m}m"
            else
                span="${m}m"
            fi
            if [ -n "$out" ]; then
                out="$out · on for $span"
            else
                out="on for $span"
            fi
        fi
    fi
    [ -n "$out" ] || out='Temperature and uptime are not reported here'
    printf '%s' "$out"
}

case "$action" in
    heat|uptime)
        printf '%s\n' "$(heat_line)"
        ;;
    memory)
        kb=$(free_ram_kb)
        if [ -n "$kb" ]; then
            printf 'RAM %s free\n' "$(human "$kb")"
        else
            printf 'Memory info unavailable\n'
        fi
        ;;
    disk)
        kb=$(free_disk_kb)
        if [ -n "$kb" ]; then
            printf 'Disk %s free\n' "$(human "$kb")"
        else
            printf 'Disk info unavailable\n'
        fi
        ;;
    power)
        printf '%s\n' "$(battery_line)"
        ;;
    status)
        ram=$(free_ram_kb)
        disk=$(free_disk_kb)
        if [ -n "$ram" ] && [ -n "$disk" ]; then
            printf 'RAM %s free · Card %s free\n' "$(human "$ram")" "$(human "$disk")"
        elif [ -n "$ram" ]; then
            printf 'RAM %s free\n' "$(human "$ram")"
        elif [ -n "$disk" ]; then
            printf 'Disk %s free\n' "$(human "$disk")"
        else
            printf 'System info unavailable\n'
        fi
        printf '%s\n%s\n' "$(battery_line)" "$(heat_line)"
        ;;
    *)
        printf 'Unknown action: %s (try status, power, heat, memory, disk)\n' "$action" >&2
        exit 1
        ;;
esac
