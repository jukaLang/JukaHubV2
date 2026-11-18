#!/bin/sh
# librarydoctor — keeps the Roms folders tidy.
#
# Actions: check, junk, dupes.
#   check  reports duplicate games, broken zips, empty files and litter
#   junk   deletes Mac/Windows litter: "._" twins, .DS_Store, Thumbs.db,
#          desktop.ini and __MACOSX folders
#   dupes  moves extra copies of a game into Roms/.duplicates
#
# JukaHub has all three built in (player/plugnative_tools.go) and runs that
# version while this file is unchanged. This script is the shell version of
# the litter part, a starting point for your own: edit it and JukaHub runs it
# instead. The script (not the built-in version) reads the custom variable
# doctor_roms when it is set, to look at another folder.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

roms=''
for arg in "$@"; do
    case "$arg" in
        --doctor_roms=*) roms=${arg#*=} ;;
    esac
done
if [ -z "$roms" ]; then
    if [ -d /mnt/SDCARD/Roms ]; then
        roms=/mnt/SDCARD/Roms
    else
        here=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
        roms="$here/../../Roms"
    fi
fi
if [ ! -d "$roms" ]; then
    printf 'No Roms folder at %s\n' "$roms"
    exit 0
fi

litter() {
    find "$roms" \( -type d -name '.duplicates' -prune \) -o \
        \( -type d -name '__MACOSX' -prune -print \) -o \
        \( -type f \( -name '._*' -o -name '.DS_Store' -o -name 'Thumbs.db' -o -name 'desktop.ini' \) -print \) 2>/dev/null
}

case "$action" in
    check)
        n=$(litter | wc -l | tr -d ' ')
        case "$n" in
            0|'') printf 'No junk files in %s\n' "$roms" ;;
            1) printf '1 junk file in %s\n' "$roms" ;;
            *) printf '%s junk files in %s\n' "$n" "$roms" ;;
        esac
        ;;
    junk)
        n=0
        litter | {
            while IFS= read -r path; do
                rm -rf "$path" && n=$((n + 1))
            done
            printf 'Removed %s junk files\n' "$n"
        }
        ;;
    dupes)
        printf 'Setting duplicates aside is built into JukaHub; the script version only handles junk files.\n'
        ;;
    *)
        printf 'Unknown action: %s (try check, junk, dupes)\n' "$action" >&2
        exit 1
        ;;
esac
