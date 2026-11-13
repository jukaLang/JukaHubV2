#!/bin/sh
# Check the pinned offline recognizer and make a standalone repair package.
# Usage: sh native/package-stt.sh SOURCE_DIR OUTPUT_ZIP
set -eu

src=${1:?source directory required}
out=${2:?output zip required}
model_sha=be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21
model_size=77691713

test -f "$src/whisper-cli"
test -f "$src/ggml-tiny.bin"
test -f "$src/BUILDINFO.txt"
test -f "$src/LICENSE.txt"
test "$(wc -c < "$src/ggml-tiny.bin")" -eq "$model_size"
printf '%s  %s\n' "$model_sha" "$src/ggml-tiny.bin" | sha256sum -c - > /dev/null
bin_sha=$(awk '$1 == "binary" && $2 == "sha256" { print $3 }' "$src/BUILDINFO.txt")
test -n "$bin_sha"
printf '%s  %s\n' "$bin_sha" "$src/whisper-cli" | sha256sum -c - > /dev/null

work=$(mktemp -d /tmp/jukahub-stt-pack.XXXXXXXX)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work/juka-whisper"
cp "$src/whisper-cli" "$src/ggml-tiny.bin" "$src/BUILDINFO.txt" "$src/LICENSE.txt" "$work/juka-whisper/"
chmod 755 "$work/juka-whisper/whisper-cli"
(
    cd "$work/juka-whisper"
    sha256sum whisper-cli ggml-tiny.bin BUILDINFO.txt LICENSE.txt > SHA256SUMS
)
out_dir=$(cd "$(dirname "$out")" && pwd)
out="$out_dir/$(basename "$out")"
(
    cd "$work"
    zip -q -r "$out" juka-whisper
)
test -s "$out"
echo "Offline speech package ready: $out"
