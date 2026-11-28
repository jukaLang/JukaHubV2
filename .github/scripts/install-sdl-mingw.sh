#!/usr/bin/env bash
# Download and verify the SDL2 MinGW development tarballs.
#
# Two jobs need these same three archives: the weekly Windows build (which
# installs them into a local prefix and runs the tests) and the release
# pipeline's Windows cross-build on Ubuntu (which merges them into one prefix
# for the MinGW-w64 compiler). Keeping the versions, URLs and digests here
# means a version bump is one edit, instead of two copies that quietly drift
# apart.
#
#   usage: install-sdl-mingw.sh <dest-dir>
#
# The tarballs are downloaded together into <dest-dir>, verified against the
# digests below, extracted there and deleted. Installing the extracted tree is
# the caller's business, because the two jobs install into different places.
#
# SDL_MINGW_ARCHIVES=<file> replaces the built-in list with one in the same
# `file digest url` format. Only the test uses it, to drive the verification
# path against a local archive: a checksum check that has never been seen to
# fail is indistinguishable from no check at all.
set -euo pipefail

dest=${1:-}
if [ -z "$dest" ]; then
  echo "usage: $0 <dest-dir>" >&2
  exit 2
fi

if [ -n "${SDL_MINGW_ARCHIVES:-}" ] && [ ! -r "$SDL_MINGW_ARCHIVES" ]; then
  echo "cannot read archive list: $SDL_MINGW_ARCHIVES" >&2
  exit 2
fi

# sha256sum on Linux and Git Bash, shasum on macOS. A missing verifier is fatal
# on purpose: silently skipping the check is the failure this script exists to
# prevent.
verify() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c -
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c -
  else
    echo "no sha256 verifier found (sha256sum or shasum)" >&2
    exit 1
  fi
}

# Git Bash on some Windows machines has neither curl nor wget on PATH even
# though curl.exe sits in System32. Try each in turn and fail loudly if none
# can fetch: skipping verification is worse than stopping the install.
fetch_archive() {
  out=$1
  url=$2
  for tool in curl curl.exe /c/Windows/System32/curl.exe; do
    if command -v "$tool" >/dev/null 2>&1 || [ -x "$tool" ]; then
      "$tool" -fsSL --retry 2 --retry-delay 2 --connect-timeout 15 --max-time 90 \
        -o "$out" "$url"
      return
    fi
  done
  if command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url"
    return
  fi
  echo "no downloader found (curl or wget)" >&2
  return 1
}

archives() {
  if [ -n "${SDL_MINGW_ARCHIVES:-}" ]; then
    cat "$SDL_MINGW_ARCHIVES"
    return
  fi
  cat <<'LIST'
SDL2-devel-2.30.11-mingw.tar.gz       0590db0a47b564aab92499ed03eaa75db9aee17c371080caa3b05df9d49d9ff9  https://github.com/libsdl-org/SDL/releases/download/release-2.30.11/SDL2-devel-2.30.11-mingw.tar.gz
SDL2_image-devel-2.8.4-mingw.tar.gz   7c0b437c54919ec3899f3edf49f8f260f5f2703451ee32e61da77eccbc04f542  https://github.com/libsdl-org/SDL_image/releases/download/release-2.8.4/SDL2_image-devel-2.8.4-mingw.tar.gz
SDL2_ttf-devel-2.22.0-mingw.tar.gz    7e3edf46630bbfb1cb8bffd69a5456b037469ed7a20a75232caee0b1752dc65b  https://github.com/libsdl-org/SDL_ttf/releases/download/release-2.22.0/SDL2_ttf-devel-2.22.0-mingw.tar.gz
LIST
}

mkdir -p "$dest"
cd "$dest"

# Fetch independent archives together. Verify all of them before extracting
# any, so a failed download or checksum cannot leave a partial SDL install.
files=()
digests=()
pids=()
while read -r file digest url; do
  [ -n "${file:-}" ] || continue
  echo "fetching $file"
  files+=("$file")
  digests+=("$digest")
  fetch_archive "$file" "$url" &
  pids+=("$!")
done < <(archives)

# An empty list would otherwise look like a successful install of nothing.
if [ "${#files[@]}" -eq 0 ]; then
  echo "no archives listed; refusing to report success" >&2
  exit 1
fi

failed=0
for i in "${!pids[@]}"; do
  if ! wait "${pids[i]}"; then
    echo "could not download ${files[i]}" >&2
    failed=1
  fi
done
[ "$failed" -eq 0 ] || exit 1

for i in "${!files[@]}"; do
  printf '%s  %s\n' "${digests[i]}" "${files[i]}" | verify
done

for file in "${files[@]}"; do
  tar -xzf "$file"
  rm -f "$file"
done
