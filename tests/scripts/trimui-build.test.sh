#!/usr/bin/env bash
# The TrimUI build script (.github/scripts/trimui-build.sh) must start the
# toolchain container without a network, with the checkout, Go and both caches
# mounted at their own paths, pass the cross-compile settings in, report the
# build's real exit status, and refuse a binary that could not start on the
# handhelds.
#
# A fake docker runs the "container" side of the script right here, against a
# fake go and gcc, inside a throwaway copy of the repository layout; check is
# fed small ELF files built with the host gcc and patched to look like
# AArch64 programs, which is all readelf looks at.
#
#   usage: tests/scripts/trimui-build.test.sh
#
# No network, no Docker. Skipped where gcc, readelf or python3 is missing (a
# stock Git Bash), but never on CI: a check that quietly stops running is worse
# than a red build.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

for tool in gcc readelf python3; do
  if ! command -v "$tool" > /dev/null 2>&1; then
    if [ "${GITHUB_ACTIONS:-}" = true ]; then
      echo "FAIL: trimui-build: $tool is not installed on this runner" >&2
      exit 1
    fi
    echo "trimui-build: skipped ($tool is not installed)"
    exit 0
  fi
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

image="anibaldeboni/trimui-smart-pro-toolchain:latest@sha256:$(printf '%064d' 7)"

# --- a copy of the repository layout the script finds from its own path ------
repo="$work/repo"
mkdir -p "$repo/.github/scripts" "$repo/player/spatialfx"
cp "$root/.github/scripts/trimui-build.sh" "$repo/.github/scripts/"
script="$repo/.github/scripts/trimui-build.sh"

# --- ELF files for check -----------------------------------------------------
mkdir -p "$work/elf/lib"
printf 'int SDL_Init(unsigned f) { return (int)f; }\n' > "$work/elf/sdl.c"
gcc -shared -fPIC -Wl,-soname,libSDL2-2.0.so.0 -o "$work/elf/lib/libSDL2-2.0.so.0" "$work/elf/sdl.c"
ln -s libSDL2-2.0.so.0 "$work/elf/lib/libSDL2.so"
cat > "$work/elf/main.c" << 'EOF'
#include <stdio.h>
#include <sys/stat.h>
int SDL_Init(unsigned);
int main(int argc, char **argv) {
  struct stat st;
  (void)argc;
  stat(argv[0], &st);
  printf("%d\n", SDL_Init(0));
  return 0;
}
EOF
aarch64_loader=-Wl,--dynamic-linker=/lib/ld-linux-aarch64.so.1
gcc -o "$work/elf/good" "$work/elf/main.c" -L"$work/elf/lib" -lSDL2 "$aarch64_loader"
cp "$work/elf/good" "$work/elf/x86"
gcc -o "$work/elf/loader" "$work/elf/main.c" -L"$work/elf/lib" -lSDL2
sed 's/SDL_Init(0)/0/; /int SDL_Init/d' "$work/elf/main.c" > "$work/elf/nosdl.c"
gcc -o "$work/elf/nosdl" "$work/elf/nosdl.c" "$aarch64_loader"

# Machine AArch64 (183) in the ELF header, and every glibc version newer than
# 2.33 renamed to 2.17 (same length, so nothing moves): what a binary linked
# against the handheld's sysroot looks like to readelf.
as_aarch64() {
  python3 - "$1" << 'EOF'
import re, sys
path = sys.argv[1]
data = bytearray(open(path, "rb").read())
data[18:20] = (183).to_bytes(2, "little")
data = re.sub(rb"GLIBC_2\.(3[4-9]|[4-9][0-9])", b"GLIBC_2.17", bytes(data))
open(path, "wb").write(data)
EOF
}
for f in good loader nosdl; do as_aarch64 "$work/elf/$f"; done

check() { # <scenario> <binary> [env...]
  local scenario=$1 bin=$2
  shift 2
  mkdir -p "$work/$scenario"
  : > "$work/$scenario/summary"
  env "$@" GITHUB_STEP_SUMMARY="$work/$scenario/summary" \
    sh "$script" check "$bin" > "$work/$scenario/log" 2>&1
}

check good "$work/elf/good" || fail "a good binary was refused: $(cat "$work/good/log")"
grep -q 'glibc up to 2\.' "$work/good/log" || fail "check did not report the glibc it found: $(cat "$work/good/log")"
grep -q 'libSDL2-2.0.so.0' "$work/good/summary" || fail "check wrote no summary line: $(cat "$work/good/summary")"
if check newglibc "$work/elf/good" GLIBC_MAX=2.10; then
  fail "a binary needing a newer glibc than the limit passed"
fi
grep -q 'would not start there' "$work/newglibc/log" || fail "the glibc refusal does not say why: $(cat "$work/newglibc/log")"
if check x86 "$work/elf/x86"; then fail "an x86-64 binary passed as a TrimUI one"; fi
grep -q 'not AArch64' "$work/x86/log" || fail "the machine refusal does not say why: $(cat "$work/x86/log")"
if check loader "$work/elf/loader"; then fail "a binary for another loader passed"; fi
grep -q 'ld-linux-aarch64' "$work/loader/log" || fail "the loader refusal does not say why: $(cat "$work/loader/log")"
if check nosdl "$work/elf/nosdl"; then fail "a binary without SDL2 passed"; fi
grep -q 'does not link SDL2' "$work/nosdl/log" || fail "the SDL refusal does not say why: $(cat "$work/nosdl/log")"
if check missing "$work/elf/none"; then fail "check passed without a binary"; fi

# --- fake docker, go, gcc and pkg-config ------------------------------------
mkdir -p "$work/host" "$work/image" "$work/goroot/bin" "$work/gomodcache" "$work/gocache"

# The host's go: names the toolchain and caches, fetches nothing.
cat > "$work/host/go" << 'EOF'
#!/usr/bin/env bash
echo "go $* (in $PWD)" >> "$FAKE_LOG"
case "$1" in
  env)
    case "$2" in
      GOROOT) echo "$FAKE_GOROOT" ;;
      GOMODCACHE) echo "$FAKE_GOMODCACHE" ;;
      GOCACHE) echo "$FAKE_GOCACHE" ;;
    esac ;;
  mod | list) [ "${FAKE_FETCH_FAIL:-0}" = 0 ] ;;
  *) exit 2 ;;
esac
EOF

# docker run: records its arguments, then runs the command with nothing but the
# -e variables, the image's PATH and SYSROOT, in the -w folder. The mounts are
# identity mounts, so here they need no doing: the test checks they are asked for.
cat > "$work/host/docker" << 'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG"
[ "$1" = run ] || { echo "fake docker: only run" >&2; exit 2; }
shift
envs=() workdir=/ entry=
while [ $# -gt 0 ]; do
  case "$1" in
    --rm) shift ;;
    --pull | --network | -v) shift 2 ;;
    --entrypoint) entry=$2; shift 2 ;;
    -e) envs+=("$2"); shift 2 ;;
    -w) workdir=$2; shift 2 ;;
    -*) echo "fake docker: unknown option $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
shift # the image
image_env=(PATH="$FAKE_IMAGE_PATH" FAKE_RECORD="$FAKE_RECORD" FAKE_BUILD_EXIT="${FAKE_BUILD_EXIT:-0}" FAKE_ELF="$FAKE_ELF")
if [ -n "${FAKE_IMAGE_SYSROOT:-}" ]; then image_env+=(SYSROOT="$FAKE_IMAGE_SYSROOT"); fi
cd "$workdir"
exec env -i "${image_env[@]}" "${envs[@]}" "$entry" "$@"
EOF

# The Go mounted from the host, as the container runs it.
cat > "$work/goroot/bin/go" << 'EOF'
#!/bin/sh
case "$1" in
  version) echo "go version go1.27.3 linux/amd64" ;;
  build)
    {
      echo "env GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GOPROXY=$GOPROXY GOTOOLCHAIN=$GOTOOLCHAIN"
      echo "env CC=$CC"
      echo "env CGO_LDFLAGS=$CGO_LDFLAGS"
      echo "env GOROOT=$GOROOT GOCACHE=$GOCACHE GOMODCACHE=$GOMODCACHE HOME=$HOME BUILD_DATE=$BUILD_DATE"
      echo "pwd $PWD"
      echo "args $*"
    } > "$FAKE_RECORD"
    out=
    while [ $# -gt 0 ]; do
      if [ "$1" = -o ]; then out=$2; fi
      shift
    done
    printf '%s\n' runtime github.com/veandco/go-sdl2/sdl jukahub/JukaHub
    if [ "$FAKE_BUILD_EXIT" != 0 ]; then
      echo "./main.go:1:1: undefined: nothing" >&2
      exit "$FAKE_BUILD_EXIT"
    fi
    cp "$FAKE_ELF" "$out"
    echo "cache entry" > "$GOCACHE/entry-from-build" ;;
  *) exit 2 ;;
esac
EOF

cat > "$work/image/aarch64-linux-gnu-gcc" << 'EOF'
#!/bin/sh
echo "aarch64-linux-gnu-gcc (Linaro GCC 7.5-2019.12) 7.5.0"
EOF
cat > "$work/image/pkg-config" << 'EOF'
#!/bin/sh
echo "-I/usr/include/SDL2 -D_REENTRANT -lSDL2"
EOF

# The game-audio plugin's build script, as the copy of the repository has it.
cat > "$repo/player/spatialfx/build.sh" << 'EOF'
#!/bin/sh
set -eu
echo "spatial CC=$CC CROSS=$CROSS OUT=$OUT" > "$FAKE_RECORD"
mkdir -p "$OUT"
echo plugin > "$OUT/jukahub-spatial.so"
EOF
chmod +x "$work/host/go" "$work/host/docker" "$work/goroot/bin/go" "$work/image/aarch64-linux-gnu-gcc" "$work/image/pkg-config"

run_script() { # <scenario> <args...>
  local scenario=$1
  shift
  mkdir -p "$work/$scenario"
  : > "$work/$scenario/docker.log"
  : > "$work/$scenario/go.log"
  : > "$work/$scenario/record"
  PATH="$work/host:$PATH" \
    FAKE_LOG="$work/$scenario/go.log" \
    FAKE_DOCKER_LOG="$work/$scenario/docker.log" \
    FAKE_RECORD="$work/$scenario/record" \
    FAKE_IMAGE_PATH="$work/image:/usr/bin:/bin" \
    FAKE_GOROOT="$work/goroot" FAKE_GOMODCACHE="$work/gomodcache" FAKE_GOCACHE="$work/gocache" \
    FAKE_ELF="$work/elf/good" \
    TOOLCHAIN_IMAGE="${TOOLCHAIN_IMAGE-$image}" \
    BUILD_DATE=2026-11-10 \
    sh "$script" "$@" > "$work/$scenario/out" 2>&1
}

# 1. A build: no network, identity mounts, the cross-compile settings, the
#    log, and the binary handed back.
s="$work/build"
run_script build go "$repo/JukaHub-Trimui" "$s/go-build.log" || fail "the build failed: $(cat "$s/out")"
[ -f "$repo/JukaHub-Trimui/JukaHub" ] || fail "no binary in the output folder"
args=$(cat "$s/docker.log")
for want in "--network none" "--pull never" "--entrypoint /bin/sh" \
  "-v $repo:$repo" "-v $work/goroot:$work/goroot:ro" \
  "-v $work/gomodcache:$work/gomodcache:ro" "-v $work/gocache:$work/gocache" \
  "-w $repo/player" "$image $script go $repo/JukaHub-Trimui"; do
  case "$args" in
    *"$want"*) ;;
    *) fail "docker run lacks '$want': $args" ;;
  esac
done
grep -q "^env GOOS=linux GOARCH=arm64 CGO_ENABLED=1 GOPROXY=off GOTOOLCHAIN=local\$" "$s/record" || fail "wrong Go settings in the container: $(cat "$s/record")"
grep -q "^env CC=aarch64-linux-gnu-gcc --sysroot=/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot\$" "$s/record" ||
  fail "without an image SYSROOT the SDK path is not used: $(cat "$s/record")"
grep -q -- "-lSDL2_image -lSDL2_ttf -lSDL2 -ldl -lpthread -lm" "$s/record" || fail "the SDL link flags changed: $(cat "$s/record")"
grep -q "GOROOT=$work/goroot GOCACHE=$work/gocache GOMODCACHE=$work/gomodcache HOME=/tmp/jukahub-home BUILD_DATE=2026-11-10" "$s/record" ||
  fail "the container did not get the host's Go and caches: $(cat "$s/record")"
grep -q "^pwd $repo/player\$" "$s/record" || fail "go build did not run in player/: $(cat "$s/record")"
grep -q -- "-v -buildvcs=false -tags dynamic -ldflags -s -w -X main.buildDate=2026-11-10 -o $repo/JukaHub-Trimui/JukaHub ." "$s/record" ||
  fail "wrong go build arguments: $(cat "$s/record")"
grep -qx 'github.com/veandco/go-sdl2/sdl' "$s/go-build.log" || fail "the go build -v output is not in the log: $(cat "$s/go-build.log")"
grep -q '^trimui-build: SYSROOT:' "$s/go-build.log" || fail "the log does not say which sysroot was used"
if grep -vE '^trimui-build: |^[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*$' "$s/go-build.log" | grep -q .; then
  fail "the log has lines go-build-cache.sh save could mistake for packages: $(cat "$s/go-build.log")"
fi
grep -q "go mod download (in $repo/player)" "$s/go.log" || fail "the modules were not fetched on the host first: $(cat "$s/go.log")"
grep -q "go list -deps . (in $repo/player)" "$s/go.log" || fail "the arm64 package graph was not resolved on the host: $(cat "$s/go.log")"

# 2. The image's own SYSROOT wins over the default.
s="$work/sysroot"
FAKE_IMAGE_SYSROOT=/opt/sdk/sysroot run_script sysroot go "$repo/JukaHub-Trimui" "$s/go-build.log" || fail "the build failed: $(cat "$s/out")"
grep -q "^env CC=aarch64-linux-gnu-gcc --sysroot=/opt/sdk/sysroot\$" "$s/record" || fail "the image's SYSROOT was not used: $(cat "$s/record")"

# 3. A failed compile fails the script with the compiler's status, and the
#    error reaches the log.
s="$work/broken"
rm -f "$repo/JukaHub-Trimui/JukaHub"
rc=0
FAKE_BUILD_EXIT=2 run_script broken go "$repo/JukaHub-Trimui" "$s/go-build.log" || rc=$?
[ "$rc" = 2 ] || fail "a failed build exited $rc, not the build's 2: $(cat "$s/out")"
grep -q 'undefined: nothing' "$s/go-build.log" || fail "the compiler error is not in the log"
[ ! -e "$repo/JukaHub-Trimui/JukaHub" ] || fail "a failed build left a binary"

# 4. Modules that will not download stop the build before the container.
s="$work/nofetch"
if FAKE_FETCH_FAIL=1 run_script nofetch go "$repo/JukaHub-Trimui" "$s/go-build.log"; then
  fail "the build went on without its modules"
fi
[ ! -s "$s/docker.log" ] || fail "the container started although the modules did not download"

# 5. The game-audio plugin, with the same compiler line, into lib/.
s="$work/spatial"
run_script spatial spatial "$repo/JukaHub-Trimui" || fail "the plugin build failed: $(cat "$s/out")"
[ -f "$repo/JukaHub-Trimui/lib/jukahub-spatial.so" ] || fail "no plugin in lib/"
grep -q "^spatial CC=aarch64-linux-gnu-gcc --sysroot=/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot CROSS=aarch64-linux-gnu- OUT=$repo/JukaHub-Trimui/lib\$" "$s/record" ||
  fail "the plugin was built with the wrong settings: $(cat "$s/record")"
case "$(cat "$s/docker.log")" in
  *"--network none"*"-v $repo:$repo"*"-w $repo "*) ;;
  *) fail "the plugin container is not started the same way: $(cat "$s/docker.log")" ;;
esac

# 6. Refusals: an image by tag, an output folder outside the checkout, and
#    usage errors.
if TOOLCHAIN_IMAGE=anibaldeboni/trimui-smart-pro-toolchain:latest run_script tag go "$repo/JukaHub-Trimui" "$work/tag/log"; then
  fail "an image by tag was accepted"
fi
[ ! -s "$work/tag/docker.log" ] || fail "docker ran for an image by tag"
if run_script outside go "$work/elsewhere" "$work/outside/log"; then
  fail "an output folder outside the checkout was accepted"
fi
grep -q 'inside the checkout' "$work/outside/out" || fail "the outside-folder refusal does not say why: $(cat "$work/outside/out")"
for bad in "go $repo/x" "spatial" "check" "build $repo/x" ""; do
  # shellcheck disable=SC2086 # word splitting is the point
  if sh "$script" $bad > /dev/null 2>&1; then fail "'trimui-build.sh $bad' did not fail"; fi
done

echo "trimui-build: ok"
