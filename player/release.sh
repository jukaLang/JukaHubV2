#!/usr/bin/env bash
# JukaHub release packaging.
# Builds Windows x64 and explicit Linux ARM64 targets for each supported
# TrimUI model. Each device artifact has a matching manifest target; the Linux
# binary is currently built from the shared codebase/toolchain for each model.
# Hardware runtime validation is still required before claiming compatibility.
#
# Produces, in dist/:
#   JukaHub-win-x64.zip
#   JukaHub-{trimui-smart-pro,trimui-smart-pro-s,trimui-brick,trimui-brick-pro,trimui-hammer}-linux-arm64.tar.gz
#   JukaHub-RemotePlay-linux-arm64.zip   (when the Remote Play helpers were built)
#   JukaHub-AndroidRT-linux-arm64.zip    (when native/bin has a verified AndroidRT package;
#                                         every TrimUI archive then carries its androidrt/)
#   JukaHub-OfflineSpeech-linux-arm64.zip (when the Smart Pro recognizer was built)
#   JukaHub-Wine-linux-arm64.zip         (when native/bin has a verified Box64 pack)
#   manifest.json
#   SHA256SUMS
#
# Usage: ./release.sh
# Requires: bash, go, tar, zip, sha256sum, and (for the ARM64 build) a Linux
# CGO cross-toolchain with SDL2/SDL_ttf/SDL_image headers for arm64.

set -euo pipefail
cd "$(dirname "$0")"

if command -v python3 >/dev/null 2>&1; then
    VERSION="$(python3 -c 'import json;print(json.load(open("jukaconfig.json"))["Version"])')"
else
    VERSION="$(grep -o '"Version"[[:space:]]*:[[:space:]]*"[^"]*"' jukaconfig.json | head -n 1 | sed 's/.*"\([^"]*\)"$/\1/')"
fi
VERSION="${VERSION#v}"
if [ -z "$VERSION" ]; then
    echo "ERROR: could not read Version from jukaconfig.json" >&2
    exit 1
fi
echo "Packaging JukaHub v${VERSION}"

# Stamp the compile time into every binary so an installation can compare its
# own build against a release published later the same day.
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BUILD_DATE="${BUILD_TIME:0:10}"
LDFLAGS="-s -w -X main.buildDate=${BUILD_DATE} -X main.buildTime=${BUILD_TIME}"

OUT="dist"
rm -rf "$OUT"
mkdir -p "$OUT"

# --- Windows x64 ---
echo "Building Windows x64..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/JukaHub.exe" .
(cd "$OUT" && zip -q -r "JukaHub-win-x64.zip" JukaHub.exe)
# Tools -> Security & trust opens SECURITY.md from next to the binary (the app
# falls back to a short built-in summary when it is missing). -j stores it at
# the root of the zip without copying it into dist/.
WINDOWS_FILES_JSON="\"JukaHub.exe\""
if [ -f ../SECURITY.md ]; then
    zip -q -j "$OUT/JukaHub-win-x64.zip" ../SECURITY.md
    WINDOWS_FILES_JSON+=", \"SECURITY.md\""
fi
WINDOWS_SHA="$(sha256sum "$OUT/JukaHub-win-x64.zip" | cut -d' ' -f1)"
WINDOWS_SIZE="$(stat -c %s "$OUT/JukaHub-win-x64.zip")"

# --- Remote Play helpers ---
# chiaki-stream and chiaki-regist are built separately (native/build-helpers.sh,
# in CI the remote-play-helpers workflow) because they need chiaki-ng's sources
# and an arm64 build host or SDK. Both put them in native/bin; bin/ is where
# older checkouts of build-helpers.sh wrote them. They are taken when present
# even without the exec bit, which a CI artifact does not keep, and a release
# without them still runs: the Remote Play panel then reports them missing.
HELPERS_DIR=""
for dir in native/bin bin; do
    if [ -f "$dir/chiaki-stream" ] && [ -f "$dir/chiaki-regist" ]; then
        HELPERS_DIR="$dir"
        break
    fi
done
if [ -n "$HELPERS_DIR" ]; then
    echo "Remote Play helpers: $HELPERS_DIR"
    # The helpers on their own as well: a JukaHub that has no bin/ downloads
    # this package by itself when PS Remote Play opens
    # (psremote_helpers_install.go). Same layout as docker-image.yml's.
    # Optional: a failure here costs the release this package, nothing else.
    HELPER_PKG="$OUT/remote-play-package"
    if ! (
        mkdir -p "$HELPER_PKG/bin" &&
        cp "$HELPERS_DIR/chiaki-stream" "$HELPERS_DIR/chiaki-regist" "$HELPER_PKG/bin/" &&
        { [ ! -f "$HELPERS_DIR/BUILDINFO.txt" ] || cp "$HELPERS_DIR/BUILDINFO.txt" "$HELPER_PKG/bin/"; } &&
        chmod 755 "$HELPER_PKG/bin/chiaki-stream" "$HELPER_PKG/bin/chiaki-regist" &&
        cd "$HELPER_PKG" && sha256sum bin/* > SHA256SUMS &&
        zip -q -r ../JukaHub-RemotePlay-linux-arm64.zip bin SHA256SUMS
    ); then
        rm -f "$OUT/JukaHub-RemotePlay-linux-arm64.zip"
        echo "WARNING: the Remote Play helper package could not be built; the release goes out without it" >&2
    fi
    rm -rf "$HELPER_PKG"
else
    echo "WARNING: no Remote Play helpers in native/bin; the TrimUI archives will ship without Remote Play streaming" >&2
fi

# --- AndroidRT and the Box64 pack (native/build-runtimes.sh) ---
# Built on an aarch64 host (in CI the remote-play-helpers workflow, which hands
# them over in native/bin): JukaHub-AndroidRT-linux-arm64.zip, whose androidrt/
# folder every TrimUI archive carries beside the binary, and
# JukaHub-Wine-linux-arm64.zip, Box64 for the Windows-app runtime, which only
# the release carries. Both are attached on their own as well, and both are
# optional: a package that is missing or does not verify (one folder, every
# file in its SHA256SUMS and matching) is left out with a warning.
ANDROIDRT_ZIP=""
if [ -f native/bin/JukaHub-AndroidRT-linux-arm64.zip ]; then
    if sh native/build-runtimes.sh verify native/bin/JukaHub-AndroidRT-linux-arm64.zip &&
        cp native/bin/JukaHub-AndroidRT-linux-arm64.zip "$OUT/"; then
        ANDROIDRT_ZIP="$OUT/JukaHub-AndroidRT-linux-arm64.zip"
    else
        rm -f "$OUT/JukaHub-AndroidRT-linux-arm64.zip"
        echo "WARNING: the AndroidRT package in native/bin does not verify; the TrimUI archives ship without androidrt/" >&2
    fi
else
    echo "WARNING: no AndroidRT package in native/bin; the TrimUI archives ship without androidrt/" >&2
fi
if [ -f native/bin/JukaHub-Wine-linux-arm64.zip ]; then
    if ! { sh native/build-runtimes.sh verify native/bin/JukaHub-Wine-linux-arm64.zip &&
        cp native/bin/JukaHub-Wine-linux-arm64.zip "$OUT/"; }; then
        rm -f "$OUT/JukaHub-Wine-linux-arm64.zip"
        echo "WARNING: the Box64 pack in native/bin does not verify; the release goes out without it" >&2
    fi
else
    echo "WARNING: no Box64 pack in native/bin; the release goes out without JukaHub-Wine-linux-arm64.zip" >&2
fi

# --- JukaHub Spatial, the game-audio plugin (spatialfx/README.md) ---
# Built once with the aarch64 cross gcc and copied into every TrimUI archive's
# lib/, where spatial_games.go looks for it. build.sh refuses a library that
# needs a glibc newer than 2.17 or anything beyond libc/libm/the loader, so the
# cross gcc of a current distribution is fine. Optional like the helpers:
# without the compiler, or when a check fails, the archives ship without it and
# Tools -> Audio says the game plugin is not installed.
SPATIAL_SO=""
if command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
    if CC=aarch64-linux-gnu-gcc CROSS=aarch64-linux-gnu- OUT="$OUT/spatialfx" sh spatialfx/build.sh; then
        SPATIAL_SO="$OUT/spatialfx/jukahub-spatial.so"
    else
        echo "WARNING: the game audio plugin did not build; the TrimUI archives ship without it" >&2
    fi
else
    echo "WARNING: no aarch64-linux-gnu-gcc; the TrimUI archives ship without the game audio plugin" >&2
fi

# --- TrimUI Linux arm64 targets ---
# These identifiers match DeviceModel values and the updater's strict target map.
TRIMUI_DEVICES=(trimui-smart-pro trimui-smart-pro-s trimui-brick trimui-brick-pro trimui-hammer)
STT_BIN_SHA=$(awk '$1 == "binary" && $2 == "sha256" { print $3 }' native/bin/juka-whisper/BUILDINFO.txt 2>/dev/null || true)
if [ -z "$STT_BIN_SHA" ] || ! sh native/package-stt.sh native/bin/juka-whisper "$OUT/JukaHub-OfflineSpeech-linux-arm64.zip"; then
    echo "ERROR: verified offline speech is required for the Smart Pro release" >&2
    exit 1
fi
TARGETS_JSON=""
for device in "${TRIMUI_DEVICES[@]}"; do
    echo "Building TrimUI target ${device} (Linux arm64, CGO + SDL2)..."
    TARGET_DIR="$OUT/$device"
    mkdir -p "$TARGET_DIR"
    GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -trimpath -ldflags "$LDFLAGS" -o "$TARGET_DIR/JukaHub" .
    cp Inter-Regular.ttf background.jpg jukaconfig.json launch.sh config.json JukaPlayer.png "$TARGET_DIR/"

    PKG_FILES=(JukaHub Inter-Regular.ttf background.jpg jukaconfig.json launch.sh config.json JukaPlayer.png)
    # SECURITY.md sits next to the binary for Tools -> Security & trust's reader.
    if [ -f ../SECURITY.md ]; then
        cp ../SECURITY.md "$TARGET_DIR/SECURITY.md"
        PKG_FILES+=(SECURITY.md)
    fi
    # Remote Play helpers (see HELPERS_DIR above).
    if [ -n "$HELPERS_DIR" ]; then
        mkdir -p "$TARGET_DIR/bin"
        cp "$HELPERS_DIR/chiaki-stream" "$HELPERS_DIR/chiaki-regist" "$TARGET_DIR/bin/"
        chmod 755 "$TARGET_DIR/bin/chiaki-stream" "$TARGET_DIR/bin/chiaki-regist"
        PKG_FILES+=(bin/chiaki-stream bin/chiaki-regist)
    else
        echo "WARNING: ${device} ships without Remote Play streaming (no helpers)" >&2
    fi
    if [ "$device" = trimui-smart-pro ]; then
        mkdir -p "$TARGET_DIR/juka-whisper"
        cp native/bin/juka-whisper/whisper-cli native/bin/juka-whisper/ggml-tiny.bin \
            native/bin/juka-whisper/BUILDINFO.txt native/bin/juka-whisper/LICENSE.txt "$TARGET_DIR/juka-whisper/"
        chmod 755 "$TARGET_DIR/juka-whisper/whisper-cli"
        PKG_FILES+=(juka-whisper)
    fi
    # The game-audio plugin (see SPATIAL_SO above).
    if [ -n "$SPATIAL_SO" ]; then
        mkdir -p "$TARGET_DIR/lib"
        cp "$SPATIAL_SO" "$TARGET_DIR/lib/"
        PKG_FILES+=(lib/jukahub-spatial.so)
    fi
    # AndroidRT beside the binary (see ANDROIDRT_ZIP above).
    if [ -n "$ANDROIDRT_ZIP" ]; then
        if sh native/build-runtimes.sh unpack "$ANDROIDRT_ZIP" "$TARGET_DIR/androidrt" > /dev/null; then
            PKG_FILES+=(androidrt)
        else
            rm -rf "$TARGET_DIR/androidrt"
            echo "WARNING: ${device} ships without androidrt/ (the package did not unpack)" >&2
        fi
    fi
    # The Brick Pro needs the bundled FFmpeg/OpenSSL runtime next to the app.
    if [ -d native/libs/brick-stock ]; then
        mkdir -p "$TARGET_DIR/libs/brick-stock"
        cp -a native/libs/brick-stock/. "$TARGET_DIR/libs/brick-stock/"
        PKG_FILES+=(libs/brick-stock)
    fi

    ASSET="JukaHub-${device}-linux-arm64.tar.gz"
    tar -C "$TARGET_DIR" -czf "$OUT/$ASSET" "${PKG_FILES[@]}"
    SHA="$(sha256sum "$OUT/$ASSET" | cut -d' ' -f1)"
    SIZE="$(stat -c %s "$OUT/$ASSET")"
    FILES_JSON=""
    for entry in "${PKG_FILES[@]}"; do
        if [[ -n "$FILES_JSON" ]]; then
            FILES_JSON+=", "
        fi
        FILES_JSON+="\"${entry}\""
    done
    if [[ -n "$TARGETS_JSON" ]]; then
        TARGETS_JSON+=$',\n'
    fi
    TARGETS_JSON+="    {\"device\": \"${device}\", \"os\": \"linux\", \"arch\": \"arm64\", \"asset\": \"${ASSET}\", \"sha256\": \"${SHA}\", \"size\": ${SIZE}, \"min_firmware\": \"1.0.4\", \"files\": [${FILES_JSON}]}"
done

cat > "$OUT/manifest.json" <<EOF
{
  "schema": 1,
  "product": "JukaHub",
  "version": "v${VERSION}",
  "channel": "stable",
  "published_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "targets": [
${TARGETS_JSON},
    {"device": "dev-build", "os": "windows", "arch": "amd64", "asset": "JukaHub-win-x64.zip", "sha256": "${WINDOWS_SHA}", "size": ${WINDOWS_SIZE}, "files": [${WINDOWS_FILES_JSON}]}
  ]
}
EOF
(cd "$OUT" && sha256sum manifest.json JukaHub-*-linux-arm64.tar.gz JukaHub-win-x64.zip > SHA256SUMS)
for pkg in JukaHub-RemotePlay-linux-arm64.zip JukaHub-AndroidRT-linux-arm64.zip JukaHub-OfflineSpeech-linux-arm64.zip JukaHub-Wine-linux-arm64.zip; do
    if [ -f "$OUT/$pkg" ]; then
        (cd "$OUT" && sha256sum "$pkg" >> SHA256SUMS)
    fi
done

echo
echo "Built release v${VERSION}:"
ls -la "$OUT"
echo
echo "manifest.json:"
cat "$OUT/manifest.json"
echo
echo "SHA256SUMS:"
cat "$OUT/SHA256SUMS"
