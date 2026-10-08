#!/usr/bin/env bash
# JukaHub release packaging.
# Builds Windows x64 and explicit Linux ARM64 targets for each supported
# TrimUI model. Each device artifact has a matching manifest target; the Linux
# binary is currently built from the shared codebase/toolchain for each model.
# Hardware runtime validation is still required before claiming compatibility.
#
# Produces, in dist/:
#   JukaHub-win-x64.zip
#   JukaHub-{trimui-smart-pro,trimui-smart-pro-s,trimui-brick,trimui-brick-pro}-linux-arm64.tar.gz
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

OUT="dist"
rm -rf "$OUT"
mkdir -p "$OUT"

# --- Windows x64 ---
echo "Building Windows x64..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -trimpath -o "$OUT/JukaHub.exe" .
(cd "$OUT" && zip -q -r "JukaHub-win-x64.zip" JukaHub.exe)
WINDOWS_SHA="$(sha256sum "$OUT/JukaHub-win-x64.zip" | cut -d' ' -f1)"
WINDOWS_SIZE="$(stat -c %s "$OUT/JukaHub-win-x64.zip")"

# --- TrimUI Linux arm64 targets ---
# These identifiers match DeviceModel values and the updater's strict target map.
TRIMUI_DEVICES=(trimui-smart-pro trimui-smart-pro-s trimui-brick trimui-brick-pro)
TARGETS_JSON=""
for device in "${TRIMUI_DEVICES[@]}"; do
    echo "Building TrimUI target ${device} (Linux arm64, CGO + SDL2)..."
    TARGET_DIR="$OUT/$device"
    mkdir -p "$TARGET_DIR"
    GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -trimpath -o "$TARGET_DIR/JukaHub" .
    cp Inter-Regular.ttf background.jpg jukaconfig.json launch.sh config.json JukaPlayer.png "$TARGET_DIR/"

    ASSET="JukaHub-${device}-linux-arm64.tar.gz"
    tar -C "$TARGET_DIR" -czf "$OUT/$ASSET" \
        JukaHub \
        Inter-Regular.ttf \
        background.jpg \
        jukaconfig.json \
        launch.sh \
        config.json \
        JukaPlayer.png
    SHA="$(sha256sum "$OUT/$ASSET" | cut -d' ' -f1)"
    SIZE="$(stat -c %s "$OUT/$ASSET")"
    if [[ -n "$TARGETS_JSON" ]]; then
        TARGETS_JSON+=$',\n'
    fi
    TARGETS_JSON+="    {\"device\": \"${device}\", \"os\": \"linux\", \"arch\": \"arm64\", \"asset\": \"${ASSET}\", \"sha256\": \"${SHA}\", \"size\": ${SIZE}, \"min_firmware\": \"1.0.4\", \"files\": [\"JukaHub\", \"Inter-Regular.ttf\", \"background.jpg\", \"jukaconfig.json\", \"launch.sh\", \"config.json\", \"JukaPlayer.png\"]}"
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
    {"device": "dev-build", "os": "windows", "arch": "amd64", "asset": "JukaHub-win-x64.zip", "sha256": "${WINDOWS_SHA}", "size": ${WINDOWS_SIZE}, "files": ["JukaHub.exe"]}
  ]
}
EOF
(cd "$OUT" && sha256sum manifest.json JukaHub-*-linux-arm64.tar.gz JukaHub-win-x64.zip > SHA256SUMS)

echo
echo "Built release v${VERSION}:"
ls -la "$OUT"
echo
echo "manifest.json:"
cat "$OUT/manifest.json"
echo
echo "SHA256SUMS:"
cat "$OUT/SHA256SUMS"
