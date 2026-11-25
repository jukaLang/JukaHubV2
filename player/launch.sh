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
#
#  jukahub-paused-record: 1
#
#  Whatever this script, the boot hook or the app pauses of the firmware's
#  launcher is paused by process id and written down in /tmp/jukahub-paused;
#  the hand-back at the end resumes exactly those processes, on every way out.
#  (Pausing "runtrimui.sh" by name used to match nothing where busybox names
#  every script it runs "sh", and resumed nothing either.)
#
#  jukahub-update-probation: 1
#
#  The first starts of an update are watched from here (player/updrescue.go):
#  JukaHub's own boot guard cannot help a build that hangs or crashes before
#  Go's main (the 2026-11-19 release deadlocked in package init and hung every
#  boot). Before the swap the app writes .update-probation: the files the
#  update replaces and where their backups are. A start under that plan must
#  report a working screen with input ("ready <pid>" in .start-check) within
#  the limit; one that hangs, crashes or has no input is stopped (SIGABRT
#  first: the Go runtime prints every goroutine's stack into errors.txt), the
#  backups are copied back, .update-rescued tells the restored build what
#  happened, and it starts at once. Once per plan. The copies are renamed into
#  place under a synced marker (.update-restoring), so a power cut between two
#  renames is finished by the next start before JukaHub runs.
# ---------------------------------------------------------------------------

# Use this script's installed folder even when an older Apps/JukaHub folder
# exists. Remote Play once needed that folder for its log, and choosing it
# merely because it exists can leave a renamed install without its binary.
# (Shell builtins only: every fork before JukaHub starts is on the boot's path.)
case "$0" in
    */*) APP_DIR="${0%/*}" ;;
    *) APP_DIR=. ;;
esac
if [ -n "$APP_DIR" ] && cd "$APP_DIR" 2>/dev/null; then
    APP_DIR="$PWD"
else
    APP_DIR=""
fi
[ -f "$APP_DIR/JukaHub" ] || APP_DIR=/mnt/SDCARD/Apps/JukaHub

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
# The games JukaHub put on the screen, "<process group> <start time>" per line
# (crashreport.go): what a crash would leave running (see stop_screen_groups).
SCREEN_GROUPS="$APP_DIR/.screen-groups"
# The firmware launcher processes paused for this session, one per line:
# "<pid> <start time> <name>" (see the header). /tmp is empty at every boot.
PAUSED=/tmp/jukahub-paused
# The update check (see the header): the plan, the count of its starts, the
# new build's "<stage> <pid>" line, the note for the restored build, and the
# watchdog's word on why it stopped a start (this run of the script only).
PROBATION="$APP_DIR/.update-probation"
PROBATION_TRIES="$APP_DIR/.update-probation-tries"
START_CHECK="$APP_DIR/.start-check"
RESCUED="$APP_DIR/.update-rescued"
# The note for the restored build while a restore renames its copies into
# place: written and synced once every copy is whole, renamed to $RESCUED when
# the last one is in. A start that finds it finishes a restore a power cut
# stopped halfway (probation_resume).
RESTORING="$APP_DIR/.update-restoring"
PROBATION_KILL="/tmp/jukahub-probation-kill.$$"
# Seconds a start under the plan gets to report a working screen: 120, and 45
# once a start of the same update ended with no verdict (a power-off on a black
# screen). JUKAHUB_PROBATION_LIMIT (3-3600) sets both, for testing.
PROBATION_LIMIT=120
PROBATION_RETRY_LIMIT=45
case "${JUKAHUB_PROBATION_LIMIT:-}" in
    "" | *[!0-9]*) ;;
    *)
        if [ "$JUKAHUB_PROBATION_LIMIT" -ge 3 ] && [ "$JUKAHUB_PROBATION_LIMIT" -le 3600 ]; then
            PROBATION_LIMIT="$JUKAHUB_PROBATION_LIMIT"
            PROBATION_RETRY_LIMIT="$JUKAHUB_PROBATION_LIMIT"
        fi
        ;;
esac

say "=== JukaHub launched from $APP_DIR ==="

# No sync here (2026-11-18): reads never wait for unwritten data, and a flush
# of every dirty buffer at a boot put the card's writes in front of the very
# reads JukaHub starts with. The paths that need their writes on the card (a
# failed run's exit code, an update swap, a restart) still sync below.

# The markers only belong to a process that has just exited. Leftovers from a
# failed update would make this launch wait for a helper that never arrives.
rm -f "$PENDING" "$SWAPPED" "$FAILED" "$RESTART_NOW" "$HANDOFF" "$SCREEN_GROUPS"
# USB storage mode's request and helper, in RAM (see usb_storage_handoff); a
# request is only ever for the run that has just exited.
USB_STORAGE_REQ=/tmp/jukahub-usbstorage.req
USB_STORAGE_HELPER=/tmp/jukahub-usbstorage-helper.sh
rm -f "$USB_STORAGE_REQ"

# ------------------------------------------------------------- processes
# A process's state, parent, process group, session and start time (fields 3,
# 4, 5, 6 and 22 of /proc/<pid>/stat) in ST_STATE, ST_PPID, ST_PGRP, ST_SID and
# ST_START. The name in brackets may hold spaces, so the fields are read after
# its last ") ".
stat_of() {
    ST_LINE=""
    read -r ST_LINE 2>/dev/null <"/proc/$1/stat" || return 1
    # shellcheck disable=SC2086
    set -- ${ST_LINE##*) }
    ST_STATE="$1"
    ST_PPID="$2"
    ST_PGRP="$3"
    ST_SID="$4"
    ST_START="${20}"
    [ -n "$ST_STATE" ]
}

# One process for the log: "name (pid N)", with the script a shell runs, so a
# launcher loop that shows up as plain "sh" can still be told apart.
describe() {
    DS_C=""
    read -r DS_C 2>/dev/null <"/proc/$1/comm"
    case "$DS_C" in
        sh | ash | bash | dash | busybox)
            DS_S="$(tr '\0' ' ' <"/proc/$1/cmdline" 2>/dev/null | cut -c1-120)"
            DS_S="${DS_S% }"
            echo "${DS_C:-?} (pid $1: ${DS_S:-?})"
            ;;
        *) echo "${DS_C:-?} (pid $1)" ;;
    esac
}

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

# Wait only while MainUI still owns the display. On the Smart Pro it can ignore
# TERM altogether, so the first wait is short before KILL; the second allows
# the driver more time to release the display. Older firmware without
# fractional sleep keeps the former one-second wait.
wait_ui_gone() {
    WG_MAX="${1:-10}"
    WG_N=0
    while [ "$WG_N" -lt "$WG_MAX" ]; do
        [ -z "$(ui_pid)" ] && return 0
        WG_N=$((WG_N + 1))
        sleep 0.1 2>/dev/null || { sleep 1; break; }
    done
    [ -z "$(ui_pid)" ]
}

# The firmware's launcher loops, found by their command line, which names the
# script even where the shell running it shows up as plain "sh" (a busybox
# built to prefer its own applets names every script it runs that way, so
# pkill and pidof by script name match nothing there): runtrimui.sh on stock
# and CrossMix firmware, trimui_launcher elsewhere. The brackets keep grep from
# finding its own command line.
loop_pids() {
    for LP_F in $(grep -l -e 'runtrimui[.]sh' -e 'trimui_launche[r]' /proc/[0-9]*/cmdline 2>/dev/null); do
        LP_P="${LP_F#/proc/}"
        LP_P="${LP_P%/cmdline}"
        case "$LP_P" in "" | *[!0-9]*) continue ;; esac
        stat_of "$LP_P" || continue
        [ "$ST_STATE" = Z ] || printf '%s ' "$LP_P"
    done
}

# Whether a process is a shell script or the firmware's launcher binary, by
# name or by executable: the only kind of MainUI parent that is ever paused,
# never a daemon that reads keys (the boot hook's rule too).
pausable() {
    PZ_C=""
    read -r PZ_C 2>/dev/null <"/proc/$1/comm"
    case "$PZ_C" in
        sh | ash | bash | dash | busybox | *.sh | trimui_launcher) return 0 ;;
    esac
    case "$(readlink "/proc/$1/exe" 2>/dev/null)" in
        */sh | */ash | */bash | */dash | */busybox) return 0 ;;
    esac
    return 1
}

# Pause a process and write it down in $PAUSED with its start time, so that
# the hand-back resumes exactly it (never a process that got its pid later).
pause_pid() {
    stat_of "$1" || return 1
    [ "$ST_STATE" = Z ] && return 1
    PP_START="$ST_START"
    kill -STOP "$1" 2>/dev/null || return 1
    if ! grep -q "^$1 $PP_START " "$PAUSED" 2>/dev/null; then
        PP_NAME=""
        read -r PP_NAME 2>/dev/null <"/proc/$1/comm"
        echo "$1 $PP_START ${PP_NAME:-?}" >>"$PAUSED" 2>/dev/null
    fi
    say "paused the firmware launcher: $(describe "$1")"
    return 0
}

# Whether a process is paused already and written down in $PAUSED (the boot
# hook's direct start). A line alone is not enough: one left behind by a start
# that gave up names a loop that runs again, or a pid another process now has.
paused_now() {
    stat_of "$1" && [ "$ST_STATE" = T ] && grep -q "^$1 $ST_START " "$PAUSED" 2>/dev/null
}

# Let everything in $PAUSED run again — what this script, the boot hook or the
# app paused — and forget it. RESUMED_PIDS keeps who was let go, for the
# hand-back's watcher.
RESUMED_PIDS=""
resume_paused() {
    [ -f "$PAUSED" ] || return 0
    RS_DONE=""
    while read -r RS_P RS_START RS_NAME; do
        case "$RS_P" in "" | *[!0-9]*) continue ;; esac
        case " $RS_DONE " in *" $RS_P "*) continue ;; esac
        if stat_of "$RS_P" && [ "$ST_START" = "$RS_START" ]; then
            kill -CONT "$RS_P" 2>/dev/null && RS_DONE="$RS_DONE $RS_P"
        fi
    done <"$PAUSED"
    rm -f "$PAUSED"
    if [ -n "$RS_DONE" ]; then
        say "let the firmware launcher run again (pid$RS_DONE)"
        RESUMED_PIDS="$RESUMED_PIDS$RS_DONE"
    fi
}

# The processes holding the display: the framebuffer, or the PowerVR GPU's
# nodes (anything that renders with GLES opens those). One pid per line.
display_holders() {
    ls -l /proc/[0-9]*/fd 2>/dev/null | {
        DH_P=""
        DH_LAST=""
        while read -r DH_L; do
            case "$DH_L" in
                /proc/*/fd:)
                    DH_P="${DH_L#/proc/}"
                    DH_P="${DH_P%/fd:}"
                    ;;
                *" -> /dev/fb0" | *" -> /dev/dri/"* | *" -> /dev/pvr_sync" | *" -> /dev/mali"*)
                    [ -n "$DH_P" ] && [ "$DH_P" != "$DH_LAST" ] && echo "$DH_P"
                    DH_LAST="$DH_P"
                    ;;
            esac
        done
    }
}

# A JukaHub that holds the display (a running UI, not a helper started from the
# same binary: the VPN tunnel or the update helper hold no display).
drawing_jukahub() {
    for DJ_P in $(pidof JukaHub 2>/dev/null || pgrep -x JukaHub 2>/dev/null); do
        [ "$DJ_P" = "$$" ] && continue
        stat_of "$DJ_P" || continue
        [ "$ST_STATE" = Z ] && continue
        if ls -l "/proc/$DJ_P/fd" 2>/dev/null | grep -q -e ' -> /dev/fb0$' -e ' -> /dev/dri/' -e ' -> /dev/pvr_sync$' -e ' -> /dev/mali'; then
            echo "$DJ_P"
            return 0
        fi
    done
    return 1
}

# A run that did not end with status 0 cannot have stopped the games it
# started: a panic off JukaHub's main goroutine, the out-of-memory killer and a
# crash in C code end the process at once, and the menu used to come back over
# a game that was still running. Each game leads a process group of its own,
# and JukaHub lists them in $SCREEN_GROUPS ("<group> <leader start time>") as
# they start and end. A listed group is stopped only while every live member is
# still one JukaHub could have started: in this script's session (JukaHub's
# children stay in it — but so may MainUI and the firmware's daemons: stock
# runtrimui.sh starts MainUI and this script without setsid, so the session
# alone proves nothing and the name check below must stay), a live leader that
# is exactly the process JukaHub listed (same start time: its id was not
# reused), no member older than that game, never MainUI, this script or one of
# its ancestors. Interrupt is the
# emulators' save-and-quit (what JukaHub itself sends), Continue wakes one
# paused under the game assistant, and whatever is left after three seconds
# is killed. The list goes after every run, so it can never act twice.
stop_screen_groups() {
    [ -f "$SCREEN_GROUPS" ] || return 0
    if ! stat_of $$; then
        rm -f "$SCREEN_GROUPS"
        return 0
    fi
    SG_SID="$ST_SID"
    SG_OWN="$ST_PGRP"
    SG_CAND=" "
    while read -r SG_G SG_T _; do
        case "$SG_G" in "" | 0 | 1 | *[!0-9]*) continue ;; esac
        # A game whose start time JukaHub could not read cannot be told from
        # a later process that got its id: left alone.
        case "$SG_T" in "" | 0 | *[!0-9]*) continue ;; esac
        [ "$SG_G" = "$$" ] && continue
        [ "$SG_G" = "$SG_OWN" ] && continue
        case "$ANCESTORS" in *" $SG_G "*) continue ;; esac
        case "$SG_CAND" in *" $SG_G "*) continue ;; esac
        SG_CAND="$SG_CAND$SG_G "
        eval "SG_T_$SG_G=\$SG_T"
    done <"$SCREEN_GROUPS"
    rm -f "$SCREEN_GROUPS"
    [ "$SG_CAND" = " " ] && return 0
    SG_LIVE=" "
    SG_BAD=" "
    for SG_F in /proc/[0-9]*/stat; do
        SG_P="${SG_F#/proc/}"
        SG_P="${SG_P%/stat}"
        stat_of "$SG_P" || continue
        case "$SG_CAND" in *" $ST_PGRP "*) ;; *) continue ;; esac
        [ "$ST_STATE" = Z ] && continue
        eval "SG_T=\$SG_T_$ST_PGRP"
        SG_C=""
        read -r SG_C 2>/dev/null <"/proc/$SG_P/comm"
        SG_WHY=""
        if [ "$ST_SID" != "$SG_SID" ]; then
            SG_WHY="another session"
        elif [ "$SG_C" = MainUI ]; then
            SG_WHY="MainUI"
        elif [ "$SG_P" = "$ST_PGRP" ] && [ "$SG_T" != 0 ] && [ "$ST_START" != "$SG_T" ]; then
            SG_WHY="not the game JukaHub listed (its process id was reused)"
        elif [ "$ST_START" -lt "$SG_T" ] 2>/dev/null; then
            SG_WHY="older than the game"
        else
            case "$ANCESTORS" in *" $SG_P "*) SG_WHY="this script's parent" ;; esac
        fi
        if [ -n "$SG_WHY" ]; then
            case "$SG_BAD" in *" $ST_PGRP "*) ;; *)
                SG_BAD="$SG_BAD$ST_PGRP "
                say "left process group $ST_PGRP alone: $(describe "$SG_P") is $SG_WHY"
                ;;
            esac
        else
            case "$SG_LIVE" in *" $ST_PGRP "*) ;; *) SG_LIVE="$SG_LIVE$ST_PGRP " ;; esac
        fi
    done
    SG_LIST=""
    for SG_G in $SG_LIVE; do
        case "$SG_BAD" in *" $SG_G "*) continue ;; esac
        SG_LIST="$SG_LIST $SG_G"
    done
    [ -n "$SG_LIST" ] || return 0
    say "JukaHub left a game running (process group$SG_LIST); stopping it before the menu comes back"
    for SG_G in $SG_LIST; do
        kill -INT -"$SG_G" 2>/dev/null
        kill -CONT -"$SG_G" 2>/dev/null
    done
    n=0
    while [ "$n" -lt 3 ]; do
        SG_UP=""
        for SG_G in $SG_LIST; do
            kill -0 -"$SG_G" 2>/dev/null && SG_UP=1
        done
        [ -n "$SG_UP" ] || return 0
        n=$((n + 1))
        sleep 1
    done
    for SG_G in $SG_LIST; do
        kill -KILL -"$SG_G" 2>/dev/null && say "process group $SG_G ignored the quit request; killed it"
    done
}

# This script's ancestors. The launcher loop that ran it (MainUI picked
# JukaHub, or the boot hook queued it) is one of them: that loop is waiting for
# the script to finish, so it can bring nothing back meanwhile, and it is never
# paused — a paused ancestor would stay paused if this script were killed.
ANCESTORS=" $$ "
CHAIN_PIDS=""
AN_P=$$
AN_N=0
while [ "$AN_N" -lt 32 ] && stat_of "$AN_P"; do
    case "$ST_PPID" in "" | 0 | 1 | *[!0-9]*) break ;; esac
    AN_P="$ST_PPID"
    ANCESTORS="$ANCESTORS$AN_P "
    [ "$AN_N" -lt 3 ] && CHAIN_PIDS="$CHAIN_PIDS $AN_P"
    AN_N=$((AN_N + 1))
done
# "started by ..." for the log, written once JukaHub is on its way (below):
# naming a shell's script forks two tools per ancestor.
STARTED_BY_TOLD=0
say_started_by() {
    [ "$STARTED_BY_TOLD" = 1 ] && return 0
    STARTED_BY_TOLD=1
    CHAIN=""
    for AN_P in $CHAIN_PIDS; do
        CHAIN="${CHAIN:+$CHAIN < }$(describe "$AN_P")"
    done
    say "started by ${CHAIN:-init}"
}

# ------------------------------------------------------- stop from outside
# The firmware's power handling (and any supervisor) stops an app by signalling
# the process group. The script is part of that group, so it used to die before
# reaching the handover below: MainUI stayed dead and the panel kept JukaHub's
# last frame, with no input able to leave it — the "device locked on the power
# button" report. Catching the stop keeps the script alive long enough to hand
# the screen back — set before anything is paused, so a stop that lands in the
# middle of the take-over still resumes what was paused.
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

# --------------------------------------------------------- one JukaHub only
# Two JukaHubs on one screen alternate frames just as two menus do. One that
# is still on its way out (a restart) gets five seconds; one that stays is
# left alone, and this start is called off without touching the firmware UI.
OTHER="$(drawing_jukahub)"
n=0
while [ -n "$OTHER" ] && [ "$n" -lt 5 ]; do
    n=$((n + 1))
    sleep 1
    OTHER="$(drawing_jukahub)"
done
if [ -n "$OTHER" ]; then
    say_started_by
    say "JukaHub is already on the screen ($(describe "$OTHER")); not starting a second one"
    exit 0
fi

# --------------------------------------------------- take over the display
# Normally the launcher has already stepped aside before this script runs.
# What would bring it back is paused first, by process id, and only ever a
# shell script (never init, never a daemon): MainUI's parent, the process that
# restarts it; and, when this script was not started by a launcher loop (a
# start over SSH, an older update helper), the loop itself even between two
# MainUIs — the top one only, not the subshells it runs in the background
# (they may watch keys or power). The loop that ran this script is waiting for
# it and is never paused. Then MainUI is stopped (SIGTERM first, SIGKILL only
# if it refuses to go).
ANCESTOR_LOOPS=""
OTHER_LOOPS=""
ALL_LOOPS=" $(loop_pids)"
for LP in $ALL_LOOPS; do
    case "$ANCESTORS" in
        *" $LP "*) ANCESTOR_LOOPS="$ANCESTOR_LOOPS $LP" ;;
        *)
            # A subshell of a loop has the loop's command line too.
            stat_of "$LP" || continue
            case "$ALL_LOOPS " in *" $ST_PPID "*) continue ;; esac
            OTHER_LOOPS="$OTHER_LOOPS $LP"
            ;;
    esac
done
if [ -z "$ANCESTOR_LOOPS" ]; then
    for LP in $OTHER_LOOPS; do
        [ "$LP" -gt 1 ] 2>/dev/null || continue
        pausable "$LP" || continue
        paused_now "$LP" || pause_pid "$LP"
    done
fi
UI_PID="$(ui_pid)"
for UP in $UI_PID; do
    stat_of "$UP" || continue
    UP_PARENT="$ST_PPID"
    case "$UP_PARENT" in "" | 0 | 1 | *[!0-9]*) continue ;; esac
    case "$ANCESTORS" in *" $UP_PARENT "*) continue ;; esac
    if pausable "$UP_PARENT"; then
        paused_now "$UP_PARENT" || pause_pid "$UP_PARENT"
    else
        say "MainUI is started by $(describe "$UP_PARENT"), which is not paused"
    fi
done

UI_SEEN="$UI_PID"
if [ -n "$UI_PID" ]; then
    say "stopping stock launcher (pid $UI_PID)"
    kill $UI_PID 2>/dev/null
    if ! wait_ui_gone 4; then
        UI_PID="$(ui_pid)"
        if [ -n "$UI_PID" ]; then
            kill -9 $UI_PID 2>/dev/null
            wait_ui_gone || :
        fi
    fi
fi

# Verify, don't assume. A refused kill used to be ignored, and JukaHub then
# started next to the stock launcher: both drew to the same framebuffer and the
# screen alternated between them. If MainUI will not die, hand the screen back
# instead of starting a second UI. With no MainUI to stop (the usual start: the
# menu queued JukaHub and exited, or the boot hook never let it open) there is
# nothing to verify.
if ! command -v pidof >/dev/null 2>&1 && ! command -v pgrep >/dev/null 2>&1; then
    # Nothing can inspect the process list, so wait out the handover instead
    # of guessing that the display is free.
    sleep 1
elif [ -n "$UI_SEEN" ]; then
    n=0
    while [ -n "$(ui_pid)" ] && [ "$n" -lt 8 ]; do
        n=$((n + 1))
        say "MainUI still running; kill -9 attempt $n"
        # One argument per pid: pidof can name more than one MainUI.
        kill -9 $(ui_pid) 2>/dev/null
        wait_ui_gone || :
    done
    if [ -n "$(ui_pid)" ]; then
        say_started_by
        say "FATAL: MainUI still holds the display after $n attempts; not starting JukaHub"
        resume_paused
        exit 1
    fi
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
# The Smart Pro S (A523, Mali-G57) shows its picture through DRM/KMS with its
# own SDL in /usr/lib; leaving SDL_VIDEODRIVER unset lets that SDL pick its
# backend there too, and nothing below assumes /dev/fb0. /usr/trimui/lib is
# only searched, so a firmware without it loses nothing.
LD_LIBRARY_PATH="$APP_DIR:$APP_DIR/libs:/usr/trimui/lib:/usr/lib:/lib:$LD_LIBRARY_PATH"
PATH="$APP_DIR:$APP_DIR/required:$PATH"
HOME="$APP_DIR"
# Stock Smart Pro firmware has a usable CA bundle, but its old curl was built
# with a different default path and rejects the tested HTTPS hosts without
# SSL_CERT_FILE. Export the firmware's own bundle for curl and other OpenSSL
# helpers started by JukaHub. Keep an explicit owner-provided path.
if [ -r /etc/ssl/certs/ca-certificates.crt ]; then
    : "${SSL_CERT_FILE:=/etc/ssl/certs/ca-certificates.crt}"
    export SSL_CERT_FILE
fi
# Read by JukaHub before an update to tell it that this script will do the
# restarting. Older launchers do not set it, and the app then falls back to the
# helper's own relaunch path.
JUKAHUB_LAUNCHED_BY=launch.sh
# This launcher waits for a screen helper (the header's screen-handoff
# protocol). Exported by the running script, so a newer launch.sh copied onto
# the card under an older running one never makes the app expect a wait.
JUKAHUB_SCREEN_HANDOFF=1
# This launcher resumes everything in /tmp/jukahub-paused at the hand-back
# (the header's paused record), so the app leaves what it paused to it.
JUKAHUB_PAUSED_RECORD=1
# This launcher watches an update's first starts (the header's update
# probation): only then does the app write a plan before an update.
JUKAHUB_UPDATE_PROBATION=1
export LD_LIBRARY_PATH PATH HOME JUKAHUB_LAUNCHED_BY JUKAHUB_SCREEN_HANDOFF JUKAHUB_PAUSED_RECORD JUKAHUB_UPDATE_PROBATION
# This launcher lends the SD card out when JukaHub asks (usb_storage_handoff):
# not under NextUI, whose own menu runs from the card.
if [ "${JUKAHUB_HOST:-}" = nextui ] || [ -n "${SHARED_USERDATA_PATH:-}" ]; then
    unset JUKAHUB_USB_STORAGE
else
    JUKAHUB_USB_STORAGE=1
    export JUKAHUB_USB_STORAGE
fi

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

# ------------------------------------------------- CPU governor and limits
# JukaHub sets the CPU governor and frequency limits for its power modes and
# its idle screens, and puts back what it found when it quits. A JukaHub that
# is killed, or that crashes off its main goroutine, cannot: the firmware menu
# then ran on what JukaHub left (an idle screen's 600 MHz powersave ceiling)
# until JukaHub next started. So the values are noted before each start and
# put back after each exit wherever they differ; after a clean quit nothing
# differs and nothing is written.
cpu_dirs() {
    CD_ANY=""
    for CD_D in /sys/devices/system/cpu/cpufreq/policy[0-9]*; do
        [ -d "$CD_D" ] && { echo "$CD_D"; CD_ANY=1; }
    done
    [ -n "$CD_ANY" ] && return 0
    for CD_D in /sys/devices/system/cpu/cpu[0-9]*/cpufreq; do
        [ -d "$CD_D" ] && echo "$CD_D"
    done
}
CPU_SNAP=""
cpu_snapshot() {
    CPU_SNAP=""
    for CS_D in $(cpu_dirs); do
        for CS_K in scaling_governor scaling_min_freq scaling_max_freq; do
            [ -f "$CS_D/$CS_K" ] || continue
            # read, not cat: a fork per value is on the start's path.
            CS_V=""
            read -r CS_V 2>/dev/null <"$CS_D/$CS_K"
            case "$CS_V" in "" | *[!A-Za-z0-9_]*) continue ;; esac
            CPU_SNAP="$CPU_SNAP$CS_D/$CS_K $CS_V
"
        done
    done
}
cpu_restore() {
    [ -n "$CPU_SNAP" ] || return 0
    CR_FIXED=""
    # Twice: a floor cannot rise above the ceiling in force, nor the ceiling
    # drop below the floor, so one of the pair may only land on the second go.
    for CR_PASS in 1 2; do
        while read -r CR_F CR_V; do
            [ -n "$CR_F" ] || continue
            CR_NOW=""
            read -r CR_NOW 2>/dev/null <"$CR_F"
            [ "$CR_NOW" = "$CR_V" ] && continue
            echo "$CR_V" >"$CR_F" 2>/dev/null
            if [ "$CR_PASS" = 1 ]; then
                CR_FIXED="$CR_FIXED ${CR_F#/sys/devices/system/cpu/} $CR_NOW->$CR_V"
            fi
        done <<EOF
$CPU_SNAP
EOF
    done
    [ -n "$CR_FIXED" ] && say "CPU settings differed from before the start; put back:$CR_FIXED"
}

# JukaHub OS reads a large executable from the SD card before its first frame.
# The stock Smart Pro leaves that card at 128 KB read-ahead. Raise only the SD
# disk (never the eMMC) while this OS session runs, then put its old value back
# when the firmware's menu returns. The standalone image does the same in init.
CARD_RA_PATH=""
CARD_RA_OLD=""
card_readahead_start() {
    [ -z "$CARD_RA_PATH" ] || return 0
    RA_DEV=""
    while read -r RA_SOURCE RA_MOUNT RA_REST; do
        if [ "$RA_MOUNT" = /mnt/SDCARD ]; then
            RA_DEV="$RA_SOURCE"
            break
        fi
    done </proc/mounts
    case "$RA_DEV" in /dev/mmcblk[0-9]*p[0-9]*) ;; *) return 0 ;; esac
    RA_DISK="${RA_DEV#/dev/}"
    RA_DISK="${RA_DISK%p*}"
    RA_TYPE=""
    read -r RA_TYPE 2>/dev/null <"/sys/block/$RA_DISK/device/type" || return 0
    [ "$RA_TYPE" = SD ] || return 0
    RA_PATH="/sys/block/$RA_DISK/queue/read_ahead_kb"
    RA_OLD=""
    read -r RA_OLD 2>/dev/null <"$RA_PATH" || return 0
    case "$RA_OLD" in "" | *[!0-9]*) return 0 ;; esac
    [ "$RA_OLD" -lt 1024 ] || return 0
    if printf '1024\n' >"$RA_PATH" 2>/dev/null; then
        CARD_RA_PATH="$RA_PATH"
        CARD_RA_OLD="$RA_OLD"
        say "SD card read-ahead: $RA_DISK $RA_OLD -> 1024 KB for JukaHub OS"
    fi
}
card_readahead_restore() {
    [ -n "$CARD_RA_PATH" ] || return 0
    if printf '%s\n' "$CARD_RA_OLD" >"$CARD_RA_PATH" 2>/dev/null; then
        say "SD card read-ahead restored to $CARD_RA_OLD KB"
    fi
    CARD_RA_PATH=""
    CARD_RA_OLD=""
}

# Stock firmware starts trimui_osdd just before MainUI. Its input listener can
# put the TrimUI quick menu over JukaHub OS when a pad button is pressed; it
# also holds the framebuffer and ALSA playback device open. JukaHub OS has its
# own controls, so close only the verified firmware OSD during an OS session.
# The firmware restarts it on its next loop; restore it ourselves as well for a
# launch from SSH or a firmware whose loop does not return to MainUI.
OSD_RESTART_EXE=""
OSD_RESTART_LIBS="${JUKAHUB_ORIG_LD_LIBRARY_PATH:-/usr/trimui/lib}"
quiet_firmware_osd() {
    [ -z "$OSD_RESTART_EXE" ] || return 0
    for OSD_P in $(pidof trimui_osdd 2>/dev/null || pgrep -x trimui_osdd 2>/dev/null); do
        OSD_EXE="$(readlink "/proc/$OSD_P/exe" 2>/dev/null)"
        case "$OSD_EXE" in
            /usr/trimui/osd/trimui_osdd | /mnt/SDCARD/trimui/osd/trimui_osdd | /mnt/UDISK/trimui/osd/trimui_osdd) ;;
            *) continue ;;
        esac
        if kill -KILL "$OSD_P" 2>/dev/null; then
            OSD_RESTART_EXE="$OSD_EXE"
            say "paused TrimUI's quick menu for JukaHub OS"
        fi
    done
}
restore_firmware_osd() {
    [ -n "$OSD_RESTART_EXE" ] || return 0
    OSD_EXE="$OSD_RESTART_EXE"
    OSD_RESTART_EXE=""
    [ -x "$OSD_EXE" ] || return 0
    [ -z "$(pidof trimui_osdd 2>/dev/null || pgrep -x trimui_osdd 2>/dev/null)" ] || return 0
    (
        cd "${OSD_EXE%/*}" || exit 1
        LD_LIBRARY_PATH="$OSD_RESTART_LIBS"
        export LD_LIBRARY_PATH
        exec "$OSD_EXE"
    ) </dev/null >/dev/null 2>&1 &
    say "TrimUI's quick menu restored for the stock menu"
}

# ------------------------------------------------------------ update check
# The header's update probation. player/updrescue.go writes the plan and reads
# the note this leaves; player/startcheck.go writes .start-check. All of it
# runs only while a plan exists, never on an ordinary start.

# A file's size in bytes, "" when it is missing; from ls, which reads no data,
# else wc. Call it in $(...): it sets -f and its arguments.
file_size() {
    [ -f "$1" ] || return 1
    set -f
    FS_F="$1"
    # shellcheck disable=SC2046
    set -- $(ls -ln "$FS_F" 2>/dev/null)
    case "$5" in
        "" | *[!0-9]*)
            # shellcheck disable=SC2046
            set -- $(wc -c <"$FS_F" 2>/dev/null)
            echo "$1"
            ;;
        *) echo "$5" ;;
    esac
}

# The plan is over: the update started fine, was undone, or cannot be watched.
# The backups kept aside for it go at JukaHub's next start.
probation_drop() {
    rm -f "$PROBATION" "$PROBATION_TRIES" "$START_CHECK" "$PROBATION_KILL"
}

# The plan's head into PB_*; false for a plan this script cannot follow
# (another format, no JukaHub size, no file).
probation_read() {
    TAB="$(printf '\t')"
    PB_VERSION="" PB_FROM="" PB_TO="" PB_JOURNAL="" PB_SAVED="" PB_EXE="" PB_FILES=0
    while IFS= read -r PR_L; do
        case "$PR_L" in
            "version "*) PB_VERSION="${PR_L#version }" ;;
            "from "*) PB_FROM="${PR_L#from }" ;;
            "to "*) PB_TO="${PR_L#to }" ;;
            "journal "*) PB_JOURNAL="${PR_L#journal }" ;;
            "saved "*) PB_SAVED="${PR_L#saved }" ;;
            "exe "*) PB_EXE="${PR_L#exe }" ;;
            "file "*) PB_FILES=$((PB_FILES + 1)) ;;
        esac
    done <"$PROBATION"
    [ "$PB_VERSION" = 1 ] || return 1
    case "$PB_EXE" in "" | *[!0-9]*) return 1 ;; esac
    [ "$PB_FILES" -gt 0 ]
}

# Before a start: is it under the plan, and how long does it get? Sets
# PROBATION_ON, PB_LIMIT and PB_LOG_AT (errors.txt's size: where this start's
# output begins, for the report).
PROBATION_ON=0
probation_begin() {
    PROBATION_ON=0
    rm -f "$START_CHECK" "$PROBATION_KILL"
    case "$SWAP_VERDICT" in
        failed | none)
            say "update check: the update did not finish copying, so this start is not watched (JukaHub finishes or undoes it as it starts)"
            probation_drop
            return 0
            ;;
    esac
    if ! probation_read; then
        say "update check: .update-probation is not a plan this launcher can follow; this start is not watched"
        probation_drop
        return 0
    fi
    # Copies a restore was still making when the power went (no marker was
    # written for them, so they may be partial): a later restore makes its own.
    probation_clean_tmp
    # Only the JukaHub the update installed is on probation, not one copied
    # over it by hand since.
    PB_SIZE="$(file_size "$APP_DIR/JukaHub")"
    if [ "$PB_SIZE" != "$PB_EXE" ]; then
        say "update check: JukaHub is not the build the update installed (${PB_SIZE:-no} bytes, the update's has $PB_EXE); not watching it"
        probation_drop
        return 0
    fi
    PB_TRY=""
    read -r PB_TRY _ 2>/dev/null <"$PROBATION_TRIES"
    # A damaged count starts again (a long number would stop dash's $(( ))).
    case "$PB_TRY" in "" | *[!0-9]* | ????*) PB_TRY=0 ;; esac
    PB_TRY=$((PB_TRY + 1))
    if [ "$PB_TRY" -gt 5 ]; then
        say "update check: $PB_TO had five starts that ended without a verdict; no longer watching it"
        probation_drop
        return 0
    fi
    echo "$PB_TRY" >"$PROBATION_TRIES" 2>/dev/null
    PB_LIMIT="$PROBATION_LIMIT"
    [ "$PB_TRY" -gt 1 ] && PB_LIMIT="$PROBATION_RETRY_LIMIT"
    PB_LOG_AT="$(file_size "$LOG")"
    case "$PB_LOG_AT" in "" | *[!0-9]*) PB_LOG_AT=-1 ;; esac
    PROBATION_ON=1
    say "update check: start $PB_TRY of $PB_TO; ${PB_FROM:-the previous version} comes back if it shows no working screen within ${PB_LIMIT}s"
    # The count has to outlast a power-off on a black screen.
    sync
}

# The watchdog, in the background beside `wait`, for the app's pid $1 and $2
# seconds: "ready" from that process, or the plan gone, ends it; "noinput", or
# no "ready" in time, stops the start — SIGABRT first, so the Go runtime
# prints every goroutine's stack into errors.txt (the diagnosis; JukaHub turns
# SIGQUIT into a quit once its main runs), SIGKILL five seconds later. Why
# goes to $PROBATION_KILL for the verdict. The start time guards against a
# reused pid.
probation_watch() {
    stat_of "$1" || exit 0
    PW_START="$ST_START"
    PW_N=0
    PW_WHY=hang
    while [ "$PW_N" -lt "$2" ]; do
        sleep 1
        PW_N=$((PW_N + 1))
        stat_of "$1" && [ "$ST_START" = "$PW_START" ] && [ "$ST_STATE" != Z ] || exit 0
        # Time it spends paused (SIGSTOP, a debugger) is not its start's.
        case "$ST_STATE" in
            T | t)
                PW_N=$((PW_N - 1))
                continue
                ;;
        esac
        [ -f "$PROBATION" ] || exit 0
        PW_STAGE="" PW_PID=""
        read -r PW_STAGE PW_PID _ 2>/dev/null <"$START_CHECK"
        [ "$PW_PID" = "$1" ] || continue
        case "$PW_STAGE" in
            ready) exit 0 ;;
            noinput)
                PW_WHY=noinput
                break
                ;;
        esac
    done
    stat_of "$1" && [ "$ST_START" = "$PW_START" ] && [ "$ST_STATE" != Z ] || exit 0
    echo "$PW_WHY $PW_N" >"$PROBATION_KILL" 2>/dev/null
    if [ "$PW_WHY" = noinput ]; then
        say "update check: JukaHub has no working input ${PW_N}s into its start; stopping it"
    else
        say "update check: JukaHub showed no working screen within ${PW_N}s; stopping it (its goroutine stacks follow)"
    fi
    kill -ABRT "$1" 2>/dev/null
    PW_N=0
    while [ "$PW_N" -lt 5 ]; do
        sleep 1
        PW_N=$((PW_N + 1))
        stat_of "$1" && [ "$ST_START" = "$PW_START" ] && [ "$ST_STATE" != Z ] || exit 0
    done
    kill -KILL "$1" 2>/dev/null && say "update check: JukaHub did not stop; killed it"
    exit 0
}

# One "file" line of the plan into PF_NAME, PF_STORED, PF_SIZE and PF_MODE;
# false for anything that could reach outside the app folder or the backups'.
probation_file_fields() {
    PF_L="${1#file }"
    case "$PF_L" in *"$TAB"*) ;; *) return 1 ;; esac
    PF_NAME="${PF_L%%"$TAB"*}"
    PF_L="${PF_L#*"$TAB"}"
    PF_STORED="${PF_L%%"$TAB"*}"
    PF_SIZE="" PF_MODE=""
    case "$PF_L" in
        *"$TAB"*)
            PF_L="${PF_L#*"$TAB"}"
            PF_SIZE="${PF_L%%"$TAB"*}"
            case "$PF_L" in *"$TAB"*) PF_MODE="${PF_L#*"$TAB"}" ;; esac
            ;;
    esac
    case "$PF_NAME" in "" | /* | */ | . | .. | ./* | ../* | */. | */.. | */./* | */../* | *//*) return 1 ;; esac
    case "$PF_STORED" in "" | .* | */*) return 1 ;; esac
    case "$PF_SIZE" in *[!0-9]*) return 1 ;; esac
    case "$PF_MODE" in "" | [0-7][0-7][0-7] | [0-7][0-7][0-7][0-7]) ;; *) return 1 ;; esac
    return 0
}

# Where the plan's backup $1 is now, in PF_SRC: the journal's folder, or where
# JukaHub's next start moved it (a saved version, or kept aside).
probation_find() {
    PF_SRC=""
    while IFS= read -r PD_L; do
        case "$PD_L" in "dir "*) ;; *) continue ;; esac
        PD_D="${PD_L#dir }"
        case "$PD_D" in "" | .. | ../* | */../* | */..) continue ;; esac
        case "$PD_D" in /*) ;; *) PD_D="$APP_DIR/$PD_D" ;; esac
        if [ -f "$PD_D/$1" ]; then
            PF_SRC="$PD_D/$1"
            return 0
        fi
    done <"$PROBATION"
    return 1
}

# Removes the copies a restore that stopped left beside their targets.
probation_clean_tmp() {
    while IFS= read -r PC_L; do
        case "$PC_L" in "file "*) ;; *) continue ;; esac
        probation_file_fields "$PC_L" && rm -f "$APP_DIR/$PF_NAME.rescue-tmp"
    done <"$PROBATION"
}

# Puts every file of the plan back ($1 why the start failed, $2 how far it
# got, for the note): first every backup found and whole, then each copied
# beside its target, then the note written as the marker ($RESTORING) and
# synced, then all renamed over their targets (probation_finish). Nothing is
# replaced unless every copy is ready. False, with the reason said, when the
# previous version is not back in full.
probation_restore() {
    PR_ERR=""
    while IFS= read -r PR_L; do
        case "$PR_L" in "file "*) ;; *) continue ;; esac
        if ! probation_file_fields "$PR_L"; then
            PR_ERR="the plan has an unusable line"
            break
        fi
        if ! probation_find "$PF_STORED"; then
            PR_ERR="the saved copy of $PF_NAME is missing"
            break
        fi
        if [ -n "$PF_SIZE" ] && [ "$(file_size "$PF_SRC")" != "$PF_SIZE" ]; then
            PR_ERR="the saved copy of $PF_NAME is damaged"
            break
        fi
    done <"$PROBATION"
    if [ -z "$PR_ERR" ]; then
        while IFS= read -r PR_L; do
            case "$PR_L" in "file "*) ;; *) continue ;; esac
            if ! probation_file_fields "$PR_L" || ! probation_find "$PF_STORED"; then
                PR_ERR="the plan changed"
                break
            fi
            if ! cp -f "$PF_SRC" "$APP_DIR/$PF_NAME.rescue-tmp" 2>/dev/null; then
                PR_ERR="copying $PF_NAME failed (is the card full?)"
                break
            fi
            [ -z "$PF_MODE" ] || chmod "$PF_MODE" "$APP_DIR/$PF_NAME.rescue-tmp" 2>/dev/null
        done <"$PROBATION"
    fi
    if [ -n "$PR_ERR" ]; then
        probation_clean_tmp
        say "update check: cannot put the previous version back: $PR_ERR"
        return 1
    fi
    # The copies reach the card before the marker does, and the marker before
    # the first rename: a marker found after a power cut means every copy
    # still beside its target is whole.
    sync
    if ! probation_note "$1" "$2" "$RESTORING"; then
        probation_clean_tmp
        say "update check: cannot put the previous version back: writing $RESTORING failed (is the card full?)"
        return 1
    fi
    sync
    probation_finish
}

# Renames each copy still beside its target over it (the release stamp last,
# as the plan lists it; a copy already gone was put in place before a power
# cut), removes the stamp the update added, then turns the marker into the
# note for the restored build. Shared by probation_restore and, after a power
# cut, probation_resume. False, with the reason said, when a copy could not
# be put in place.
probation_finish() {
    PR_FAIL=""
    while IFS= read -r PR_L; do
        case "$PR_L" in
            "file "*)
                probation_file_fields "$PR_L" || continue
                [ -f "$APP_DIR/$PF_NAME.rescue-tmp" ] || continue
                mv -f "$APP_DIR/$PF_NAME.rescue-tmp" "$APP_DIR/$PF_NAME" 2>/dev/null || PR_FAIL="$PR_FAIL $PF_NAME"
                ;;
            "drop "*)
                PR_D="${PR_L#drop }"
                case "$PR_D" in "" | /* | .. | ../* | */../* | */..) continue ;; esac
                rm -f "$APP_DIR/$PR_D"
                ;;
        esac
    done <"$PROBATION"
    # FAT cards may refuse chmod; the copy's mode then comes from the mount.
    chmod 755 "$APP_DIR/JukaHub" 2>/dev/null
    sync
    if [ -n "$PR_FAIL" ]; then
        probation_clean_tmp
        rm -f "$RESTORING"
        say "update check: putting the previous version back stopped halfway; not replaced:$PR_FAIL"
        return 1
    fi
    mv -f "$RESTORING" "$RESCUED" 2>/dev/null
    return 0
}

# A restore that a power cut (or anything else that ended this script) stopped
# between its renames left the marker: every copy it made was whole and on the
# card by then, so the rest are renamed in now, before JukaHub starts, the note
# reaches the restored build as after any restore, and the plan ends. Without
# its plan only the marker goes.
probation_resume() {
    TAB="$(printf '\t')"
    if [ ! -f "$PROBATION" ]; then
        rm -f "$RESTORING"
        say "update check: removed a restore marker whose plan is gone"
        return 0
    fi
    say "update check: a restore of the previous version was interrupted; finishing it"
    (
        trap '' TERM INT HUP
        probation_finish && say "update check: the previous version is back; starting it"
        probation_drop
        rm -f "$RESTART_NOW" "$HANDOFF"
        sync
    )
}

# How the start under the plan went, once it has exited. True when it failed
# and is undone: PV_WHY (hang, noinput, crash) and PV_SEEN (frame when it drew
# one, else none). A start that was ready ends the plan; one stopped from
# outside, or one that quit cleanly before it was ready, keeps it for the next
# start; one that started another install leaves it to that install.
probation_verdict() {
    PV_STAGE="" PV_PID=""
    read -r PV_STAGE PV_PID _ 2>/dev/null <"$START_CHECK"
    [ "$PV_PID" = "$LAST_PID" ] || PV_STAGE=""
    PV_WHY=""
    read -r PV_WHY _ 2>/dev/null <"$PROBATION_KILL"
    rm -f "$PROBATION_KILL"
    if [ "$PV_STAGE" = ready ]; then
        say "update check: $PB_TO started fine; no longer watching it"
        probation_drop
        return 1
    fi
    if [ -f "$PENDING" ]; then
        say "update check: JukaHub is installing something else; that install takes over"
        return 1
    fi
    if [ ! -f "$PROBATION" ]; then
        # Withdrawn by the app (a rollback took over).
        rm -f "$PROBATION_TRIES" "$START_CHECK"
        return 1
    fi
    if [ "$STOPPED" -eq 1 ]; then
        say "update check: JukaHub was stopped from outside before it was ready; its next start is watched again"
        return 1
    fi
    case "$PV_STAGE" in frame | noinput) PV_SEEN=frame ;; *) PV_SEEN=none ;; esac
    if [ -z "$PV_WHY" ]; then
        if [ "$PV_STAGE" = noinput ]; then
            PV_WHY=noinput
        elif [ "$RC" -ne 0 ]; then
            PV_WHY=crash
        else
            say "update check: JukaHub quit before it was ready (exit code 0); its next start is watched again"
            return 1
        fi
    fi
    return 0
}

# The note for the restored build ($1 why, $2 how far it got), as $3 (default
# $RESCUED; probation_restore writes it as the marker first). False when it
# could not be written.
probation_note() {
    PN_TO="${3:-$RESCUED}"
    {
        echo "# launch.sh undid an update that did not start; JukaHub reads and removes this (player/updrescue.go)"
        echo "from $PB_FROM"
        echo "to $PB_TO"
        echo "journal $PB_JOURNAL"
        echo "saved $PB_SAVED"
        echo "reason $1"
        echo "rc $RC"
        echo "stage $2"
        echo "pid $LAST_PID"
        echo "log $PB_LOG_AT"
    } >"$PN_TO.tmp" 2>/dev/null && mv -f "$PN_TO.tmp" "$PN_TO"
}

# -------------------------------------------------------- USB storage mode
# JukaHub quit to lend the SD card to a computer as a USB drive
# (player/usbstorage.go): it left $USB_STORAGE_REQ and its helper in /tmp. The
# card cannot be unmounted while this script runs - the shell reads it from
# the card, and the shell that started it has its working folder there - so
# this script does not wait: the firmware's launcher loop above it is paused
# (written down in $PAUSED, so the stock menu stays closed), the helper starts
# on its own (a session of its own: the power key's signals to this group do
# not reach it) and this script ends without handing the screen back. The
# helper waits for the shells up to that loop to finish, lends the card out,
# mounts it back and starts this script anew; that run's hand-back lets the
# loop go on. Returns 1 when nothing was started (the reason is left in
# /tmp/jukahub-usbstorage.result, which the next JukaHub shows).
usb_storage_handoff() {
    if ! sed -n 2p "$USB_STORAGE_HELPER" 2>/dev/null | grep -q 'jukahub-usbstorage-helper: 1'; then
        echo "failed JukaHub could not prepare the USB storage helper" >/tmp/jukahub-usbstorage.result
        return 1
    fi
    # Started by the boot hook's own copy, not through the loop: that copy
    # waits on the card for this script and lets the loop go when it ends.
    for AN_P in $ANCESTORS; do
        case "$(tr '\0' ' ' <"/proc/$AN_P/cmdline" 2>/dev/null)" in
            *jukahub-autostart*)
                echo "failed JukaHub was started straight from the boot hook this time; open it from the stock menu to use USB storage mode" >/tmp/jukahub-usbstorage.result
                return 1
                ;;
        esac
    done
    # The helper waits for this script and the shells up to the loop.
    US_WAIT=""
    for AN_P in $ANCESTORS; do
        case " $ANCESTOR_LOOPS " in *" $AN_P "*) break ;; esac
        US_WAIT="$US_WAIT $AN_P"
    done
    # A loop left running would bring the stock menu back over a lent card;
    # one paused here before a refusal is let go at this session's hand-back.
    for LP in $ANCESTOR_LOOPS; do
        stat_of "$LP" && [ "$ST_STATE" != Z ] || continue
        if ! pausable "$LP" || ! pause_pid "$LP"; then
            echo "failed The stock menu could not be held back while the card is away" >/tmp/jukahub-usbstorage.result
            return 1
        fi
    done
    say "USB storage mode: handing the SD card to the helper in /tmp (it starts JukaHub again)"
    # No library path into the card for the helper's own shell: a library it
    # loaded from there would hold the card (the helper restores the
    # original for the start at the end).
    # shellcheck disable=SC2086
    if command -v setsid >/dev/null 2>&1; then
        LD_LIBRARY_PATH= setsid /bin/sh "$USB_STORAGE_HELPER" "$USB_STORAGE_REQ" "$APP_DIR" $US_WAIT </dev/null >/dev/null 2>&1 &
    else
        (
            trap '' HUP
            LD_LIBRARY_PATH= exec /bin/sh "$USB_STORAGE_HELPER" "$USB_STORAGE_REQ" "$APP_DIR" $US_WAIT
        ) </dev/null >/dev/null 2>&1 &
    fi
    return 0
}

# --------------------------------------------------------------- run it
if [ ! -x "$APP_DIR/JukaHub" ]; then
    say "FATAL: binary $APP_DIR/JukaHub not found or not executable"
    chmod +x "$APP_DIR/JukaHub" 2>/dev/null
fi

# How the last update swap's helper ended (swapped, failed, none), for the
# update check at the next start; set where the loop waits for it, below.
SWAP_VERDICT=""
while :; do
    # Also covers a stop that arrived while waiting for an update helper.
    [ "$STOPPED" -eq 1 ] && break
    cpu_snapshot
    # The first starts of an update are watched (the update check above). A
    # restore a power cut stopped is finished first.
    PROBATION_ON=0
    [ -f "$RESTORING" ] && probation_resume
    [ -f "$PROBATION" ] && probation_begin
    SWAP_VERDICT=""
    # JukaHub OS (docs/JUKAHUB_OS.md). With OS mode on in os/os.json, JukaHub
    # starts as "JukaHub --os-session": it applies its layers in a private
    # mount namespace and executes itself again inside it — the same process,
    # so the wait, the exit status and .last-exit below are unchanged. Read on
    # every pass, so "Start OS session now" (a clean exit with .restart-now)
    # comes back in the new mode. JukaHub checks the file again itself and
    # starts as the app when OS mode cannot run; an older JukaHub ignores the
    # flag. The pattern is the boot hook's too (autostart.go): any spacing, and
    # a file saved on Windows (CRLF) still counts.
    if [ -f "$APP_DIR/os/os.json" ] && grep -q '"enabled"[[:space:]]*:[[:space:]]*true' "$APP_DIR/os/os.json" 2>/dev/null; then
        card_readahead_start
        quiet_firmware_osd
        say "starting JukaHub OS (os/os.json: OS mode on; what it applied follows as \"jukaos:\" lines)"
        "$APP_DIR/JukaHub" --os-session >>"$LOG" 2>&1 &
    else
        card_readahead_restore
        restore_firmware_osd
        # Why this start is the app, for a report from a card that should
        # have booted JukaHub OS.
        if [ -f "$APP_DIR/os/os.json" ]; then
            say "os/os.json has OS mode off; starting as the app"
        elif [ -d "$APP_DIR/os" ]; then
            say "no os/os.json; starting as the app"
        fi
        "$APP_DIR/JukaHub" >>"$LOG" 2>&1 &
    fi
    APP_PID=$!
    WATCH_PID=""
    if [ "$PROBATION_ON" = 1 ]; then
        probation_watch "$APP_PID" "$PB_LIMIT" </dev/null >/dev/null 2>&1 &
        WATCH_PID=$!
        # Its start time: the wait below also collects a watchdog that ended
        # by itself ("ready"), and by JukaHub's exit its id may be another's.
        WATCH_START=""
        stat_of "$WATCH_PID" && WATCH_START="$ST_START"
    fi
    # Who started this script, now that JukaHub is on its way (first pass).
    say_started_by
    wait "$APP_PID"
    RC=$?
    # The watchdog goes as soon as the app is gone or stopping: it must never
    # act on a later process. Only while it is still the one started above.
    if [ -n "$WATCH_PID" ] && stat_of "$WATCH_PID" && [ "$ST_START" = "$WATCH_START" ]; then
        kill "$WATCH_PID" 2>/dev/null
    fi
    # A stop signal interrupts wait, not the app. Wait again so the display is
    # only touched once JukaHub has really exited (its own watchdog bounds
    # this to a few seconds even if a cleanup step is stuck). Not kill -0: the
    # stop handler's say may have collected a JukaHub that quit at once, and
    # the shell still holds its status for this wait. Only a first wait that
    # already reported it makes the shell answer 127; that status stands then.
    if [ "$STOPPED" -eq 1 ]; then
        wait "$APP_PID"
        RC_AGAIN=$?
        [ "$RC_AGAIN" -eq 127 ] || RC="$RC_AGAIN"
    fi
    # The watchdog is gone before the verdict below reads what it left.
    if [ -n "$WATCH_PID" ]; then
        wait "$WATCH_PID" 2>/dev/null
        WATCH_PID=""
    fi
    # The status tells the next start's boot guard a crash or a kill (2, 134,
    # 137, 139, ...) from a quit; the pid tells it the line is about the run it
    # recorded. A failed run needs this line flushed for recovery.
    { echo "$RC $APP_PID" >"$LAST_EXIT"; } 2>/dev/null
    LAST_PID="$APP_PID"
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
    # A clean quit stopped its games itself; after anything else one may still
    # hold the screen, and the menu must not come back over it.
    if [ "$RC" -eq 0 ]; then
        rm -f "$SCREEN_GROUPS"
    else
        stop_screen_groups
    fi
    # A clean quit already synced in JukaHub's teardown. Syncing every dirty
    # card buffer a second time here delayed the Brick's hand-back most when
    # a download had just been active. Failed runs still flush the exit code
    # before the menu or next build starts, for boot-guard recovery.
    cpu_restore
    [ "$RC" -eq 0 ] || sync

    # The update check's verdict (see the header). A start that failed it is
    # undone and the restored build starts right away, in this same session.
    # Once per plan (the restore ends it), and a restore that cannot be done in
    # full ends the plan too and falls through to what happens without one.
    if [ "$PROBATION_ON" = 1 ]; then
        PROBATION_ON=0
        if probation_verdict; then
            say "update check: $PB_TO did not start ($PV_WHY, exit code $RC); putting ${PB_FROM:-the previous version} back"
            # In a subshell that ignores the stop signals: the power key sends
            # them to the whole group, and a cp or mv they cut short would
            # leave the plan dropped with the failed build in place, or half
            # of each. A stop still ends the session once this is done.
            if (
                trap '' TERM INT HUP
                probation_restore "$PV_WHY" "$PV_SEEN" || exit 1
                probation_drop
                rm -f "$RESTART_NOW" "$HANDOFF"
                sync
                exit 0
            ); then
                say "update check: ${PB_FROM:-the previous version} is back; starting it"
                continue
            fi
            probation_drop
        fi
    fi

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

    # JukaHub asked to lend the SD card out (USB storage mode, see
    # usb_storage_handoff). Only after a clean exit with no update under way;
    # a helper that cannot be started brings JukaHub straight back, which
    # says why.
    if [ -f "$USB_STORAGE_REQ" ]; then
        if [ "$RC" -eq 0 ] && [ ! -f "$PENDING" ]; then
            if usb_storage_handoff; then
                card_readahead_restore
                restore_firmware_osd
                exit 0
            fi
            rm -f "$USB_STORAGE_REQ"
            say "USB storage mode not started: $(head -n 1 /tmp/jukahub-usbstorage.result 2>/dev/null)"
            continue
        fi
        rm -f "$USB_STORAGE_REQ"
    fi

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
        SWAP_VERDICT=failed
        say "swap helper reported a failure; JukaHub finishes or undoes the update as it starts"
    elif [ ! -f "$SWAPPED" ]; then
        SWAP_VERDICT=none
        say "no swap verdict after 30s; starting the installed build"
    else
        SWAP_VERDICT=swapped
    fi
    rm -f "$PENDING" "$SWAPPED" "$FAILED"
    sync
done

# --------------------------------------------------- give the screen back
card_readahead_restore
restore_firmware_osd
# Everything paused for this session — by this script, by the boot hook, or by
# the app's display guard — runs again, exactly those processes.
resume_paused

# Started from NextUI's Tools list (the JukaHub.pak nextui.go writes sets
# JUKAHUB_HOST; NextUI's launcher exports SHARED_USERDATA_PATH to every pak):
# NextUI's own loop shows its menu the moment this script returns. The stock
# MainUI is still on the device, and the watcher below would start it over
# NextUI's menu: two UIs on one screen.
if [ "${JUKAHUB_HOST:-}" = nextui ] || [ -n "${SHARED_USERDATA_PATH:-}" ]; then
    say "started from NextUI; its menu comes back by itself"
    exit 0
fi

# Who brings MainUI back. On stock and CrossMix firmware MainUI started this
# script (through /tmp/cmd_to_run.sh) and exited, and the firmware supervisor,
# runtrimui.sh, is waiting for this script: it starts MainUI again the moment
# the script returns. (Started by the boot hook instead, the supervisor's own
# MainUI was the one stopped above, and resuming it lets it start a new one.)
# This script used to start a MainUI of its own two seconds before returning —
# so the supervisor's made two, both drawing to /dev/fb0: the glitchy,
# flickering screen after leaving JukaHub.
#
# The supervisor always gets the first go. A watcher left behind waits for a
# MainUI to appear: ten seconds, or up to forty while a supervisor that will
# bring one back is still alive (one left paused by anything is let go). Only
# when none has come does it start one (a firmware without a supervisor, a
# start over SSH), checking right before it does, and for a minute after that
# it closes its own again if the firmware's appears after all: never a black
# screen, never two launchers. Last, it notes in errors.txt anything other
# than MainUI that still holds the display — a process JukaHub left behind
# would draw over the menu, which reads as a glitching menu.
KEEPERS="$ANCESTOR_LOOPS $RESUMED_PIDS"
(
    # The nohup of the old code: a hang-up as the session ends must not take
    # the watcher (or the MainUI it starts) with it.
    trap '' HUP
    n=0
    OWN_UI=""
    while :; do
        sleep 1
        n=$((n + 1))
        UI_NOW="$(ui_pid)"
        if [ -n "$UI_NOW" ]; then
            UI_FROM=""
            for P in $UI_NOW; do stat_of "$P" && UI_FROM="$UI_FROM $(describe "$ST_PPID")"; done
            say "MainUI is back after ${n}s (pid $UI_NOW, started by${UI_FROM:- ?})"
            break
        fi
        ALIVE=""
        for K in $KEEPERS; do
            stat_of "$K" || continue
            case "$ST_STATE" in
                Z) continue ;;
                T)
                    kill -CONT "$K" 2>/dev/null
                    say "the firmware launcher was still paused; let it run: $(describe "$K")"
                    ;;
            esac
            ALIVE=1
        done
        [ "$n" -lt 10 ] && continue
        [ -n "$ALIVE" ] && [ "$n" -lt 40 ] && continue
        for UI in \
            /usr/trimui/bin/MainUI \
            /usr/bin/MainUI \
            /usr/local/bin/MainUI \
            /mnt/SDCARD/System/usr/trimui/bin/MainUI
        do
            [ -x "$UI" ] || continue
            [ -n "$(ui_pid)" ] && break
            say "no launcher came back after ${n}s; starting $UI"
            (cd "$(dirname "$UI")" 2>/dev/null && exec "$UI") >/dev/null 2>&1 &
            OWN_UI=$!
            break
        done
        [ -n "$OWN_UI" ] || [ -n "$(ui_pid)" ] || say "no launcher came back and none was found to start"
        break
    done
    # Never two menus: the firmware's own MainUI wins over the one started here.
    if [ -n "$OWN_UI" ]; then
        n=0
        while [ "$n" -lt 60 ] && kill -0 "$OWN_UI" 2>/dev/null; do
            sleep 1
            n=$((n + 1))
            for P in $(ui_pid); do
                [ "$P" = "$OWN_UI" ] && continue
                kill -9 "$OWN_UI" 2>/dev/null
                say "the firmware started its own MainUI after all (pid $P); closed the one started here"
                break 2
            done
        done
    fi
    # What still holds the display a few seconds after the menu is back.
    sleep 3
    UI_NOW=" $(ui_pid) "
    UI_COUNT=0
    for P in $UI_NOW; do UI_COUNT=$((UI_COUNT + 1)); done
    [ "$UI_COUNT" -gt 1 ] && say "display: $UI_COUNT MainUIs are running (pid$UI_NOW)"
    for P in $(display_holders); do
        case "$UI_NOW" in *" $P "*) continue ;; esac
        [ "$P" = "$$" ] && continue
        stat_of "$P" || continue
        [ "$ST_STATE" = Z ] && continue
        # The stock OSD service keeps fb0 open for volume/brightness overlays;
        # it is present beside MainUI too, so it is not a stray renderer.
        DS_COMM=""
        read -r DS_COMM 2>/dev/null <"/proc/$P/comm" || :
        if [ "$DS_COMM" = trimui_osdd ] &&
            [ "$(readlink "/proc/$P/exe" 2>/dev/null)" = /usr/trimui/osd/trimui_osdd ]; then
            continue
        fi
        say "display: still held after JukaHub exited by $(describe "$P"), parent $(describe "$ST_PPID")"
    done
) </dev/null >/dev/null 2>&1 &

exit 0
