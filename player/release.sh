#!/usr/bin/env bash
# JukaHub release packaging.
#
# Builds:
#   - Windows x64 (development / desktop)
#   - TrimUI Smart Pro Linux ARM64 (the device target)
#
# Produces, in dist/:
#   JukaHub-win-x64.zip
#   JukaHub-trimui-smart-pro-linux-arm64.tar.gz
#   manifest.json          (machine-readable update manifest for the Patch module)
#   SHA256SUMS
#
# The version is read from jukaconfig.json ("Version") so the binary, the
# header, the manifest and the release name stay consistent.
#
# Usage: ./release.sh
# Requires: bash, go, tar, zip, sha256sum, and (for the ARM64 build) a Linux
# CGO cross-toolchain with SDL2/SDL_ttf/SDL_image headers for arm64.

set -euo pipefail
cd "$(dirname "$0")"

VERSION="$(python3 -c 'import json;print(json.load(open("jukaconfig.json"))["Version"])')"
VERSION="${VERSION#v}"
echo "Packaging JukaHub v${VERSION}"

OUT="dist"
rm -rf "$OUT"
mkdir -p "$OUT"

# --- Windows x64 ---
echo "Building Windows x64..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -trimpath -o "$OUT/jukahub.exe" .
(cd "$OUT" && zip -q -r "JukaHub-win-x64.zip" jukahub.exe)

# --- TrimUI Smart Pro (Linux arm64, CGO + SDL2) ---
echo "Building TrimUI Linux arm64 (CGO + SDL2)..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -trimpath -o "$OUT/jukahub" .
ASSET="JukaHub-trimui-smart-pro-linux-arm64.tar.gz"
tar -C "$OUT" -czf "$OUT/$ASSET" \
    jukahub \
    Inter-Regular.ttf \
    background.jpg \
    jukaconfig.json \
    launch.sh

# --- Manifest + checksums ---
SHA="$(sha256sum "$OUT/$ASSET" | cut -d' ' -f1)"
SIZE="$(stat -c %s "$OUT/$ASSET")"
cat > "$OUT/manifest.json" <<EOF
{
  "schema": 1,
  "product": "JukaHub",
  "version": "v${VERSION}",
  "channel": "stable",
  "published_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "targets": [
    {
      "device": "trimui-smart-pro",
      "os": "linux",
      "arch": "arm64",
      "asset": "${ASSET}",
      "sha256": "${SHA}",
      "size": ${SIZE},
      "min_firmware": "1.0.4",
      "files": ["jukahub", "Inter-Regular.ttf", "background.jpg", "jukaconfig.json", "launch.sh"]
    }
  ]
}
EOF
(cd "$OUT" && sha256sum manifest.json "$ASSET" "JukaHub-win-x64.zip" > SHA256SUMS)

echo
echo "Built release v${VERSION}:"
ls -la "$OUT"
echo
echo "manifest.json:"
cat "$OUT/manifest.json"
echo
echo "SHA256SUMS:"
cat "$OUT/SHA256SUMS"
