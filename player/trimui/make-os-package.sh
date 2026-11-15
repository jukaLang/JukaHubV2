#!/usr/bin/env bash
# ---------------------------------------------------------------------------
#  JukaHub OS package for the TrimUI Smart Pro / Brick
#
#  Turns a built JukaHub app (the JukaHub-Trimui folder or JukaHub-Trimui.zip
#  the release builds) into JukaHub-OS-Trimui.zip: an SD-card ROOT that boots
#  straight into JukaHub OS. Same binary, same files; what makes it the OS:
#
#      Apps/JukaHub/...                     the app, unchanged
#      Apps/JukaHub/os/os.json              OS mode on, "edition": "os", shipped layers on
#      Apps/JukaHub/os/layers/README.txt    where layers go
#      Apps/JukaHub/os/layers/speed/        storage + memory tuning (os-layers/ here)
#      System/starts/jukahub-autostart.sh   the boot hook, already installed
#      JukaHub-OS-README.txt                install / go back to stock
#
#  The boot hook is the one the app writes itself for "Start JukaHub at login"
#  (player/autostart.go, trimuiAutostartTemplate), taken from the source so the
#  two can never differ, with the card's fixed paths filled in. Nothing of the
#  firmware is replaced: the stock system stays on the device's own storage,
#  and deleting the hook (or Settings -> JukaHub OS -> Boot into JukaHub OS off)
#  gives the stock menu back. docs/JUKAHUB_OS.md explains the OS session.
#
#  Usage:
#      make-os-package.sh <JukaHub-Trimui folder | JukaHub-Trimui.zip> [out.zip]
#  Environment:
#      SD_ROOT      where the card is mounted on the device (default /mnt/SDCARD)
#      AUTOSTART_GO path to player/autostart.go (default: beside this script)
#      KEEP_STAGE   1 = leave the staged card folder next to the zip
#      OS_LAYERS    folder of layers to ship and switch on (default os-layers/)
# ---------------------------------------------------------------------------
set -euo pipefail

die() { echo "make-os-package: $*" >&2; exit 1; }

[ $# -ge 1 ] || die "usage: $0 <JukaHub-Trimui folder | JukaHub-Trimui.zip> [out.zip]"
SRC="$1"
OUT="${2:-JukaHub-OS-Trimui.zip}"
SD_ROOT="${SD_ROOT:-/mnt/SDCARD}"
HERE="$(cd "$(dirname "$0")" && pwd)"
AUTOSTART_GO="${AUTOSTART_GO:-$HERE/../autostart.go}"
APP_REL="Apps/JukaHub"
APP_ON_CARD="$SD_ROOT/$APP_REL"

[ -f "$AUTOSTART_GO" ] || die "cannot find autostart.go at $AUTOSTART_GO (set AUTOSTART_GO)"
case "$SD_ROOT" in
    /*) ;;
    *) die "SD_ROOT must be an absolute path on the device" ;;
esac
case "$SD_ROOT" in
    *"'"*|*[[:space:]]*) die "SD_ROOT must not contain quotes or spaces" ;;
esac

WORK="$(mktemp -d "${TMPDIR:-/tmp}/jukahub-os.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

# ------------------------------------------------------------ 1. the app
# A zip is unpacked first. The app folder is the one holding both the JukaHub
# binary and launch.sh: JukaHub-Trimui.zip keeps them in a JukaHub-Trimui/
# folder, an SD-card package in Apps/JukaHub/, a hand-made zip at its root.
if [ -f "$SRC" ]; then
    command -v unzip >/dev/null 2>&1 || die "unzip is needed to read $SRC"
    unzip -q "$SRC" -d "$WORK/in"
    SRC="$WORK/in"
fi
[ -d "$SRC" ] || die "$SRC is neither a folder nor a zip"
APP_SRC=""
for cand in "$SRC" "$SRC"/* "$SRC"/*/* "$SRC"/*/*/*; do
    if [ -d "$cand" ] && [ -f "$cand/JukaHub" ] && [ -f "$cand/launch.sh" ]; then
        APP_SRC="$cand"
        break
    fi
done
[ -n "$APP_SRC" ] || die "no folder with both JukaHub and launch.sh under $1"
grep -q -- '--os-session' "$APP_SRC/launch.sh" ||
    die "$APP_SRC/launch.sh predates JukaHub OS (no --os-session); build a newer JukaHub"

STAGE="$WORK/card"
APP="$STAGE/$APP_REL"
mkdir -p "$APP"
cp -a "$APP_SRC/." "$APP/"
chmod 0755 "$APP/JukaHub" "$APP/launch.sh" 2>/dev/null || :

# ---------------------------------------------------------- 2. OS mode on
# The exact form jukaosEncodeState writes: launch.sh greps `"enabled": true`.
# A user's own os.json (a card refreshed with a newer package) is kept.
# The layers the OS package ships (os-layers/ beside this script, e.g. "speed",
# the storage and memory tuning) are copied in and switched on.
mkdir -p "$APP/os/layers"
LAYERS_SRC="${OS_LAYERS:-$HERE/os-layers}"
ON=""
if [ -d "$LAYERS_SRC" ]; then
    for l in "$LAYERS_SRC"/*/; do
        [ -f "$l/layer.json" ] || continue
        id="$(basename "$l")"
        case "$id" in
            enabled|.*|*[!A-Za-z0-9._-]*) die "os-layers/$id: not a usable layer id" ;;
        esac
        rm -rf "$APP/os/layers/$id"
        cp -a "$l" "$APP/os/layers/$id"
        ON="$ON${ON:+,}
    \"$id\": true"
    done
fi
if [ ! -f "$APP/os/os.json" ]; then
    if [ -n "$ON" ]; then
        printf '{\n  "enabled": true,\n  "layers": {%s\n  },\n  "edition": "os"\n}\n' "$ON" >"$APP/os/os.json"
    else
        printf '{\n  "enabled": true,\n  "edition": "os"\n}\n' >"$APP/os/os.json"
    fi
fi
cat >"$APP/os/layers/README.txt" <<'EOF'
JukaHub OS layers
=================

Each layer is a folder here with a layer.json inside, for example

    os/layers/newer-sdl/layer.json
    os/layers/newer-sdl/lib/libSDL2-2.0.so.0

    {
      "name": "Newer SDL2",
      "arch": "aarch64",
      "libs": ["lib"],
      "children": true
    }

Switch layers on in Settings -> JukaHub OS, then Restart JukaHub OS. Layers
only change what JukaHub and its games see while JukaHub runs; nothing on
the device's own storage is written, and a reboot is always the stock system.
The full format (file replacements, drivers, system values) is in
docs/JUKAHUB_OS.md.
EOF

# ---------------------------------------------------------- 3. boot hook
# trimuiAutostartTemplate is a Go raw string: from the line after
# "const trimuiAutostartTemplate = `" (whose rest is the first line) to the
# line holding only "`". Filled in as trimuiAutostartScript does: each path
# single-quoted.
HOOK_DIR="$STAGE/System/starts"
HOOK="$HOOK_DIR/jukahub-autostart.sh"
mkdir -p "$HOOK_DIR"
awk '
    !on && index($0, "const trimuiAutostartTemplate = `") == 1 {
        on = 1
        sub(/^const trimuiAutostartTemplate = `/, "")
        print
        next
    }
    on && $0 == "`" { done = 1; exit }
    on { print }
    END { if (!done) exit 3 }
' "$AUTOSTART_GO" >"$WORK/hook.tmpl" || die "could not find trimuiAutostartTemplate in $AUTOSTART_GO"
[ -s "$WORK/hook.tmpl" ] || die "the boot hook template came out empty"
for p in @APP_DIR@ @LAUNCH@ @PENDING@; do
    grep -q "$p" "$WORK/hook.tmpl" || die "the boot hook template has no $p: update this script with autostart.go"
done
sed -e "s|@APP_DIR@|'$APP_ON_CARD'|g" \
    -e "s|@LAUNCH@|'$APP_ON_CARD/launch.sh'|g" \
    -e "s|@PENDING@|'$APP_ON_CARD/.autostart-pending'|g" \
    "$WORK/hook.tmpl" >"$HOOK"
if grep -q '@[A-Z_][A-Z_]*@' "$HOOK"; then
    die "the boot hook still has a placeholder: $(grep -o '@[A-Z_][A-Z_]*@' "$HOOK" | sort -u | tr '\n' ' ')"
fi
sh -n "$HOOK" || die "the generated boot hook does not parse"
chmod 0755 "$HOOK"

# ------------------------------------------------------------- 4. readme
cat >"$STAGE/JukaHub-OS-README.txt" <<EOF
JukaHub OS for the TrimUI Smart Pro and Brick
=============================================

Install
  1. Turn the handheld off and put its SD card in a computer.
  2. Extract EVERYTHING in this zip to the ROOT of the card, merging the
     Apps and System folders with the ones already there.
  3. Put the card back and power on: the handheld starts into JukaHub OS.

The stock system is not replaced. JukaHub OS runs from the card and makes
its changes (newer libraries, drivers, settings) only while it runs; a
reboot always starts from the untouched stock system.

Go back to the stock menu
  - Once: hold Select (or B) while the JukaHub logo boots.
  - Now: Power -> Return to <firmware> (Settings -> JukaHub OS has it too).
  - For good: Settings -> JukaHub OS -> Boot into JukaHub OS -> off,
    or delete System/starts/jukahub-autostart.sh on the card.
  JukaHub itself stays in Apps -> JukaHub either way.

Paths on the card
  $APP_REL/                  JukaHub (updates itself like the app)
  $APP_REL/os/os.json        OS mode switch
  $APP_REL/os/layers/        layers (see the README there)
  System/starts/jukahub-autostart.sh   the boot hook
EOF

# --------------------------------------------------------------- 5. zip
case "$OUT" in
    /*) OUT_ABS="$OUT" ;;
    *) OUT_ABS="$(pwd)/$OUT" ;;
esac
mkdir -p "$(dirname "$OUT_ABS")"
rm -f "$OUT_ABS"
if command -v zip >/dev/null 2>&1; then
    (cd "$STAGE" && zip -qr -X "$OUT_ABS" .)
elif command -v python3 >/dev/null 2>&1; then
    python3 - "$STAGE" "$OUT_ABS" <<'PY'
import os, sys, zipfile
root, out = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    for d, _, files in os.walk(root):
        for f in sorted(files):
            p = os.path.join(d, f)
            info = zipfile.ZipInfo.from_file(p, os.path.relpath(p, root))
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(p, "rb") as fh:
                z.writestr(info, fh.read())
PY
else
    die "neither zip nor python3 is available to write $OUT"
fi
if [ "${KEEP_STAGE:-0}" = "1" ]; then
    rm -rf "${OUT_ABS%.zip}"
    cp -a "$STAGE" "${OUT_ABS%.zip}"
fi
echo "JukaHub OS package: $OUT_ABS ($(du -h "$OUT_ABS" | cut -f1))"
