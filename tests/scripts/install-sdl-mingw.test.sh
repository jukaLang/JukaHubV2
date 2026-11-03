#!/usr/bin/env bash
# The shared SDL installer must refuse an archive whose digest does not match.
#
# A checksum check nobody has ever seen fail is indistinguishable from no check
# at all, so this drives both outcomes through a local manifest: the tampered
# tarball has to be rejected and left unextracted, and the intact one accepted
# through the same code path, so a harness that fails for an unrelated reason
# cannot pass as "the check works".
#
#   usage: tests/scripts/install-sdl-mingw.test.sh
#
# No network: the archive is built here and fetched through a file:// URL.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
installer="$root/.github/scripts/install-sdl-mingw.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

sha() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# Git Bash's /tmp is not a path the native curl understands (it resolves it
# under C:/tmp), so a file:// URL needs the Windows form there. Elsewhere
# this is the identity, because cygpath only exists on Windows.
as_url_path() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -m "$1"
  else
    printf '%s' "$1"
  fi
}

# A one-file archive, so extraction is observable after the fact.
mkdir -p "$work/src"
echo "payload" > "$work/src/payload.txt"
tar -czf "$work/good.tar.gz" -C "$work/src" payload.txt
good=$(sha "$work/good.tar.gz")
bad=0000000000000000000000000000000000000000000000000000000000000000

# 1. A digest that does not match the bytes: no extraction, non-zero exit, and a
#    message that names the failure instead of dying somewhere further along.
printf 'good.tar.gz  %s  file://%s\n' "$bad" "$(as_url_path "$work/good.tar.gz")" > "$work/tampered.list"
mkdir -p "$work/tampered"
if SDL_MINGW_ARCHIVES="$work/tampered.list" bash "$installer" "$work/tampered" > "$work/tampered.log" 2>&1; then
  fail "the installer accepted an archive whose digest does not match"
fi
if [ -e "$work/tampered/payload.txt" ]; then
  fail "the tampered archive was extracted anyway"
fi
if ! grep -qiE 'FAILED|did NOT match' "$work/tampered.log"; then
  fail "no verification failure was reported: $(cat "$work/tampered.log")"
fi

# 2. The same archive with its real digest, to prove the rejection above came
#    from the digest and not from a broken harness.
printf 'good.tar.gz  %s  file://%s\n' "$good" "$(as_url_path "$work/good.tar.gz")" > "$work/intact.list"
mkdir -p "$work/intact"
if ! SDL_MINGW_ARCHIVES="$work/intact.list" bash "$installer" "$work/intact" > "$work/intact.log" 2>&1; then
  fail "the installer rejected the intact archive: $(cat "$work/intact.log")"
fi
if [ ! -f "$work/intact/payload.txt" ]; then
  fail "the intact archive was not extracted"
fi
if [ -e "$work/intact/good.tar.gz" ]; then
  fail "the downloaded archive was left behind after extraction"
fi

# 3. An empty list is a configuration error, not a quiet "installed nothing".
: > "$work/empty.list"
if SDL_MINGW_ARCHIVES="$work/empty.list" bash "$installer" "$work/empty" > /dev/null 2>&1; then
  fail "an empty archive list was accepted as a successful install"
fi

# 4. A list that cannot be read fails before any download, not after.
if SDL_MINGW_ARCHIVES="$work/does-not-exist.list" bash "$installer" "$work/missing" > /dev/null 2>&1; then
  fail "an unreadable archive list was accepted"
fi

echo "install-sdl-mingw: ok"
