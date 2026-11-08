#!/bin/sh
# dice — a little randomness for a button: a die roll, a coin, or a pick from
# a list of your own.
#
# Actions: roll, coin, pick.
#   roll  optional --dice_sides=N   (default 6, 2..1000)
#   pick  optional --dice_list=a,b,c (default heads,tails)
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

sides=6
list=''
for arg in "$@"; do
    case "$arg" in
        --dice_sides=*) sides=${arg#*=} ;;
        --dice_list=*) list=${arg#*=} ;;
    esac
done

if ! command -v awk >/dev/null 2>&1; then
    printf 'Randomness needs awk, which is not on PATH\n' >&2
    exit 1
fi

# one_between N prints a whole number from 1 to N. The seed comes from
# nanoseconds when the system clock offers them, so two quick presses differ.
one_between() {
    n=$1
    seed=$(date +%s%N 2>/dev/null)
    case "$seed" in
        ''|*[!0-9]*) seed=$(date +%s) ;;
    esac
    # Keep the last nine digits: awk holds numbers as doubles, and a full
    # nanosecond stamp loses exactly the digits two quick presses differ in.
    if [ "${#seed}" -gt 9 ]; then
        seed=${seed#"${seed%?????????}"}
    fi
    awk -v n="$n" -v s="$seed" 'BEGIN { srand(s); printf "%d\n", int(rand() * n) + 1 }'
}

case "$action" in
    roll)
        case "$sides" in
            ''|*[!0-9]*)
                printf 'Sides must be a number, not %s\n' "$sides" >&2
                exit 1
                ;;
        esac
        if [ "$sides" -lt 2 ] || [ "$sides" -gt 1000 ]; then
            printf 'Sides must be between 2 and 1000\n' >&2
            exit 1
        fi
        printf 'Rolled %s of %s\n' "$(one_between "$sides")" "$sides"
        ;;
    coin)
        if [ "$(one_between 2)" -eq 1 ]; then
            printf 'Heads\n'
        else
            printf 'Tails\n'
        fi
        ;;
    pick)
        [ -n "$list" ] || list='heads,tails'
        count=0
        oldifs=$IFS
        IFS=','
        for _item in $list; do
            count=$((count + 1))
        done
        IFS=$oldifs
        if [ "$count" -eq 0 ]; then
            printf 'Nothing to pick from\n' >&2
            exit 1
        fi
        want=$(one_between "$count")
        seen=0
        chosen=''
        oldifs=$IFS
        IFS=','
        for item in $list; do
            seen=$((seen + 1))
            if [ "$seen" -eq "$want" ]; then
                chosen=$item
            fi
        done
        IFS=$oldifs
        chosen=${chosen# }
        chosen=${chosen% }
        [ -n "$chosen" ] || chosen='(empty entry)'
        printf 'Picked: %s\n' "$chosen"
        ;;
    *)
        printf 'Unknown action: %s (try roll, coin, pick)\n' "$action" >&2
        exit 1
        ;;
esac
