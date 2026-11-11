#!/bin/sh
# Compile JukaHub for the TrimUI handhelds inside the community toolchain image.
#
# A TrimUI binary has to be linked against what the handheld runs: the device
# SDK sysroot (its glibc), the Linaro aarch64 gcc and SDL2 2.26.1, which the
# image pinned in .github/workflows/docker-image.yml carries. Everything else
# comes from the machine running this script: the checkout, the Go toolchain
# go.mod selects, and Go's module and build caches, mounted into the container
# at the paths they have outside it. Go's cache keys include those paths, so
# the build cache the release job restores (go-build-cache.sh) is the one this
# build reads, and the container needs no network at all.
#
#   trimui-build.sh go <out-dir> <log>   build <out-dir>/JukaHub; the output of
#                                        go build -v also goes to <log>
#   trimui-build.sh spatial <out-dir>    build the game-audio plugin
#                                        (player/spatialfx) into <out-dir>/lib
#   trimui-build.sh check <binary>       check a built binary can start on the
#                                        handhelds (no container)
#
# go and spatial need TOOLCHAIN_IMAGE, the image by digest, and docker; go also
# needs go, check needs readelf. <out-dir> must be inside the checkout, which
# is all the container sees. The container runs as the image's own user (root,
# as the release job always did) and gives what it wrote into the mounted
# folders back to the calling user before it ends.
#
# The release job (docker-image.yml, build-trimui) runs the three in turn. On a
# Linux machine with Docker and Go (WSL 2 works) the same commands build the
# same binary locally, and later builds reuse the local build cache:
#
#   export TOOLCHAIN_IMAGE=<the TOOLCHAIN_IMAGE value in docker-image.yml>
#   sh .github/scripts/trimui-build.sh go JukaHub-Trimui /tmp/trimui-go-build.log
#   sh .github/scripts/trimui-build.sh check JukaHub-Trimui/JukaHub
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
self="$repo/.github/scripts/trimui-build.sh"

# The newest glibc a TrimUI binary may ask for: the Smart Pro has had 2.33
# since firmware 1.0.4 (docs/BUILDING.md), and a binary that needs a newer one
# does not start at all.
GLIBC_MAX=${GLIBC_MAX:-2.33}

say() { printf 'trimui-build: %s\n' "$*"; }
die() {
	printf 'trimui-build: %s\n' "$*" >&2
	exit 1
}
usage() {
	echo "usage: $0 go <out-dir> <log> | spatial <out-dir> | check <binary>" >&2
	exit 2
}

# version_le A B: A <= B for dotted version numbers.
version_le() {
	[ "$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)" = "$2" ]
}

toolchain_image() {
	if ! printf '%s\n' "${TOOLCHAIN_IMAGE:-}" | grep -Eq '^[^[:space:]]+@sha256:[0-9a-f]{64}$'; then
		die "set TOOLCHAIN_IMAGE to the toolchain image by digest (the TOOLCHAIN_IMAGE value in .github/workflows/docker-image.yml)"
	fi
	command -v docker > /dev/null 2>&1 || die "docker is not installed"
	printf '%s\n' "$TOOLCHAIN_IMAGE"
}

# Creates <out-dir> and prints its absolute path, which must lie in the checkout.
output_dir() {
	mkdir -p "$1"
	dir=$(cd "$1" && pwd)
	case "$dir/" in
	"$repo"/*) printf '%s\n' "$dir" ;;
	*) die "the output folder must be inside the checkout ($repo): the container sees nothing else" ;;
	esac
}

host_go() {
	image=$(toolchain_image)
	out=$(output_dir "$1")
	log=$2
	command -v go > /dev/null 2>&1 || die "go is not installed"
	# Asked inside the module, so go names the toolchain go.mod selects
	# (fetching it first if it must), not merely the first go on PATH.
	goroot=$(cd "$repo/player" && go env GOROOT)
	gomodcache=$(cd "$repo/player" && go env GOMODCACHE)
	gocache=$(cd "$repo/player" && go env GOCACHE)
	if [ -z "$goroot" ] || [ -z "$gomodcache" ] || [ -z "$gocache" ]; then
		die "go env did not name GOROOT, GOMODCACHE and GOCACHE"
	fi
	mkdir -p "$gomodcache" "$gocache" "$(dirname "$log")"
	# Every module the linux/arm64 build reads is fetched out here, because the
	# container gets no network: go list resolves the packages go build will.
	if ! (cd "$repo/player" && go mod download &&
		GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go list -deps . > /dev/null); then
		die "could not fetch the Go modules"
	fi
	status_file=$(mktemp)
	{
		rc=0
		docker run --rm --pull never --network none \
			--entrypoint /bin/sh \
			-e JUKAHUB_IN_TOOLCHAIN_IMAGE=1 \
			-e HOME=/tmp/jukahub-home \
			-e GOROOT="$goroot" -e GOMODCACHE="$gomodcache" -e GOCACHE="$gocache" \
			-e GOTOOLCHAIN=local -e GOPROXY=off \
			-e BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%d)}" \
			-e OWNER="$(id -u):$(id -g)" \
			-v "$repo:$repo" \
			-v "$goroot:$goroot:ro" \
			-v "$gomodcache:$gomodcache:ro" \
			-v "$gocache:$gocache" \
			-w "$repo/player" \
			"$image" "$self" go "$out" || rc=$?
		echo "$rc" > "$status_file"
	} 2>&1 | tee "$log"
	rc=$(cat "$status_file")
	rm -f "$status_file"
	return "${rc:-1}"
}

host_spatial() {
	image=$(toolchain_image)
	out=$(output_dir "$1")
	docker run --rm --pull never --network none \
		--entrypoint /bin/sh \
		-e JUKAHUB_IN_TOOLCHAIN_IMAGE=1 \
		-e HOME=/tmp/jukahub-home \
		-e OWNER="$(id -u):$(id -g)" \
		-v "$repo:$repo" \
		-w "$repo" \
		"$image" "$self" spatial "$out"
}

# What a TrimUI binary must be to start on the handheld: an AArch64 program for
# the standard glibc loader, linking SDL2 from the firmware, and asking for no
# glibc newer than the firmware's. A wrong compiler or sysroot fails here
# instead of on someone's handheld.
check() {
	bin=$1
	[ -f "$bin" ] || die "there is no binary at $bin"
	command -v readelf > /dev/null 2>&1 || die "readelf is not installed (binutils)"
	machine=$(readelf -h "$bin" 2> /dev/null | sed -n 's/^[[:space:]]*Machine:[[:space:]]*//p')
	case "$machine" in
	*AArch64*) ;;
	*) die "$bin is built for '${machine:-something that is not ELF}', not AArch64" ;;
	esac
	interp=$(readelf -l "$bin" | sed -n 's/.*Requesting program interpreter: \(.*\)\]$/\1/p')
	if [ "$interp" != /lib/ld-linux-aarch64.so.1 ]; then
		die "$bin asks for the loader '${interp:-none}', not /lib/ld-linux-aarch64.so.1"
	fi
	needed=$(readelf -d "$bin" | sed -n 's/.*(NEEDED).*\[\(.*\)\]$/\1/p' | tr '\n' ' ' | sed 's/ $//')
	case "$needed" in
	*libSDL2*) ;;
	*) die "$bin does not link SDL2 from the firmware (it needs: ${needed:-nothing})" ;;
	esac
	max=$(readelf -V "$bin" | grep -o 'GLIBC_[0-9][0-9.]*' | sed 's/^GLIBC_//' |
		sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)
	[ -n "$max" ] || die "$bin names no glibc symbol version; it is not linked against glibc"
	if ! version_le "$max" "$GLIBC_MAX"; then
		die "$bin needs GLIBC_$max, newer than the handhelds' GLIBC_$GLIBC_MAX: it would not start there"
	fi
	size=$(($(wc -c < "$bin") / 1048576))
	say "$bin: AArch64, $interp, glibc up to $max (limit $GLIBC_MAX), $size MiB"
	say "needs: $needed"
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf -- '- binary: %s MiB, glibc up to %s (limit %s), needs %s\n' \
			"$size" "$max" "$GLIBC_MAX" "$needed" >> "$GITHUB_STEP_SUMMARY"
	fi
}

# --- inside the image -------------------------------------------------------

toolchain_env() {
	# The image's own SYSROOT when it sets one, which is what the release build
	# has always used; the SDK's path otherwise, as package-sdcard.sh does.
	SYSROOT=${SYSROOT:-/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot}
	export SYSROOT
	export CC="aarch64-linux-gnu-gcc --sysroot=$SYSROOT"
	export CXX="aarch64-linux-gnu-g++ --sysroot=$SYSROOT"
	say "SYSROOT: $SYSROOT"
	say "CC: $CC ($(command -v aarch64-linux-gnu-gcc || echo 'not on PATH'))"
	say "gcc: $(aarch64-linux-gnu-gcc --version 2> /dev/null | sed -n 1p)"
}

inside_go() {
	out=$1
	export PATH="$GOROOT/bin:$PATH"
	export CGO_ENABLED=1 GOOS=linux GOARCH=arm64
	export CGO_CFLAGS="-I$SYSROOT/usr/include -I/usr/aarch64-linux-gnu/include -I/usr/aarch64-linux-gnu/include/SDL2 -I/usr/include/SDL2 -D_REENTRANT"
	export CGO_LDFLAGS="-L$SYSROOT/usr/lib -L/usr/lib/aarch64-linux-gnu -lSDL2_image -lSDL2_ttf -lSDL2 -ldl -lpthread -lm"
	# Kept from the job this replaces, which made both links before every
	# build: the sysroot's libpthread at the absolute paths its linker script
	# names. --sysroot already resolves those, so a failure here is harmless.
	ln -sf "$SYSROOT/lib/libpthread.so.0" /usr/lib/libpthread.so.0 2> /dev/null || true
	ln -sf "$SYSROOT/usr/lib/libpthread_nonshared.a" /usr/lib/libpthread_nonshared.a 2> /dev/null || true
	say "$(go version)"
	say "pkg-config sdl2: $(pkg-config --cflags --libs sdl2 2>&1 || true)"
	# -v names every package actually compiled (cached ones are not printed),
	# which is how go-build-cache.sh save tells a player-only build from one
	# that recompiled a dependency. -s -w drop the symbol table and DWARF: less
	# for the linker to write and a smaller download, while a panic still
	# prints function names and lines. -buildvcs=false: nothing reads the VCS
	# stamp, and git in the image would refuse a checkout owned by another
	# user. go-sdl2 links SDL dynamically unless built with -tags static;
	# "dynamic" names that default and selects nothing.
	go build -v -buildvcs=false -tags dynamic \
		-ldflags "-s -w -X main.buildDate=$BUILD_DATE" \
		-o "$out/JukaHub" .
}

inside_spatial() {
	out=$1
	CROSS=aarch64-linux-gnu- OUT="$out/lib" sh "$repo/player/spatialfx/build.sh"
}

inside() {
	mode=$1
	out=$2
	mkdir -p "$HOME"
	# OWNER is uid:gid of whoever started the container (host_go, host_spatial).
	case "$mode" in
	go) trap '[ -z "${OWNER:-}" ] || chown -R "$OWNER" "$GOCACHE" "$out/JukaHub" 2> /dev/null || true' EXIT ;;
	spatial) trap '[ -z "${OWNER:-}" ] || chown -R "$OWNER" "$out/lib" 2> /dev/null || true' EXIT ;;
	esac
	toolchain_env
	"inside_$mode" "$out"
}

if [ "${JUKAHUB_IN_TOOLCHAIN_IMAGE:-}" = 1 ]; then
	case "${1:-}:$#" in
	go:2 | spatial:2) inside "$1" "$2" ;;
	*) usage ;;
	esac
	exit 0
fi

case "${1:-}:$#" in
go:3) host_go "$2" "$3" ;;
spatial:2) host_spatial "$2" ;;
check:2) check "$2" ;;
*) usage ;;
esac
