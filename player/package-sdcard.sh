#!/usr/bin/env bash
# ---------------------------------------------------------------------------
#  JukaHub → TrimUI Smart Pro SD-card package
#
#  Produces a copy-and-go folder:
#      dist/JukaHub-SDCARD/Apps/JukaHub/...
#  and one zip file:
#      dist/JukaHub-SDCARD.zip
#
#  Usage (inside the TrimUI toolchain container / WSL / Linux):
#      ./package-sdcard.sh
#
#  Environment:
#      SYSROOT        TrimUI SDK sysroot (default: the linaro toolchain)
#      WITH_YTDLP     1 = download the static ARM64 yt-dlp   (default 1)
#      WITH_FFMPEG    1 = download ffmpeg/ffprobe/ffplay     (default 1)
#      FFMPEG_BUILD   BtbN asset name                       (default rolling master)
# ---------------------------------------------------------------------------
set -euo pipefail

cd "$(dirname "$0")"

APP_NAME="JukaHub"
APP_DIR_NAME="JukaHub"
SYSROOT="${SYSROOT:-/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot}"
WITH_YTDLP="${WITH_YTDLP:-1}"
WITH_FFMPEG="${WITH_FFMPEG:-1}"
FFMPEG_BUILD="${FFMPEG_BUILD:-ffmpeg-master-latest-linuxarm64-gpl}"
YTDLP_URL="${YTDLP_URL:-https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux_aarch64}"
FFMPEG_URL="${FFMPEG_URL:-https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/${FFMPEG_BUILD}.tar.xz}"

OUT="dist"
STAGE="$OUT/${APP_NAME}-SDCARD"
PKG="$STAGE/Apps/$APP_DIR_NAME"
REQ="$PKG/required"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

# The toolchain image ships wget but not always curl.
fetch() {
    url="$1"; dest="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 3 -o "$dest" "$url"
    else
        wget -q -O "$dest" "$url"
    fi
}

rm -rf "$STAGE"
mkdir -p "$PKG" "$REQ"

# ------------------------------------------------------------------ 1. build
say "Building $APP_NAME for linux/arm64 (CGO + SDL2)"
export SYSROOT
export CC="aarch64-linux-gnu-gcc --sysroot=$SYSROOT"
export CXX="aarch64-linux-gnu-g++ --sysroot=$SYSROOT"
export CGO_CFLAGS="-I$SYSROOT/usr/include -I/usr/aarch64-linux-gnu/include -I/usr/aarch64-linux-gnu/include/SDL2 -I/usr/include/SDL2 -D_REENTRANT"
export CGO_LDFLAGS="-L$SYSROOT/usr/lib -L/usr/lib/aarch64-linux-gnu -lSDL2_image -lSDL2_ttf -lSDL2 -ldl -lpthread -lm"
export GOOS=linux
export GOARCH=arm64
export CGO_ENABLED=1
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

echo "    CC          = $CC"
echo "    SYSROOT     = $SYSROOT"
go version
go build -trimpath -ldflags "-s -w" -o "$PKG/$APP_NAME" .

# Remote Play helpers, when native/build-helpers.sh has produced them. The
# GitHub release uses it; this script does not build them itself.
if [ -x native/bin/chiaki-stream ] && [ -x native/bin/chiaki-regist ]; then
    mkdir -p "$PKG/bin"
    cp native/bin/chiaki-stream native/bin/chiaki-regist "$PKG/bin/"
    chmod +x "$PKG/bin/chiaki-stream" "$PKG/bin/chiaki-regist"
fi
if [ -d native/libs/brick-stock ]; then
    mkdir -p "$PKG/libs/brick-stock"
    cp -a native/libs/brick-stock/. "$PKG/libs/brick-stock/"
fi

# `file` is not installed in every toolchain image; it is only for the log.
if command -v file >/dev/null 2>&1; then
    file "$PKG/$APP_NAME"
else
    echo "    (the 'file' utility is not installed here; skipping the type report)"
fi

# The device rootfs already ships these; make sure we link against the SONAME
# it actually provides (libSDL2-2.0.so.0 ...), not a bare libSDL2.so.
say "Dynamic libraries the binary expects from the device"
readelf -d "$PKG/$APP_NAME" | grep NEEDED || true

# --------------------------------------------------------------- 2. app files
say "Staging app files"
cp -f trimui/Apps/$APP_DIR_NAME/config.json "$PKG/config.json"
cp -f trimui/Apps/$APP_DIR_NAME/launch.sh   "$PKG/launch.sh"
cp -f trimui/Apps/$APP_DIR_NAME/icon.png     "$PKG/icon.png"
cp -f trimui/jukaconfig.device.json          "$PKG/jukaconfig.json"
cp -f Inter-Regular.ttf                      "$PKG/Inter-Regular.ttf"
cp -f background.jpg                         "$PKG/background.jpg"
chmod +x "$PKG/launch.sh" "$PKG/$APP_NAME"

# ------------------------------------------------------------------ 3. tools
if [ "$WITH_YTDLP" = "1" ]; then
    say "Downloading yt-dlp (ARM64 static, no Python needed)"
    fetch "$YTDLP_URL" "$REQ/yt-dlp"
    chmod +x "$REQ/yt-dlp"
    # Never probe --version here: this script runs on an x86 build host, so the
    # ARM64 binary cannot execute and a failed probe says nothing about it.
    ls -l "$REQ/yt-dlp"
fi

if [ "$WITH_FFMPEG" = "1" ]; then
    say "Downloading $FFMPEG_BUILD"
    TMP="$(mktemp -d)"
    fetch "$FFMPEG_URL" "$TMP/ffmpeg.tar.xz"
    tar -xJf "$TMP/ffmpeg.tar.xz" -C "$TMP"
    BINDIR="$(find "$TMP" -type d -name bin | head -n1)"
    # The static builds ship with symbols (~130 MB each, 400 MB for all three).
    # Stripping drops that to a fraction; the handhelds only have 1 GB of RAM
    # and a FAT32 card, so the size matters more than being able to debug ffmpeg.
    STRIP="$(command -v aarch64-linux-gnu-strip || true)"
    for tool in ffmpeg ffprobe ffplay; do
        if [ -f "$BINDIR/$tool" ]; then
            cp -f "$BINDIR/$tool" "$REQ/$tool"
            if [ -n "$STRIP" ]; then
                "$STRIP" --strip-unneeded "$REQ/$tool" 2>/dev/null || \
                    echo "    warn: could not strip $tool (shipping unstripped)"
            fi
            chmod +x "$REQ/$tool"
        fi
    done
    rm -rf "$TMP"
    ls -la "$REQ"
fi

[ -f "$REQ/yt-dlp" ] || echo "    (no yt-dlp in package)"
chmod +x "$REQ"/* 2>/dev/null || true

# --------------------------------------------------------------- 4. manifest
VERSION="$(grep -o '"Version"[^,]*' jukaconfig.json | head -n1 | sed 's/.*: *"//; s/"$//')"
cat > "$PKG/VERSION.txt" <<EOF
$APP_NAME $VERSION
built:     $(date -u +%Y-%m-%dT%H:%M:%SZ)
target:    TrimUI Smart Pro (linux/arm64, stock OS)
install:   extract this Apps/JukaHub folder into the root of the SD card
EOF

# ------------------------------------------------------------------ 5. zip
say "Creating archive"
# $OUT is relative to the script directory, so resolve it before cd'ing into
# $STAGE - otherwise zip looks for dist/.../dist/... and fails with I/O error.
OUT_ABS="$(cd "$OUT" && pwd)"
( cd "$STAGE" && zip -qr "$OUT_ABS/${APP_NAME}-SDCARD.zip" Apps )

SIZE="$(du -h "$OUT/${APP_NAME}-SDCARD.zip" | cut -f1)"

{
    echo "$APP_NAME $VERSION  —  TrimUI Smart Pro (linux/arm64)"
    echo "archive: $OUT/${APP_NAME}-SDCARD.zip ($SIZE)"
    echo
    echo "linked libraries:"
    readelf -d "$PKG/$APP_NAME" | grep NEEDED || true
    echo
    echo "contents:"
    find "$STAGE" -maxdepth 4 -type f -printf '%10s  %P\n' | sort -k2
} > "$OUT/build-summary.txt"

say "Done"
echo
echo "  archive : $OUT/${APP_NAME}-SDCARD.zip ($SIZE)"
echo "  folder  : $STAGE/Apps/$APP_DIR_NAME"
echo
find "$STAGE" -maxdepth 4 -type f -printf '  %10s  %P\n' | sort -k2
echo
echo "Copy the Apps folder from the archive to the root of your SD card,"
echo "then reboot the device and open the Apps menu."