#!/bin/sh
# Check the pinned offline text model and make its optional install package.
# Usage: sh native/package-llm.sh SOURCE_DIR OUTPUT_ZIP
set -eu

src=${1:?source directory required}
out=${2:?output zip required}
model=SmolLM2-360M-Instruct-Q4_0.gguf
model_sha=c3608933eb6e5763b87f769bda40c204dc158333668c7af214644fe39da58627
model_size=229733280

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
