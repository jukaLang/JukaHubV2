#!/bin/sh
# notes — a one-line-at-a-time scratchpad kept in this plugin's folder.
#
# Actions: add, last, count, clear.
#   add   needs --notes_text=something to remember
#   last  prints the newest note
#   count prints how many notes are saved
#   clear empties the pad
#
# The notes live in notes.txt next to this script, so the pad survives
# restarts and can be copied off the device with the plugins folder.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

here=$(dirname "$0")
pad="$here/notes.txt"

text=''
for arg in "$@"; do
    case "$arg" in
        --notes_text=*) text=${arg#*=} ;;
    esac
done

case "$action" in
    add)
        if [ -z "$text" ]; then
            printf 'Nothing to add: set the notes_text variable first\n' >&2
            exit 1
        fi
        stamp=$(date '+%H:%M')
        printf '[%s] %s\n' "$stamp" "$text" >>"$pad" || {
            printf 'Could not write to %s\n' "$pad" >&2
            exit 1
        }
        printf 'Saved: %s\n' "$(printf '%s' "$text" | cut -c1-38)"
        ;;
    last)
        if [ ! -s "$pad" ]; then
            printf 'No notes yet\n'
            exit 0
        fi
        tail -n 1 "$pad" | cut -c1-46
        ;;
    count)
        if [ ! -s "$pad" ]; then
            printf 'No notes yet\n'
            exit 0
        fi
        n=$(wc -l <"$pad" | tr -d ' ')
        if [ "$n" -eq 1 ]; then
            printf '1 note saved\n'
        else
            printf '%s notes saved\n' "$n"
        fi
        ;;
    clear)
        : >"$pad" || {
            printf 'Could not clear %s\n' "$pad" >&2
            exit 1
        }
        printf 'Notes cleared\n'
        ;;
    *)
        printf 'Unknown action: %s (try add, last, count, clear)\n' "$action" >&2
        exit 1
        ;;
esac
