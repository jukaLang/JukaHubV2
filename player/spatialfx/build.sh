#!/bin/sh
# build.sh — build the JukaHub Spatial game-audio plugin.
#
#   sh player/spatialfx/build.sh                         host build (tests)
#   CROSS=aarch64-linux-gnu- sh player/spatialfx/build.sh  TrimUI build
#   CC="aarch64-linux-gnu-gcc --sysroot=$SYSROOT" sh ...  any compiler line
#   TARGET=windows CROSS=x86_64-w64-mingw32- sh ...       jukahub-spatial.dll
#
# Output: $OUT/jukahub-spatial.so (or .dll), default player/spatialfx/build.
# One library that is both a RetroArch audio DSP filter and a LADSPA plugin
# (README.md). It must load on any handheld firmware, so the build fails when
# the library needs anything but the C and maths libraries (and the dynamic
# loader, see below), a glibc symbol version newer than $MAX_GLIBC (2.17, the
# first aarch64 glibc; the TrimUI Smart Pro has 2.33), or was built for
# another CPU than the compiler line names (EXPECT_MACHINE overrides the
# guess). The source avoids exp/pow/log on purpose: glibc 2.29 gave them new
# versions.
#
# The library is built and checked in a private folder and only then moved
# into $OUT, so a build that fails — or a check tool that is missing — never
# leaves an unchecked library where a package picks it up; an older one in
# $OUT is removed first, so a failed rebuild leaves none at all.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
out=${OUT:-"$here/build"}
target=${TARGET:-linux}
cross=${CROSS:-}
cc=${CC:-"${cross}gcc"}
strip=${STRIP:-"${cross}strip"}
max_glibc=${MAX_GLIBC:-2.17}
extra=${CFLAGS:-}

case "$target" in
linux) name=jukahub-spatial.so ;;
windows) name=jukahub-spatial.dll ;;
*)
	echo "build.sh: TARGET must be linux or windows, not $target" >&2
	exit 2
	;;
esac

mkdir -p "$out"
rm -f "$out/$name"
work=$(mktemp -d "${TMPDIR:-/tmp}/jukahub-spatial.XXXXXX")
staged="$out/.$name.tmp"
trap 'rm -rf "$work"; rm -f "$staged"' EXIT
trap 'exit 1' HUP INT TERM
lib="$work/$name"

case "$target" in
linux)
	# shellcheck disable=SC2086
	$cc -std=c99 -O2 -Wall -Wextra -Wno-unused-parameter $extra \
		-fPIC -shared -fvisibility=hidden -Wl,--no-undefined \
		-Wl,-soname,jukahub-spatial.so -Wl,-z,noexecstack \
		-o "$lib" "$here/jukahub_spatial.c" -lm
	;;
windows)
	# shellcheck disable=SC2086
	$cc -std=c99 -O2 -Wall -Wextra -Wno-unused-parameter $extra \
		-shared -static-libgcc -Wl,--no-undefined \
		-o "$lib" "$here/jukahub_spatial.c"
	;;
esac

if command -v "$strip" >/dev/null 2>&1; then
	"$strip" --strip-unneeded "$lib" || {
		echo "build.sh: $strip failed on $name" >&2
		exit 1
	}
else
	echo "build.sh: $strip not found; leaving symbols in" >&2
fi

pick() {
	for tool in "$@"; do
		if [ -n "$tool" ] && command -v "$tool" >/dev/null 2>&1; then
			echo "$tool"
			return 0
		fi
	done
	return 1
}

# The CPU the compiler line builds for, as the check tools print it.
expect=${EXPECT_MACHINE:-}
if [ -z "$expect" ]; then
	case "$cc $cross" in
	*aarch64*) expect=AArch64 ;;
	*arm*) expect=ARM ;;
	*x86_64* | *x86-64*) expect=X86-64 ;;
	*i686* | *i386*) expect=80386 ;;
	*)
		case "$(uname -m)" in
		x86_64 | amd64) expect=X86-64 ;;
		aarch64 | arm64) expect=AArch64 ;;
		armv6* | armv7*) expect=ARM ;;
		i?86) expect=80386 ;;
		esac
		;;
	esac
fi

fail=0
if [ "$target" = linux ]; then
	# GNU readelf reads every ELF machine, so the host's is a fine fallback.
	readelf=$(pick "${READELF:-}" "${cross}readelf" readelf) || {
		echo "build.sh: no readelf to verify $name" >&2
		exit 1
	}
	machine=$("$readelf" -h "$lib" | sed -n 's/^ *Machine: *//p')
	echo "machine: $machine"
	if [ -z "$machine" ]; then
		echo "build.sh: $readelf could not read $name" >&2
		fail=1
	elif [ -n "$expect" ]; then
		case "$machine" in
		*"$expect"*) ;;
		*)
			echo "build.sh: $name is built for $machine, not $expect (wrong compiler?)" >&2
			fail=1
			;;
		esac
	else
		echo "build.sh: unknown CPU for this compiler line; set EXPECT_MACHINE to check $name" >&2
	fi
	needed=$("$readelf" -d "$lib" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p')
	echo "NEEDED: $(echo "$needed" | tr '\n' ' ')"
	for lib_needed in $needed; do
		case "$lib_needed" in
		# The dynamic loader is mapped into every dynamic process. It shows
		# up when the compiler hardens with the stack protector by default
		# (Ubuntu's aarch64 cross gcc): on aarch64 the canary,
		# __stack_chk_guard, lives in ld-linux-aarch64.so.1 (GLIBC_2.17).
		libc.so.6 | libm.so.6 | ld-linux*.so.[0-9]*) ;;
		*)
			echo "build.sh: $name needs $lib_needed; only libc, libm and the dynamic loader are allowed" >&2
			fail=1
			;;
		esac
	done
	versions=$("$readelf" --dyn-syms -W "$lib" | grep -o 'GLIBC_[0-9][0-9.]*' | sed 's/GLIBC_//' | sort -u -V)
	newest=$(echo "$versions" | tail -n 1)
	echo "glibc symbol versions: $(echo "$versions" | tr '\n' ' ')(newest ${newest:-none}, allowed up to $max_glibc)"
	if [ -n "$newest" ]; then
		top=$(printf '%s\n%s\n' "$newest" "$max_glibc" | sort -V | tail -n 1)
		if [ "$top" != "$max_glibc" ]; then
			echo "build.sh: $name needs GLIBC_$newest, newer than GLIBC_$max_glibc" >&2
			"$readelf" --dyn-syms -W "$lib" | grep "GLIBC_$newest" >&2 || true
			fail=1
		fi
	fi
	exports=$("$readelf" --dyn-syms -W "$lib" | awk '$4 == "FUNC" && $5 == "GLOBAL" && $7 != "UND" {print $8}' | sed 's/@.*//' | sort)
	echo "exports: $(echo "$exports" | tr '\n' ' ')"
	for sym in dspfilter_get_implementation ladspa_descriptor; do
		echo "$exports" | grep -qx "$sym" || {
			echo "build.sh: $name does not export $sym" >&2
			fail=1
		}
	done
else
	objdump=$(pick "${OBJDUMP:-}" "${cross}objdump" objdump) || {
		echo "build.sh: no objdump to verify $name" >&2
		exit 1
	}
	arch=$("$objdump" -f "$lib" | sed -n 's/^architecture: *\([^,]*\).*/\1/p')
	echo "architecture: $arch"
	case "$expect" in
	X86-64) want_arch='*x86-64*' ;;
	80386) want_arch='i386' ;;
	AArch64) want_arch='*aarch64*' ;;
	*) want_arch='' ;;
	esac
	if [ -z "$arch" ]; then
		echo "build.sh: $objdump could not read $name" >&2
		fail=1
	elif [ -n "$want_arch" ]; then
		# shellcheck disable=SC2254
		case "$arch" in
		$want_arch) ;;
		*)
			echo "build.sh: $name is built for $arch, not $expect (wrong compiler?)" >&2
			fail=1
			;;
		esac
	fi
	dlls=$("$objdump" -p "$lib" | sed -n 's/.*DLL Name: *//p')
	echo "imports: $(echo "$dlls" | tr '\n' ' ')"
	for dll in $dlls; do
		case "$(echo "$dll" | tr '[:upper:]' '[:lower:]')" in
		kernel32.dll | msvcrt.dll | api-ms-win-crt-*.dll | ucrtbase.dll) ;;
		*)
			echo "build.sh: $name imports $dll; only the C runtime and KERNEL32 are allowed" >&2
			fail=1
			;;
		esac
	done
	for sym in dspfilter_get_implementation ladspa_descriptor; do
		"$objdump" -p "$lib" | grep -q "$sym" || {
			echo "build.sh: $name does not export $sym" >&2
			fail=1
		}
	done
fi

if [ "$fail" -ne 0 ]; then
	echo "build.sh: $name failed its checks; nothing was put in $out" >&2
	exit 1
fi

# Checked: into place through a temporary name on the same filesystem, so
# $OUT never holds half a library.
cp "$lib" "$staged"
mv -f "$staged" "$out/$name"
echo "built $out/$name ($(wc -c <"$out/$name" | tr -d ' ') bytes)"
