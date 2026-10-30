#!/usr/bin/env bash
# Headless boot benchmark for JukaHub.
#
# Builds the player once, runs it with SDL's dummy drivers and
# JUKAHUB_STARTUP_BENCH=1 (exit after the first frame), then checks the median
# against JUKAHUB_BENCH_LIMIT_MS. The bench uses a private startup-state file
# that is deleted before every run, so every sample is a cold boot and a warm
# start can never hide a regression.
#
# Usage:
#   ./startup-bench.sh
#   JUKAHUB_BENCH_RUNS=5 JUKAHUB_BENCH_LIMIT_MS=3000 ./startup-bench.sh
#
# Run it on the device too: the 2 s budget is a device claim, not a CI claim.
# GitHub's runner is slower and the default limit is 5 s there.

set -euo pipefail

cd "$(dirname "$0")"

runs="${JUKAHUB_BENCH_RUNS:-3}"
limit="${JUKAHUB_BENCH_LIMIT_MS:-5000}"
case "$runs" in
  ''|*[!0-9]*) echo "JUKAHUB_BENCH_RUNS must be a number" >&2; exit 2 ;;
esac
case "$limit" in
  ''|*[!0-9]*) echo "JUKAHUB_BENCH_LIMIT_MS must be a number" >&2; exit 2 ;;
esac

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

bin="$work/JukaHub"
if [ "${OS:-}" = "Windows_NT" ]; then
  bin="$bin.exe"
fi

echo "building player..."
go build -o "$bin" .

# Windows resolves SDL from the executable's own folder, and every sample runs
# from a fresh temp folder, so the DLLs have to travel with it: without them a
# run dies at load time and the bench reports "cannot open shared object file".
# build.bat copies them next to the player; the managed SDK keeps them in
# .sdl2/<triplet>/bin.
if [ "${OS:-}" = "Windows_NT" ]; then
  for dll in SDL2.dll SDL2_image.dll SDL2_ttf.dll; do
    for dir in . .sdl2/*/bin; do
      if [ -f "$dir/$dll" ]; then
        cp "$dir/$dll" "$work/"
        break
      fi
    done
  done
fi

# CI has no jukaconfig.json (it is a user file). The shipped device config is
# the same schema and exercises the real home screen, so use it there.
config="jukaconfig.json"
if [ ! -f "$config" ] && [ -f "trimui/jukaconfig.device.json" ]; then
  config="trimui/jukaconfig.device.json"
fi
cp "$config" "$work/jukaconfig.json"
for asset in Inter-Regular.ttf JukaPlayer.png background.jpg; do
  if [ -f "$asset" ]; then cp "$asset" "$work/"; fi
done

export SDL_VIDEODRIVER=dummy
export SDL_AUDIODRIVER=dummy
export JUKAHUB_STARTUP_BENCH=1
export JUKAHUB_STARTUP_STATE="$work/startup-state.json"

values=()
for i in $(seq 1 "$runs"); do
  root="$work/run-$i"
  mkdir -p "$root"
  cp "$work/jukaconfig.json" "$root/"
  for asset in Inter-Regular.ttf JukaPlayer.png background.jpg; do
    if [ -f "$work/$asset" ]; then cp "$work/$asset" "$root/"; fi
  done
  rm -f "$JUKAHUB_STARTUP_STATE"
  log="$work/run-$i.log"
  if ! (cd "$root" && "$bin") >"$log" 2>&1; then
    echo "run $i failed; last lines:" >&2
    tail -20 "$log" >&2
    exit 1
  fi
  line="$(grep -o 'startup: pre-sdl=.*target=[0-9]*ms' "$log" | tail -1 || true)"
  if [ -z "$line" ]; then
    echo "run $i did not report a startup line; last lines:" >&2
    tail -20 "$log" >&2
    exit 1
  fi
  total="$(printf '%s' "$line" | sed -n 's/.*total=\([0-9]*\)ms.*/\1/p')"
  if [ -z "$total" ]; then
    echo "run $i startup line has no total: $line" >&2
    exit 1
  fi
  values+=("$total")
  echo "run $i: ${total}ms"
  echo "         $line"
done

sorted="$(printf '%s\n' "${values[@]}" | sort -n)"
median="$(printf '%s\n' "$sorted" | sed -n "$(( (runs + 1) / 2 ))p")"
echo
echo "cold-boot median: ${median}ms over ${runs} run(s), limit ${limit}ms"

if [ "$median" -gt "$limit" ]; then
  echo "BOOT BUDGET EXCEEDED: ${median}ms > ${limit}ms" >&2
  exit 1
fi
echo "boot budget OK"
