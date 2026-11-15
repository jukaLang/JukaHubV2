#!/bin/sh
# Stock-menu app: runs dump-board.sh (copied next to this file) and returns to
# the menu when it is done. The result and dump-board.log are on the card.
HERE=$(cd "$(dirname "$0")" && pwd)
sh "$HERE/dump-board.sh" /mnt/SDCARD > "$HERE/dump-board.log" 2>&1
sync
