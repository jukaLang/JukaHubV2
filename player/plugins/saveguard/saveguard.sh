#!/bin/sh
# saveguard — daily save backups.
#
# Actions: backup, last.
#   backup  snapshots every emulator save folder unless nothing changed since
#           the newest snapshot (JukaHub runs it on the first start of a day)
#   last    when the newest snapshot was made, and how big it is
#
# The real work is built into JukaHub (player/plugnative_tools.go), which
# knows where each emulator keeps its saves and writes snapshots the save
# manager can restore. While this file is unchanged JukaHub runs that version;
# edit it and JukaHub runs your script instead, with JukaHub's custom
# variables passed as --name=value arguments.
action=${1-}

case "$action" in
    backup|last)
        printf 'Save guard runs inside JukaHub; this script is a starting point for your own version.\n'
        ;;
    *)
        printf 'Unknown action: %s (try backup, last)\n' "$action" >&2
        exit 1
        ;;
esac
