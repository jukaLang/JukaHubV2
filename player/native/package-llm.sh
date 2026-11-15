#!/bin/sh
# Check the pinned offline text model and make its optional install package.
# Usage: sh native/package-llm.sh SOURCE_DIR OUTPUT_ZIP
set -eu

src=${1:?source directory required}
out=${2:?output zip required}
# The model's pins live in build-llm.sh beside this script, the one place to
# change them (offline-ai.yml reads them from there too).
pin() {
    sed -n "s/^$1=\([A-Za-z0-9._-]*\)\$/\1/p" "$(dirname "$0")/build-llm.sh"
}
model=$(pin MODEL_NAME)
model_sha=$(pin MODEL_SHA)
model_size=$(pin MODEL_SIZE)
if [ -z "$model" ] || [ -z "$model_sha" ] || [ -z "$model_size" ]; then
    echo "package-llm.sh: could not read the model pins from build-llm.sh" >&2
    exit 1
fi

for name in llama-cli llama-server "$model" BUILDINFO.txt LLAMA_LICENSE.txt MODEL_LICENSE.txt; do
    test -s "$src/$name"
done
test "$(wc -c < "$src/$model")" -eq "$model_size"
printf '%s  %s\n' "$model_sha" "$src/$model" | sha256sum -c - > /dev/null
bin_sha=$(awk '$1 == "binary" && $2 == "sha256" { print $3 }' "$src/BUILDINFO.txt")
test -n "$bin_sha"
printf '%s  %s\n' "$bin_sha" "$src/llama-cli" | sha256sum -c - > /dev/null
server_sha=$(awk '$1 == "server" && $2 == "sha256" { print $3 }' "$src/BUILDINFO.txt")
test -n "$server_sha"
printf '%s  %s\n' "$server_sha" "$src/llama-server" | sha256sum -c - > /dev/null

work=$(mktemp -d /tmp/jukahub-llm-pack.XXXXXXXX)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work/juka-llm"
cp "$src/llama-cli" "$src/llama-server" "$src/$model" "$src/BUILDINFO.txt" \
    "$src/LLAMA_LICENSE.txt" "$src/MODEL_LICENSE.txt" "$work/juka-llm/"
chmod 755 "$work/juka-llm/llama-cli" "$work/juka-llm/llama-server"
(
    cd "$work/juka-llm"
    sha256sum llama-cli llama-server "$model" BUILDINFO.txt LLAMA_LICENSE.txt MODEL_LICENSE.txt > SHA256SUMS
)
out_dir=$(cd "$(dirname "$out")" && pwd)
out="$out_dir/$(basename "$out")"
(
    cd "$work"
    zip -q -r "$out" juka-llm
)
test -s "$out"
echo "Offline AI package ready: $out"
