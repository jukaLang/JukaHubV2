#!/bin/sh
# Check the natural offline voice build-voice.sh made and turn it into its
# optional install package, JukaHub-Voice-linux-arm64.zip: the folder
# juka-voice/ with a SHA256SUMS listing every file in it, which JukaHub checks
# before it installs the package (relpack.go, tts_phonon_install.go).
# Usage: sh .github/scripts/package-voice.sh SOURCE_DIR OUTPUT_ZIP
set -eu

src=${1:?source directory required}
out=${2:?output zip required}

test -s "$src/juka-voice.tar"
test -s "$src/BUILDINFO.txt"
tar_sha=$(awk '$1 == "tar" && $2 == "sha256" { print $3 }' "$src/BUILDINFO.txt")
test -n "$tar_sha"
printf '%s  %s\n' "$tar_sha" "$src/juka-voice.tar" | sha256sum -c - > /dev/null

work=$(mktemp -d /tmp/jukahub-voice-pack.XXXXXXXX)
trap 'rm -rf "$work"' EXIT HUP INT TERM
tar -xf "$src/juka-voice.tar" -C "$work"
pkg="$work/juka-voice"
# Only regular files and folders: the zip carries no links or devices.
if find "$pkg" ! -type f ! -type d | grep -q .; then
    find "$pkg" ! -type f ! -type d >&2
    echo "package-voice.sh: the package holds something other than files and folders" >&2
    exit 1
fi
for name in juka-voice-server voice.json MODEL_LICENSE.txt; do
    test -s "$pkg/$name"
done
# The same rules the handheld applies (ttsPhononParseManifest in
# player/tts_phonon_install.go): a package it would refuse is not shipped.
python3 - "$pkg/voice.json" << 'EOF'
import json, re, sys
v = json.load(open(sys.argv[1]))
assert v.get("engine") == "phonon" and v.get("server") == "juka-voice-server", "engine/server"
args = v.get("args", [])
assert isinstance(args, list) and len(args) <= 32, "args"
assert all(isinstance(a, str) and len(a) <= 512 and "\0" not in a for a in args), "args"
flag = v.get("addr_flag", "")
assert flag.startswith("-") and len(flag) <= 32 and not set(flag) & set(" =\0"), "addr_flag"
assert v.get("format") == "pcm" and 8000 <= int(v.get("rate", 0)) <= 48000, "format/rate"
assert 0 <= int(v.get("rss_mb", 0)) <= 900, "rss_mb"
for k in ("model", "female", "male"):
    x = v.get(k, "")
    assert isinstance(x, str) and len(x) <= 64 and not set(x) & set('"\\\0\n'), k
for k, x in (v.get("env") or {}).items():
    assert re.fullmatch(r"[A-Z][A-Z0-9_]{0,47}", k) and not k.startswith("LD_") and k != "PATH", k
    assert isinstance(x, str) and len(x) <= 512 and "\0" not in x, k
EOF
cp "$src/BUILDINFO.txt" "$pkg/BUILDINFO.txt"
chmod 755 "$pkg/juka-voice-server"
(
    cd "$pkg"
    find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort | while read -r f; do
        sha256sum "$f"
    done > SHA256SUMS
)
out_dir=$(cd "$(dirname "$out")" && pwd)
out="$out_dir/$(basename "$out")"
rm -f "$out"
(
    cd "$work"
    zip -q -r "$out" juka-voice
)
test -s "$out"
echo "Natural voice package ready: $out ($(du -h "$out" | cut -f1))"
