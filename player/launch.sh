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
#
#  jukahub-restart-now: 1
#
#  A clean exit that leaves .restart-now behind (Restart in safe mode on the
#  recovery card) is started again once, the same way.
#
#  jukahub-screen-handoff: 1
#
#  A helper that quits JukaHub to take the display for itself (PS Remote Play's
#  stream) leaves its pid in .screen-handoff. This script waits for that helper
#  with the firmware UI still stopped, then starts JukaHub again, so nothing
#  draws over the stream and the user comes back to JukaHub, not to MainUI.
# ---------------------------------------------------------------------------

APP_DIR=/mnt/SDCARD/Apps/JukaHub

# The app folder can be renamed; fall back to this script's own location.
[ -d "$APP_DIR" ] || APP_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$APP_DIR" || exit 1

# --------------------------------------------------------------- log helper
LOG="$APP_DIR/errors.txt"
# Every session appends here, so cap it: past 1 MiB the old lines move to
# errors.txt.1 (one generation kept) instead of growing on the card for ever.
if [ -f "$LOG" ]; then
    LOG_SIZE="$(wc -c <"$LOG" | tr -d ' ')"
    if [ "${LOG_SIZE:-0}" -gt 1048576 ] 2>/dev/null; then
        mv -f "$LOG" "$LOG.1" 2>/dev/null
    fi
fi
say() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >>"$LOG"
}

PENDING="$APP_DIR/.restart-pending"
SWAPPED="$APP_DIR/.update-swapped"
FAILED="$APP_DIR/.update-failed"
RESTART_NOW="$APP_DIR/.restart-now"
# A helper that took the display over from JukaHub (see the header).
HANDOFF="$APP_DIR/.screen-handoff"
# How the last run ended, "<exit status> <pid>", for JukaHub's next start (its
# boot guard reads and removes it). Not cleared below: that start may come from
# a later launch of this script.
LAST_EXIT="$APP_DIR/.last-exit"

say "=== JukaHub launched from $APP_DIR ==="

# Flush card writes first: FAT32 is slow and SDL reads the assets right away.
sync

# The markers only belong to a process that has just exited. Leftovers from a
# failed update would make this launch wait for a helper that never arrives.
rm -f "$PENDING" "$SWAPPED" "$FAILED" "$RESTART_NOW" "$HANDOFF"

# --------------------------------------------------- take over the display
# MainUI can be found through pidof (busybox) or pgrep; when neither exists,
# the checks below are skipped and the script behaves as it did before.
#
# A zombie is not counted. A MainUI killed while its supervisor is paused
# stays a zombie until the supervisor runs again, and busybox pidof still
# lists it: every start made while MainUI was up (the boot hook, a start over
# SSH) used to "fail" to kill it eight times and give up without starting
# JukaHub. A zombie holds no display, so it is not in the way.
ui_pid() {
    UI_LIST=""
    for UI_P in $(
        if command -v pidof >/dev/null 2>&1; then
            pidof MainUI 2>/dev/null
        elif command -v pgrep >/dev/null 2>&1; then
            pgrep -x MainUI 2>/dev/null
        fi
    ); do
        UI_STATE=""
        # 2>/dev/null first: a MainUI that has just gone must not print here.
        read -r _ _ UI_STATE _ 2>/dev/null <"/proc/$UI_P/stat" || continue
        [ "$UI_STATE" = Z ] || UI_LIST="$UI_LIST${UI_LIST:+ }$UI_P"
    done
    echo "$UI_LIST"
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
        # One argument per pid: pidof can name more than one MainUI.
        kill -9 $(ui_pid) 2>/dev/null
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
# The games, emulators and apps JukaHub starts must get the environment MainUI
# would have given them, not the one set below for JukaHub itself (its own
# folder first on the library path, HOME pointed at its folder). So the
# originals are recorded before anything changes: JUKAHUB_ORIG_ENV names the
# variables, and JUKAHUB_ORIG_<NAME> carries a value only when it was set.
# JukaHub puts them back for every screen-taking child (childenv.go). A launch
# from inside JukaHub's own environment (the update helper's relaunch) already
# carries the originals, and keeps them rather than recording JukaHub's values.
if [ -z "${JUKAHUB_ORIG_ENV+x}" ]; then
    JUKAHUB_ORIG_ENV="HOME PATH LD_LIBRARY_PATH"
    if [ -n "${HOME+x}" ]; then JUKAHUB_ORIG_HOME="$HOME"; export JUKAHUB_ORIG_HOME; fi
    if [ -n "${PATH+x}" ]; then JUKAHUB_ORIG_PATH="$PATH"; export JUKAHUB_ORIG_PATH; fi
    if [ -n "${LD_LIBRARY_PATH+x}" ]; then
        JUKAHUB_ORIG_LD_LIBRARY_PATH="$LD_LIBRARY_PATH"
        export JUKAHUB_ORIG_LD_LIBRARY_PATH
    fi
fi
export JUKAHUB_ORIG_ENV

# SDL2 + SDL2_image + SDL2_ttf already live in the device rootfs (/usr/lib):
# 2.26.1 on early firmware, 2.30.8 since firmware 1.1.0. Its video backends
# are "mali" (SDL's generic fbdev-EGL driver, running on the PowerVR GE8300's
# fbdev EGL — the name is not the GPU) and "dummy", so no SDL_VIDEODRIVER is
# set here; the app asks for the opengles2 renderer itself and logs what it
# got ("render pipeline"). If the user dropped copies into ./libs they win.
LD_LIBRARY_PATH="$APP_DIR:$APP_DIR/libs:/usr/trimui/lib:/usr/lib:/lib:$LD_LIBRARY_PATH"
PATH="$APP_DIR:$APP_DIR/required:$PATH"
HOME="$APP_DIR"
# Read by JukaHub before an update to tell it that this script will do the
# restarting. Older launchers do not set it, and the app then falls back to the
# helper's own relaunch path.
JUKAHUB_LAUNCHED_BY=launch.sh
# This launcher waits for a screen helper (the header's screen-handoff
# protocol). Exported by the running script, so a newer launch.sh copied onto
# the card under an older running one never makes the app expect a wait.
JUKAHUB_SCREEN_HANDOFF=1
export LD_LIBRARY_PATH PATH HOME JUKAHUB_LAUNCHED_BY JUKAHUB_SCREEN_HANDOFF

# ---------------------------------------------------------------- time zone
# The zone chosen in Settings -> Date & time is kept in jukatimezone next to
# the binary and exported as TZ, so JukaHub starts in it and every emulator it
# runs inherits it. Only a plain IANA name passes ("Europe/Berlin",
# "Etc/GMT+5"): no path, no "..", nothing a shell would read. JukaHub carries
# the zone database itself (tzdata_linux.go), so the name works even where the
# image has no /usr/share/zoneinfo; a C program there falls back to UTC as it
# always did. /etc is never written: the root filesystem belongs to the
# firmware and may be read-only. At most 128 bytes are read (a damaged file
# cannot hold the script up) and a name longer than 64 characters is refused,
# the same rule as the app's (tzValidName).
if [ -f "$APP_DIR/jukatimezone" ]; then
    JUKA_TZ="$(dd if="$APP_DIR/jukatimezone" bs=128 count=1 2>/dev/null | head -n 1 | tr -d '\r\n')"
    case "$JUKA_TZ" in
        "" | /* | */ | *..* | *[!A-Za-z0-9_+/-]*)
            [ -n "$JUKA_TZ" ] && say "ignoring time zone file: not a zone name"
            ;;
        *)
            if [ "${#JUKA_TZ}" -le 64 ]; then
                TZ="$JUKA_TZ"
                export TZ
            else
                say "ignoring time zone file: name too long"
            fi
            ;;
    esac
fi

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
    # The status tells the next start's boot guard a crash or a kill (2, 134,
    # 137, 139, ...) from a quit; the pid tells it the line is about the run it
    # recorded. The sync below puts it on the card.
    { echo "$RC $APP_PID" >"$LAST_EXIT"; } 2>/dev/null
    APP_PID=""
    say "=== JukaHub exited with code $RC ==="
    # Tell a crash from a quit. 2 is Go's status after a panic or a fatal
    # runtime error: the stack is in the lines above, and JukaHub files the
    # same text as crash-<time>.txt in this folder at its next start. 1 is a
    # startup failure JukaHub explained above. Above 128 is the signal that
    # ended it (137 SIGKILL, often the out-of-memory killer; 139 SIGSEGV).
    # Nothing here restarts after a crash.
    case "$RC" in
        0) ;;
        1) say "JukaHub stopped during startup (exit code 1); the reason is in the lines above" ;;
        2) say "JukaHub CRASHED (exit code 2: Go panic or fatal error); the stack is above, the report is crash-*.txt" ;;
        *)
            if [ "$RC" -gt 128 ] 2>/dev/null; then
                say "JukaHub was ended by signal $((RC - 128)) (exit code $RC)"
            else
                say "JukaHub exited with an error (exit code $RC)"
            fi
            ;;
    esac
    sync

    # A helper took the display over (PS Remote Play): JukaHub quit so the
    # helper could have the screen to itself, and wrote the helper's pid. Wait
    # for it here, with the supervisors still paused and MainUI still stopped —
    # giving the screen back now would put MainUI over the stream — then start
    # JukaHub again, which is where the user came from. A stop request ends the
    # wait (the power key); twelve hours bound it.
    if [ -f "$HANDOFF" ]; then
        HPID="$(head -c 32 "$HANDOFF" 2>/dev/null | tr -cd '0-9')"
        rm -f "$HANDOFF"
        # A real pid only: 0 or 1 would make the kills below reach this
        # script's own group or every process, and never this script itself.
        if [ -n "$HPID" ] && [ "$HPID" -gt 1 ] 2>/dev/null && [ "$HPID" != "$$" ] &&
            kill -0 "$HPID" 2>/dev/null; then
            say "screen handed to helper pid $HPID; waiting for it"
            n=0
            while [ "$STOPPED" -eq 0 ] && [ "$n" -lt 43200 ] && kill -0 "$HPID" 2>/dev/null; do
                n=$((n + 1))
                sleep 1
            done
            if kill -0 "$HPID" 2>/dev/null; then
                # The stop request (or the bound) ended the wait, not the
                # helper: it runs in a session of its own and got no signal.
                # Handing the screen back now would put MainUI over a live
                # stream, so its whole session goes first (it leads it, so its
                # pid is the group's): TERM, then KILL after five seconds.
                say "stopping helper pid $HPID before handing the screen back"
                kill -TERM -"$HPID" 2>/dev/null || kill -TERM "$HPID" 2>/dev/null
                n=0
                while [ "$n" -lt 5 ] && { kill -0 -"$HPID" 2>/dev/null || kill -0 "$HPID" 2>/dev/null; }; do
                    n=$((n + 1))
                    sleep 1
                done
                kill -KILL -"$HPID" 2>/dev/null
            else
                say "helper pid $HPID finished after ${n}s"
            fi
            if [ "$STOPPED" -eq 0 ] && [ "$RC" -eq 0 ]; then
                sync
                continue
            fi
        fi
    fi

    # A stop request ends the session: never restart into an update in the
    # middle of handing the screen back.
    [ "$STOPPED" -eq 1 ] && break

    # JukaHub asked to be started again (Restart in safe mode on the recovery
    # card). Only after a clean exit, and once per request: the marker goes
    # before the restart, so this can never turn into a crash loop. An update
    # swap in progress wins: the restart below waits for its helper first.
    if [ -f "$RESTART_NOW" ]; then
        rm -f "$RESTART_NOW"
        if [ "$RC" -eq 0 ] && [ ! -f "$PENDING" ]; then
            say "JukaHub asked to be started again"
            sync
            continue
        fi
    fi

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
        say "swap helper reported a failure; JukaHub finishes or undoes the update as it starts"
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

# Who brings MainUI back. On stock and CrossMix firmware MainUI started this
# script (through /tmp/cmd_to_run.sh) and exited, and the firmware supervisor,
# runtrimui.sh, is waiting for this script: it starts MainUI again the moment
# the script returns. (Started by the boot hook instead, the supervisor's own
# MainUI was the one stopped above, and the CONT above lets it start a new
# one.) This script used to start a MainUI of its own two seconds before
# returning — so the supervisor's made two, both drawing to /dev/fb0: the
# glitchy, flickering screen after leaving JukaHub.
#
# The supervisor now always gets the first go. A watcher left behind waits up
# to ten seconds after this script is gone for a MainUI to appear and starts
# one only if none has (a firmware without a supervisor, a start over SSH),
# checking once more right before it does, so the user never ends up on a
# black screen and never gets two launchers either.
(
    # The nohup of the old code: a hang-up as the session ends must not take
    # the watcher (or the MainUI it starts) with it.
    trap '' HUP
    n=0
    while [ "$n" -lt 10 ]; do
        sleep 1
        [ -n "$(ui_pid)" ] && exit 0
        n=$((n + 1))
    done
    for UI in \
        /usr/trimui/bin/MainUI \
        /usr/bin/MainUI \
        /usr/local/bin/MainUI \
        /mnt/SDCARD/System/usr/trimui/bin/MainUI
    do
        if [ -x "$UI" ]; then
            [ -n "$(ui_pid)" ] && exit 0
            say "no launcher came back after 10s; starting $UI"
            cd "$(dirname "$UI")" 2>/dev/null
            exec "$UI" >/dev/null 2>&1
        fi
    done
    say "no launcher came back and none was found to start"
) </dev/null >/dev/null 2>&1 &

exit 0
