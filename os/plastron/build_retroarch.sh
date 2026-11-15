#!/bin/sh
# Build a lean RetroArch for plastron's Brick userspace. Run on Linux after
# plastron has made output-brick/host (its cross compiler and sysroot).
set -eu

OUT=${1:-}
PLASTRON_OUTPUT=${PLASTRON_OUTPUT:-}
[ -n "$OUT" ] && [ -n "$PLASTRON_OUTPUT" ] || {
	echo "usage: PLASTRON_OUTPUT=/path/to/output-brick $0 /new/retroarch-directory" >&2
	exit 2
}
[ ! -e "$OUT" ] || { echo "output exists: $OUT" >&2; exit 1; }
HOST=$PLASTRON_OUTPUT/host
TRIPLE=aarch64-buildroot-linux-gnu
SYSROOT=$HOST/$TRIPLE/sysroot
[ -x "$HOST/bin/$TRIPLE-gcc" ] && [ -d "$SYSROOT" ] || {
	echo "plastron's Brick compiler and sysroot are missing under $HOST" >&2
	exit 1
}
for tool in git make readelf; do
	command -v "$tool" > /dev/null || { echo "$tool is required" >&2; exit 1; }
done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
git clone --quiet --depth 1 --branch v1.21.0 \
	https://github.com/libretro/RetroArch.git "$WORK/RetroArch"
cd "$WORK/RetroArch"
PIN=baee906ef35b99283f9a1a060a2ce5ac86159b63
[ "$(git rev-parse HEAD)" = "$PIN" ] || {
	echo "RetroArch v1.21.0 moved; refusing an unreviewed source" >&2
	exit 1
}

export PATH="$HOST/bin:$PATH"
export CC="$HOST/bin/$TRIPLE-gcc"
export CXX="$HOST/bin/$TRIPLE-g++"
export AR="$HOST/bin/$TRIPLE-ar"
export PKG_CONFIG="$HOST/bin/pkg-config"
export PKG_CONFIG_SYSROOT_DIR="$SYSROOT"
export PKG_CONFIG_LIBDIR="$SYSROOT/usr/lib/pkgconfig:$SYSROOT/usr/share/pkgconfig"

sh ./configure --host="$TRIPLE" \
	--enable-sdl2 --enable-alsa --enable-opengles --enable-egl \
	--disable-x11 --disable-wayland --disable-kms --disable-vulkan \
	--disable-udev --disable-pulse --disable-dbus --disable-ffmpeg
make -j"$(nproc)"
"$HOST/bin/$TRIPLE-strip" retroarch
readelf -h retroarch | grep -q 'Machine:.*AArch64' || {
	echo "RetroArch build is not AArch64" >&2; exit 1;
}
mkdir -p "$OUT"
cp retroarch "$OUT/retroarch"
cp COPYING "$OUT/RetroArch-COPYING.txt"
printf '%s\n' "$PIN" > "$OUT/SOURCE_COMMIT"
echo "RetroArch for plastron: $OUT/retroarch"
