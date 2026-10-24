#!/bin/sh
# ---------------------------------------------------------------------------
#  JukaHub launcher — TrimUI Smart Pro / Brick (stock OS / CrossMix)
#
#  Called by the device's stock launcher:
#     /mnt/SDCARD/Apps/JukaHub/config.json  ->  "launch": "launch.sh"
#
#  What this script does:
#    1. take the display over from the stock launcher (MainUI)
#    2. set up the environment (SDL2 libraries, tool PATH, HOME)
#    3. run the JukaHub binary
#    4. restart in place after a self-update, or hand the display back to the
#       launcher when the app exits
#
#  jukahub-restart-protocol: 1
#
#  JukaHub reads the line above before an update. When it is present the app
#  writes a .restart-pending marker as it replaces itself, and this script runs
#  the new build — the firmware supervisor stays paused and MainUI never
#  reappears in the middle of an update. Without that marker two UIs draw to
#  the same /dev/fb0 and the screen appears to flicker.
# ---------------------------------------------------------------------------

APP_DIR=/mnt/SDCARD/Apps/JukaHub

# The app folder can be renamed; fall back to this script's own location.
[ -d "$APP_DIR" ] || APP_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$APP_DIR" || exit 1

# --------------------------------------------------------------- log helper
LOG="$APP_DIR/errors.txt"
say() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >>"$LOG"
}

PENDING="$APP_DIR/.restart-pending"
SWAPPED="$APP_DIR/.update-swapped"
FAILED="$APP_DIR/.update-failed"

say "=== JukaHub launched from $APP_DIR ==="

# Flush card writes first: FAT32 is slow and SDL reads the assets right away.
sync

# The markers only belong to a process that has just exited. Leftovers from a
# failed update would make this launch wait for a helper that never arrives.
rm -f "$PENDING" "$SWAPPED" "$FAILED"

# --------------------------------------------------- take over the display
# MainUI can be found through pidof (busybox) or pgrep; when neither exists,
# the checks below are skipped and the script behaves as it did before.
ui_pid() {
    if command -v pidof >/dev/null 2>&1; then
        pidof MainUI 2>/dev/null
    elif command -v pgrep >/dev/null 2>&1; then
        pgrep -x MainUI 2>/dev/null
    fi
}

# Normally the launcher has already stepped aside before this script runs.
# If it still holds the framebuffer, pause the supervisor and stop the UI
# (SIGTERM first, SIGKILL only if it refuses to go).
if command -v pgrep >/dev/null 2>&1; then
    pkill -STOP runtrimui.sh 2>/dev/null
    pkill -STOP trimui_launcher 2>/dev/null
fi

UI_PID="$(ui_pid)"
if [ -n "$UI_PID" ]; then
    say "stopping stock launcher (pid $UI_PID)"
    kill $UI_PID 2>/dev/null
    sleep 1
    UI_PID="$(ui_pid)"
    [ -n "$UI_PID" ] && kill -9 $UI_PID 2>/dev/null
fi

# Verify, don't assume. A refused kill used to be ignored, and JukaHub then
# started next to the stock launcher: both drew to the same framebuffer and the
# screen alternated between them. If MainUI will not die, hand the screen back
# instead of starting a second UI.
if command -v pidof >/dev/null 2>&1 || command -v pgrep >/dev/null 2>&1; then
    n=0
    while [ -n "$(ui_pid)" ] && [ "$n" -lt 8 ]; do
        n=$((n + 1))
        say "MainUI still running; kill -9 attempt $n"
        kill -9 "$(ui_pid)" 2>/dev/null
        sleep 1
    done
    if [ -n "$(ui_pid)" ]; then
        say "FATAL: MainUI still holds the display after $n attempts; not starting JukaHub"
        pkill -CONT runtrimui.sh 2>/dev/null
        pkill -CONT trimui_launcher 2>/dev/null
        exit 1
    fi
else
    # Nothing can inspect the process list, so wait out the handover instead
    # of guessing that the display is free.
    sleep 1
fi

# ------------------------------------------------------------- environment
# SDL2 2.26.1 + SDL2_image + SDL2_ttf already live in the device rootfs
# (/usr/lib).  If the user dropped copies into ./libs they win.
LD_LIBRARY_PATH="$APP_DIR:$APP_DIR/libs:/usr/trimui/lib:/usr/lib:/lib:$LD_LIBRARY_PATH"
PATH="$APP_DIR:$APP_DIR/required:$PATH"
HOME="$APP_DIR"
# Read by JukaHub before an update to tell it that this script will do the
# restarting. Older launchers do not set it, and the app then falls back to the
# helper's own relaunch path.
JUKAHUB_LAUNCHED_BY=launch.sh
export LD_LIBRARY_PATH PATH HOME JUKAHUB_LAUNCHED_BY

# TrimUI/Allwinner firmwares ship OpenSSL 1.1
CLR_OPENSSL_VERSION_OVERRIDE=1.1
export CLR_OPENSSL_VERSION_OVERRIDE

# --------------------------------------------------------------- run it
if [ ! -x "$APP_DIR/JukaHub" ]; then
    say "FATAL: binary $APP_DIR/JukaHub not found or not executable"
    chmod +x "$APP_DIR/JukaHub" 2>/dev/null
fi

# ------------------------------------------------------- stop from outside
# The firmware's power handling (and any supervisor) stops an app by signalling
# the process group. The script is part of that group, so it used to die before
# reaching the handover below: MainUI stayed dead and the panel kept JukaHub's
# last frame, with no input able to leave it — the "device locked on the power
# button" report. Catching the stop keeps the script alive long enough to hand
# the screen back.
#
# The signal is not forwarded: the app received the same one directly, and it
# treats a repeat as impatience (force quit, no cleanup). If the app did not
# get it, it is still running and still owns the screen; waiting for it is the
# right answer either way.
APP_PID=""
STOPPED=0
on_stop() {
    STOPPED=1
    say "stop request received; waiting for JukaHub to finish shutting down"
}
trap on_stop TERM INT HUP

while :; do
    # Also covers a stop that arrived while waiting for an update helper.
    [ "$STOPPED" -eq 1 ] && break
    "$APP_DIR/JukaHub" >>"$LOG" 2>&1 &
    APP_PID=$!
    wait "$APP_PID"
    RC=$?
    # A stop signal interrupts wait, not the app. Wait again so the display is
    # only touched once JukaHub has really exited (its own watchdog bounds
    # this to a few seconds even if a cleanup step is stuck).
    if [ "$STOPPED" -eq 1 ]; then
        wait "$APP_PID"
        RC=$?
    fi
    APP_PID=""
    say "=== JukaHub exited with code $RC ==="
    sync

    # A stop request ends the session: never restart into an update in the
    # middle of handing the screen back.
    [ "$STOPPED" -eq 1 ] && break

    # A self-update is the only reason to restart without restoring the stock
    # launcher: the helper is replacing the files right now, so the new build
    # runs here in the same process and MainUI stays out of the way.
    [ -f "$PENDING" ] || break

    say "update swap in progress; waiting for the helper"
    n=0
    while [ "$n" -lt 30 ] && [ ! -f "$SWAPPED" ] && [ ! -f "$FAILED" ]; do
        n=$((n + 1))
        sleep 1
    done
    if [ -f "$FAILED" ]; then
        say "swap helper reported a failure; startup rollback will restore the installed build"
    elif [ ! -f "$SWAPPED" ]; then
        say "no swap verdict after 30s; starting the installed build"
    fi
    rm -f "$PENDING" "$SWAPPED" "$FAILED"
    sync
done

# --------------------------------------------------- give the screen back
if command -v pgrep >/dev/null 2>&1; then
    pkill -CONT runtrimui.sh 2>/dev/null
    pkill -CONT trimui_launcher 2>/dev/null
fi

# If no supervisor brought the launcher back, start it ourselves so the user
# never ends up on a black screen.
sleep 2
if [ -z "$(ui_pid)" ]; then
    for UI in \
        /usr/trimui/bin/MainUI \
        /usr/bin/MainUI \
        /usr/local/bin/MainUI \
        /mnt/SDCARD/System/usr/trimui/bin/MainUI
    do
        if [ -x "$UI" ]; then
            say "restarting stock launcher: $UI"
            (nohup "$UI" >/dev/null 2>&1 &)
            break
        fi
    done
fi

exit 0
