#!/bin/sh
# gamepick — can't decide what to play? Let the card decide.
#
# Actions: pick, count.
#   pick   one game at random from the ROM folders, with its system
#   count  how many games the ROM folders hold
#
# The library is the usual Roms/<SYSTEM>/ layout. The folders looked in are the
# TrimUI card (/mnt/SDCARD/Roms), the Roms folder beside JukaHub and ~/Roms;
# set the custom variable gamepick_dir to point somewhere else.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

custom=''
for arg in "$@"; do
    case "$arg" in
        --gamepick_dir=*) custom=${arg#*=} ;;
    esac
done

# The script runs from plugins/gamepick, two folders below JukaHub's own.
here=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
roots="/mnt/SDCARD/Roms
$here/../../Roms
${HOME:-/nonexistent}/Roms"
if [ -n "$custom" ]; then
    roots=$custom
fi

# games lists game files: two or three folders deep (Roms/SNES/game.sfc or
# Roms/PS/Game/disc.cue), skipping artwork, saves and metadata.
games() {
    printf '%s\n' "$roots" | while IFS= read -r root; do
        [ -n "$root" ] && [ -d "$root" ] || continue
        find "$root" -mindepth 2 -maxdepth 3 -type f 2>/dev/null
    done | while IFS= read -r path; do
        case "$path" in
            */.*|*/Imgs/*|*/imgs/*|*/media/*|*/.media/*) continue ;;
        esac
        case "$path" in
            *.png|*.PNG|*.jpg|*.JPG|*.jpeg|*.txt|*.xml|*.db|*.json|*.cfg|*.ini|*.srm|*.sav|*.state*|*.log|*.bak|*.nfo|*.md|*.dat|*.m3u.bak)
                continue
                ;;
        esac
        printf '%s\n' "$path"
    done
}

case "$action" in
    count)
        n=$(games | wc -l | tr -d ' ')
        case "$n" in
            0|'') printf 'No games found in the Roms folders\n' ;;
            1) printf '1 game on the card\n' ;;
            *) printf '%s games on the card\n' "$n" ;;
        esac
        ;;
    pick)
        if ! command -v awk >/dev/null 2>&1; then
            printf 'Picking needs awk, which is not on PATH\n' >&2
            exit 1
        fi
        seed=$(date +%s%N 2>/dev/null)
        case "$seed" in
            ''|*[!0-9]*) seed=$(date +%s) ;;
        esac
        # Keep the last nine digits: awk holds numbers as doubles, and a full
        # nanosecond stamp loses exactly the digits two quick presses differ in.
        if [ "${#seed}" -gt 9 ]; then
            seed=${seed#"${seed%?????????}"}
        fi
        chosen=$(games | awk -v s="$seed" 'BEGIN { srand(s) } { n++; if (rand() * n < 1) pick = $0 } END { print pick }')
        if [ -z "$chosen" ]; then
            printf 'No games found in the Roms folders\n'
            exit 0
        fi
        name=$(basename "$chosen")
        name=${name%.*}
        system=$(basename "$(dirname "$chosen")")
        parent=$(basename "$(dirname "$(dirname "$chosen")")")
        case "$parent" in
            Roms|roms|ROMS) ;;
            *) system=$parent ;;
        esac
        printf 'Try: %s (%s)\n' "$name" "$system"
        ;;
    *)
        printf 'Unknown action: %s (try pick, count)\n' "$action" >&2
        exit 1
        ;;
esac
