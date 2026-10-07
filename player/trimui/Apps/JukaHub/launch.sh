#!/bin/sh
# ---------------------------------------------------------------------------
#  JukaHub launcher — TrimUI Smart Pro (stock OS / CrossMix)
#
#  Dipakai oleh launcher bawaan perangkat:
#     /mnt/SDCARD/Apps/JukaHub/config.json  ->  "launch": "launch.sh"
#
#  Tugas script ini:
#    1. ambil alih layar dari launcher bawaan (MainUI)
#    2. set environment (library SDL2, PATH tool, HOME)
#    3. jalankan binary JukaHub
#    4. kembalikan layar ke launcher saat aplikasi ditutup
# ---------------------------------------------------------------------------

APP_DIR=/mnt/SDCARD/Apps/JukaHub

# Folder app bisa diganti namanya; fallback ke lokasi script ini.
[ -d "$APP_DIR" ] || APP_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$APP_DIR" || exit 1

# --------------------------------------------------------------- log helper
LOG="$APP_DIR/errors.txt"
say() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >>"$LOG"
}

say "=== JukaHub launched from $APP_DIR ==="

# Flush card writes first: FAT32 is slow and SDL reads the assets right away.
sync

# --------------------------------------------------- take over the display
# Normally the launcher has already stepped aside before this script runs.
# If it still holds the framebuffer, pause the supervisor and stop the UI
# (SIGTERM first, SIGKILL only if it refuses to go).
if command -v pgrep >/dev/null 2>&1; then
    pkill -STOP runtrimui.sh 2>/dev/null
    pkill -STOP trimui_launcher 2>/dev/null
fi

UI_PID="$(pidof MainUI 2>/dev/null)"
if [ -n "$UI_PID" ]; then
    say "stopping stock launcher (pid $UI_PID)"
    kill $UI_PID 2>/dev/null
    sleep 1
    UI_PID="$(pidof MainUI 2>/dev/null)"
    [ -n "$UI_PID" ] && kill -9 $UI_PID 2>/dev/null
fi

# A short settle so the display is free before SDL grabs it.
sleep 1

# ------------------------------------------------------------- environment
# SDL2 2.26.1 + SDL2_image + SDL2_ttf already live in the device rootfs
# (/usr/lib).  If the user dropped copies into ./libs they win.
LD_LIBRARY_PATH="$APP_DIR:$APP_DIR/libs:/usr/trimui/lib:/usr/lib:/lib:$LD_LIBRARY_PATH"
PATH="$APP_DIR:$APP_DIR/required:$PATH"
HOME="$APP_DIR"
export LD_LIBRARY_PATH PATH HOME

# TrimUI/Allwinner firmwares ship OpenSSL 1.1
CLR_OPENSSL_VERSION_OVERRIDE=1.1
export CLR_OPENSSL_VERSION_OVERRIDE

# --------------------------------------------------------------- run it
if [ ! -x "$APP_DIR/JukaHub" ]; then
    say "FATAL: binary $APP_DIR/JukaHub not found or not executable"
    chmod +x "$APP_DIR/JukaHub" 2>/dev/null
fi

"$APP_DIR/JukaHub" >>"$LOG" 2>&1
RC=$?
say "=== JukaHub exited with code $RC ==="
sync

# --------------------------------------------------- give the screen back
if command -v pgrep >/dev/null 2>&1; then
    pkill -CONT runtrimui.sh 2>/dev/null
    pkill -CONT trimui_launcher 2>/dev/null
fi

# If no supervisor brought the launcher back, start it ourselves so the user
# never ends up on a black screen.
sleep 2
if [ -z "$(pidof MainUI 2>/dev/null)" ]; then
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