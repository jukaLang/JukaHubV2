#!/bin/sh
# Check the pinned offline recognizer and make a standalone repair package.
# Usage: sh native/package-stt.sh SOURCE_DIR OUTPUT_ZIP
set -eu

src=${1:?source directory required}
out=${2:?output zip required}
model_sha=be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21
model_size=77691713
vosk_lib_sha=0e9df29f060a93cf3df3263a4d3635e1b75688a5fd84e86ade1599372e3c9597
vosk_model_sha=30f26242c4eb449f948e42cb302dd7a686cb29a3423a8367f99ff41780942498
vosk_license_sha=bb618af833c817ab01c1564bd3ec75ba4b4f9cf30910a4a2168e30a674ee8cc6

test -f "$src/whisper-cli"
test -f "$src/ggml-tiny.bin"
test -f "$src/BUILDINFO.txt"
test -f "$src/LICENSE.txt"
test -f "$src/vosk-cli"
test -f "$src/libvosk.so"
test -f "$src/vosk-model-small-en-us-0.15.zip"
test -f "$src/VOSK_LICENSE.txt"
test "$(wc -c < "$src/ggml-tiny.bin")" -eq "$model_size"
printf '%s  %s\n' "$model_sha" "$src/ggml-tiny.bin" | sha256sum -c - > /dev/null
bin_sha=$(awk '$1 == "binary" && $2 == "sha256" { print $3 }' "$src/BUILDINFO.txt")
test -n "$bin_sha"
printf '%s  %s\n' "$bin_sha" "$src/whisper-cli" | sha256sum -c - > /dev/null
vosk_bin_sha=$(awk '$1 == "vosk" && $2 == "binary" && $3 == "sha256" { print $4 }' "$src/BUILDINFO.txt")
test -n "$vosk_bin_sha"
printf '%s  %s\n' "$vosk_bin_sha" "$src/vosk-cli" | sha256sum -c - > /dev/null
printf '%s  %s\n' "$vosk_lib_sha" "$src/libvosk.so" | sha256sum -c - > /dev/null
printf '%s  %s\n' "$vosk_model_sha" "$src/vosk-model-small-en-us-0.15.zip" | sha256sum -c - > /dev/null
printf '%s  %s\n' "$vosk_license_sha" "$src/VOSK_LICENSE.txt" | sha256sum -c - > /dev/null

work=$(mktemp -d /tmp/jukahub-stt-pack.XXXXXXXX)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work/juka-whisper"
cp "$src/whisper-cli" "$src/ggml-tiny.bin" "$src/BUILDINFO.txt" "$src/LICENSE.txt" \
    "$src/vosk-cli" "$src/libvosk.so" "$src/VOSK_LICENSE.txt" "$work/juka-whisper/"
unzip -q "$src/vosk-model-small-en-us-0.15.zip" -d "$work/juka-whisper"
test -s "$work/juka-whisper/vosk-model-small-en-us-0.15/am/final.mdl"
chmod 755 "$work/juka-whisper/whisper-cli" "$work/juka-whisper/vosk-cli"
(
    cd "$work/juka-whisper"
    {
        sha256sum whisper-cli ggml-tiny.bin BUILDINFO.txt LICENSE.txt vosk-cli libvosk.so VOSK_LICENSE.txt
        find vosk-model-small-en-us-0.15 -type f -print0 | sort -z | xargs -0 sha256sum
    } > SHA256SUMS
)
out_dir=$(cd "$(dirname "$out")" && pwd)
out="$out_dir/$(basename "$out")"
(
    cd "$work"
    zip -q -r "$out" juka-whisper
)
test -s "$out"
echo "Offline speech package ready: $out"
