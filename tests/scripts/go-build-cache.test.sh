#!/usr/bin/env bash
# The release build caches (Windows and TrimUI) must come only from an earlier
# push-to-main run of the same workflow whose commit is on main, must never fail
# a release, and must not grow; fetch, which reuses the Remote Play helpers
# under the same rules, must take exactly the files asked for or nothing.
#
# A fake gh answers the GitHub API from fixtures, applying the script's own
# --jq filters with the real jq, so the provenance rules are exercised exactly
# as written rather than restated here.
#
#   usage: tests/scripts/go-build-cache.test.sh
#
# No network. Skipped where jq, zstd, zip or unzip is missing, which is how a
# stock Git Bash looks, but never skipped on CI: a provenance test that quietly
# stops running is worse than a red build.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
script="$root/.github/scripts/go-build-cache.sh"

for tool in jq zstd zip unzip tar; do
  if ! command -v "$tool" > /dev/null 2>&1; then
    if [ "${GITHUB_ACTIONS:-}" = true ]; then
      echo "FAIL: go-build-cache: $tool is not installed on this runner" >&2
      exit 1
    fi
    echo "go-build-cache: skipped ($tool is not installed)"
    exit 0
  fi
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

name=go-build-cache-windows
repo=acme/hub
own=jukahub/JukaHub
self_run=100
workflow=7
head=ffffffffffffffffffffffffffffffffffffffff

# --- fake gh --------------------------------------------------------------
mkdir -p "$work/bin"
cat > "$work/bin/gh" << 'FAKE'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$FAKE_GH_DIR/calls.log"
if [ "${FAKE_GH_FAIL:-0}" = 1 ]; then
  echo "HTTP 502: Bad Gateway" >&2
  exit 1
fi
[ "${1:-}" = api ] || { echo "fake gh: unsupported: $*" >&2; exit 2; }
path=$2
shift 2
expr=""
while [ $# -gt 0 ]; do
  case "$1" in
    --jq) expr=$2; shift 2 ;;
    *) echo "fake gh: unexpected flag $1" >&2; exit 2 ;;
  esac
done
fixture="$FAKE_GH_DIR/$(printf '%s' "$path" | tr '/?&=' '____')"
if [ ! -f "$fixture" ]; then
  echo "HTTP 404: Not Found ($path)" >&2
  exit 1
fi
if [ -n "$expr" ]; then
  jq -r "$expr" "$fixture"
else
  cat "$fixture"
fi
FAKE
chmod +x "$work/bin/gh"

fixtures="$work/fixtures"
mkdir -p "$fixtures"
fixture() { # <api path>  (content on stdin)
  cat > "$fixtures/$(printf '%s' "$1" | tr '/?&=' '____')"
}

sha_of() { printf '%040d' "$1"; } # a stand-in commit id per run
run() { # <run id> <event> <branch> <workflow id> <head repo> [compare status]
  printf '{"id":%s,"event":"%s","head_branch":"%s","workflow_id":%s,"head_repository":{"full_name":"%s"},"head_sha":"%s"}\n' \
    "$1" "$2" "$3" "$4" "$5" "$(sha_of "$1")" | fixture "repos/$repo/actions/runs/$1"
  if [ -n "${6:-}" ]; then
    printf '{"status":"%s"}\n' "$6" | fixture "repos/$repo/compare/$(sha_of "$1")...$head"
  fi
}

# --- cache helpers --------------------------------------------------------
# A Go cache entry pair for <seed>: <2 hex>/<64 hex>-a and -d.
entry_id() {
  if command -v sha256sum > /dev/null 2>&1; then
    printf '%s' "$1" | sha256sum | cut -c1-64
  else
    printf '%s' "$1" | shasum -a 256 | cut -c1-64
  fi
}
mkentry() { # <cache dir> <seed> [content]
  local id dir
  id=$(entry_id "$2")
  dir="$1/${id:0:2}"
  mkdir -p "$dir"
  printf 'action %s\n' "$2" > "$dir/$id-a"
  printf '%s\n' "${3:-output $2}" > "$dir/$id-d"
}
has_entry() { # <cache dir> <seed>
  local id
  id=$(entry_id "$2")
  [ -f "$1/${id:0:2}/$id-a" ] && [ -f "$1/${id:0:2}/$id-d" ]
}
use_entry() { # what the go command does to an entry it reads
  local id
  id=$(entry_id "$2")
  touch "$1/${id:0:2}/$id-a" "$1/${id:0:2}/$id-d"
}
steps_in() { find "$1" -mindepth 2 -maxdepth 2 -type f -name '*-a' | wc -l | tr -d ' '; }

# Builds the artifact zip save would have produced from a directory of entries.
make_artifact() { # <artifact id> <source cache dir>
  local stage="$work/stage-$1"
  mkdir -p "$stage"
  rm -f "$work/artifact-$1.zip"
  # Links too: the refusal of one is under test below.
  (cd "$2" && find . -mindepth 2 \( -type f -o -type l \) | sed 's|^\./||' | LC_ALL=C sort | tar -cf - -T -) \
    | zstd -q -f -c > "$stage/$name.tar.zst"
  (cd "$stage" && zip -q -0 "$work/artifact-$1.zip" "$name.tar.zst")
  fixture "repos/$repo/actions/artifacts/$1/zip" < "$work/artifact-$1.zip"
}

artifact() { # <artifact id> <run id> <created_at> <expired> <head repo id> [branch]
  printf '{"id":%s,"name":"%s","expired":%s,"created_at":"%s","workflow_run":{"id":%s,"repository_id":1,"head_repository_id":%s,"head_branch":"%s"}}' \
    "$1" "$name" "$4" "$3" "$2" "$5" "${6:-main}"
}

# Runs the script the way the workflow does, with a clean state per scenario.
run_script() { # <scenario> <args...>
  local scenario=$1
  shift
  mkdir -p "$work/$scenario/tmp"
  PATH="$work/bin:$PATH" \
    FAKE_GH_DIR="$fixtures" \
    FAKE_GH_FAIL="${FAKE_GH_FAIL:-0}" \
    GITHUB_REPOSITORY="$repo" \
    GITHUB_RUN_ID="$self_run" \
    GITHUB_SHA="$head" \
    RUNNER_TEMP="$work/$scenario/tmp" \
    GITHUB_OUTPUT="$work/$scenario/output" \
    GITHUB_STEP_SUMMARY="$work/$scenario/summary" \
    GO_BUILD_CACHE_DIR="$work/$scenario/gocache" \
    bash "$script" "$@" > "$work/$scenario/log" 2>&1
}
state_of() { cat "$work/$1/tmp/go-build-cache.state" 2> /dev/null || true; }

# This run itself passes every check (a re-run attempt builds the same commit),
# so only the explicit self test keeps restore off an earlier attempt's cache.
run "$self_run" push main "$workflow" "$repo" identical

# 1. Nothing uploaded yet: a cold build, and save uploads what it built. -----
echo '{"total_count":0,"artifacts":[]}' | fixture "repos/$repo/actions/artifacts?name=$name&per_page=100"
s="$work/cold"
mkdir -p "$s/gocache"
run_script cold restore "$name" || fail "restore without any artifact exited non-zero: $(cat "$s/log")"
state_of cold | grep -q '^restored=0$' || fail "a cold start was recorded as restored"
grep -q 'none from an earlier release build' "$s/summary" || fail "the summary does not say the build is cold"
for i in 1 2 3; do mkentry "$s/gocache" "cold-$i"; done
echo "trimmed" > "$s/gocache/README"
printf '%s\n' runtime github.com/veandco/go-sdl2/sdl "$own" > "$s/build.log"
run_script cold save "$name" "$s/out" "$s/build.log" "$own" || fail "save exited non-zero: $(cat "$s/log")"
grep -q '^upload=true$' "$s/output" || fail "a cold build did not upload its cache: $(cat "$s/log")"
grep -q "^archive=$s/out/$name.tar.zst\$" "$s/output" || fail "save did not report the archive path: $(cat "$s/output")"
[ -f "$s/out/$name.tar.zst" ] || fail "save reported an upload but wrote no archive"
members=$(zstd -dc "$s/out/$name.tar.zst" | tar -tf -)
[ "$(printf '%s\n' "$members" | wc -l | tr -d ' ')" = 6 ] || fail "the archive should hold 3 entry pairs, holds: $members"
if printf '%s\n' "$members" | grep -q README; then
  fail "the archive carries files that are not cache entries"
fi

# 2. Provenance: only an earlier push-to-main run of this workflow whose -----
#    commit is in the history being built counts.
good="$work/src-good"
older="$work/src-older"
for i in $(seq 1 20); do mkentry "$good" "dep-$i" "good $i"; done
mkentry "$older" "older" "older"
for id in 501 502 503 504 505 507 512 513 514 515; do make_artifact "$id" "$older"; done
make_artifact 506 "$good"
# 516 has no zip fixture: its download fails, and restore must move on.
{
  printf '{"total_count":12,"artifacts":['
  artifact 507 207 2026-11-01T00:00:00Z false 1 # valid but older than 506
  printf ','
  artifact 501 201 2026-11-09T09:00:00Z false 1 # a pull-request run
  printf ','
  artifact 502 202 2026-11-09T08:00:00Z false 1 # another workflow
  printf ','
  artifact 512 205 2026-11-09T07:30:00Z false 1 # a tag named "main"
  printf ','
  artifact 513 208 2026-11-09T07:20:00Z false 1 # listed as main, run says feature
  printf ','
  artifact 514 209 2026-11-09T07:10:00Z false 1 # listed as this repo, run says a fork
  printf ','
  artifact 503 203 2026-11-09T07:00:00Z false 99 # listed as a fork
  printf ','
  artifact 504 204 2026-11-09T06:00:00Z true 1 # expired
  printf ','
  printf '{"id":515,"name":"%s","expired":false,"created_at":"2026-11-09T05:30:00Z","workflow_run":null}' "$name"
  printf ','
  artifact 505 "$self_run" 2026-11-09T05:00:00Z false 1 # this run, an earlier attempt
  printf ','
  artifact 516 211 2026-11-09T04:00:00Z false 1 # acceptable, but the download fails
  printf ','
  artifact 506 206 2026-11-08T00:00:00Z false 1 # the one to use
  printf ']}\n'
} | fixture "repos/$repo/actions/artifacts?name=$name&per_page=100"
run 201 pull_request main "$workflow" "$repo" ahead
run 202 push main 8 "$repo" ahead
run 203 push main "$workflow" "someone/hub" ahead
# Only the expiry rules 504 out: its run would otherwise pass every check.
run 204 push main "$workflow" "$repo" ahead
run 205 push main "$workflow" "$repo" diverged
run 206 push main "$workflow" "$repo" ahead
run 207 push main "$workflow" "$repo" ahead
run 208 push feature "$workflow" "$repo" ahead
run 209 push main "$workflow" "someone/hub" ahead
run 211 push main "$workflow" "$repo" identical

s="$work/provenance"
: > "$fixtures/calls.log"
run_script provenance restore "$name" || fail "restore exited non-zero: $(cat "$s/log")"
state_of provenance | grep -q '^restored=1$' || fail "no cache was restored: $(cat "$s/log")"
state_of provenance | grep -q '^run=206$' || fail "restored from the wrong run: $(state_of provenance)"
has_entry "$s/gocache" dep-7 || fail "the restored cache is missing an entry"
[ "$(steps_in "$s/gocache")" = 20 ] || fail "restored $(steps_in "$s/gocache") steps, not 20"
if has_entry "$s/gocache" older; then
  fail "an entry from a rejected artifact was restored"
fi
for id in 501 502 503 504 505 507 512 513 514 515; do
  if grep -q "artifacts/$id/zip" "$fixtures/calls.log"; then
    fail "artifact $id was downloaded although it does not qualify"
  fi
done
for r in 201 202 205 208 209; do
  grep -q "actions/runs/$r " "$fixtures/calls.log" || fail "run $r was never looked at, so the check that rejects it went untested"
done
grep -q "artifacts/516/zip" "$fixtures/calls.log" || fail "the acceptable artifact whose download fails was not tried"
grep -q 'not in this build.s history (diverged)' "$s/log" || fail "the tag named main was not rejected by the commit check: $(cat "$s/log")"
# Backdated, so save can tell what the build used; and a fresh trim stamp, so
# Go does not delete the backdated entries before the build gets to them.
if [ -n "$(find "$s/gocache" -mindepth 2 -type f -newermt '2001-01-01' -print -quit)" ]; then
  fail "restored entries were not backdated"
fi
[ -f "$s/gocache/trim.txt" ] || fail "restore wrote no trim.txt"
stamp=$(cat "$s/gocache/trim.txt")
[ $(($(date +%s) - stamp)) -lt 120 ] || fail "trim.txt does not hold a fresh stamp: $stamp"
if [ -n "$(find "$s/tmp" -name 'go-build-cache.??????' -print -quit)" ]; then
  fail "restore left its download directory behind"
fi
if [ -n "$(find "$s" -maxdepth 1 -name 'gocache.restore.*' -print -quit)" ]; then
  fail "restore left its staging directory next to the cache"
fi

# 3. A build that compiled only the player's own packages: prune, but upload
#    nothing. The log carries the chatter a real go build -v can print.
for i in $(seq 1 15); do use_entry "$s/gocache" "dep-$i"; done
for i in 1 2 3 4 5 6 7; do mkentry "$s/gocache" "player-$i"; done
cat > "$s/build.log" << EOF
go: downloading github.com/veandco/go-sdl2 v0.4.40
# github.com/veandco/go-sdl2/sdl
sdl_wrapper.h:3:10: warning: unused variable 'x' [-Wunused-variable]
$own/internal/jdelta
$own
EOF
run_script provenance save "$name" "$s/out" "$s/build.log" "$own" || fail "save exited non-zero: $(cat "$s/log")"
grep -q '^upload=false$' "$s/output" || fail "a player-only build uploaded a new cache: $(cat "$s/log")"
[ ! -e "$s/out/$name.tar.zst" ] || fail "an archive was written although nothing is uploaded"
[ "$(steps_in "$s/gocache")" = 22 ] || fail "prune should keep 15 used + 7 new steps, kept $(steps_in "$s/gocache")"
if has_entry "$s/gocache" dep-20; then
  fail "an entry the build did not use survived the prune"
fi
has_entry "$s/gocache" dep-3 || fail "an entry the build used was pruned"

# 4. A build that compiled a dependency again: upload the pruned cache. -------
s="$work/bump"
run_script bump restore "$name" || fail "restore exited non-zero: $(cat "$s/log")"
for i in $(seq 1 10); do use_entry "$s/gocache" "dep-$i"; done
for i in 1 2 3; do mkentry "$s/gocache" "bumped-$i"; done
printf '%s\n' github.com/veandco/go-sdl2/sdl "$own" > "$s/build.log"
run_script bump save "$name" "$s/out" "$s/build.log" "$own" || fail "save exited non-zero: $(cat "$s/log")"
grep -q '^upload=true$' "$s/output" || fail "a build that recompiled a dependency did not upload: $(cat "$s/log")"
members=$(zstd -dc "$s/out/$name.tar.zst" | tar -tf -)
[ "$(printf '%s\n' "$members" | grep -c -- '-a$')" = 13 ] || fail "the archive should hold 10 used + 3 new steps"
id=$(entry_id dep-15)
if printf '%s\n' "$members" | grep -q "$id"; then
  fail "the uploaded cache carries an entry the build did not use"
fi
grep -q 'saved 13' "$s/summary" || fail "the summary does not report the saved cache: $(cat "$s/summary")"

# 5. No build log after a restore: no telling what compiled, so upload. ------
s="$work/nolog"
run_script nolog restore "$name" || fail "restore exited non-zero: $(cat "$s/log")"
use_entry "$s/gocache" dep-1
run_script nolog save "$name" "$s/out" "$s/missing.log" "$own" || fail "save exited non-zero: $(cat "$s/log")"
grep -q '^upload=true$' "$s/output" || fail "save without a build log did not upload: $(cat "$s/log")"

# 6. A build that failed on the restored cache: discard empties it and makes
#    the retry's cache the one that gets uploaded. Without a restored cache it
#    refuses, so the workflow fails instead of repeating a cold build.
if run_script cold discard; then
  fail "discard agreed to a retry although nothing was restored"
fi
s="$work/discard"
run_script discard restore "$name" || fail "restore exited non-zero: $(cat "$s/log")"
run_script discard discard || fail "discard exited non-zero: $(cat "$s/log")"
[ "$(steps_in "$s/gocache")" = 0 ] || fail "discard left $(steps_in "$s/gocache") cached steps"
state_of discard | grep -q '^restored=0$' || fail "discard did not forget the restore"
mkentry "$s/gocache" "rebuilt"
printf '%s\n' "$own" > "$s/build.log"
run_script discard save "$name" "$s/out" "$s/build.log" "$own" || fail "save exited non-zero: $(cat "$s/log")"
grep -q '^upload=true$' "$s/output" || fail "the cache rebuilt after a discard was not uploaded"

# 7. The API failing is a cold build, not a failed release. ------------------
s="$work/down"
FAKE_GH_FAIL=1 run_script down restore "$name" || fail "restore failed the step when the API was down"
grep -q '::warning' "$s/log" || fail "an API failure produced no warning"
state_of down | grep -q '^restored=0$' || fail "an API failure was recorded as restored"

# 8. An archive with anything but cache entries is refused, not unpacked, and
#    restore falls back to the next acceptable one (507, the older cache).
tamper() { # <scenario> <source dir>
  local s="$work/$1"
  make_artifact 506 "$2"
  run_script "$1" restore "$name" || fail "restore failed the step on a bad archive"
  grep -q '::warning' "$s/log" || fail "$1: the bad archive produced no warning"
  state_of "$1" | grep -q '^run=207$' || fail "$1: restore did not fall back to the next cache: $(state_of "$1")"
  has_entry "$s/gocache" older || fail "$1: the fallback cache was not unpacked"
  if has_entry "$s/gocache" fine || [ -n "$(find "$s/gocache" \( -type l -o -name '*.txt' ! -name trim.txt \) -print -quit)" ]; then
    fail "$1: something from the bad archive was unpacked"
  fi
}
odd="$work/src-odd"
mkentry "$odd" "fine"
mkdir -p "$odd/ab"
echo "not an entry" > "$odd/ab/notes.txt"
tamper oddname "$odd"
link="$work/src-link"
mkentry "$link" "fine"
lid=$(entry_id "evil")
mkdir -p "$link/${lid:0:2}"
ln -s /etc/passwd "$link/${lid:0:2}/$lid-d"
tamper symlink "$link"

# 9. Usage errors are loud: they are workflow bugs, not cache misses. --------
usage_error() { # <args...>: must exit 2
  local rc=0
  bash "$script" "$@" > /dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "'$*' exited $rc instead of failing as a usage error"
}
usage_error restore
usage_error restore 'a/b'
usage_error save "$name" "$work/out"
usage_error discard extra
usage_error fetch "$name" "$work/out"
usage_error fetch "$name" "" chiaki-stream
usage_error fetch "$name" "$work/out" bin/chiaki-stream
usage_error fetch "$name" "$work/out" ..
usage_error fetch 'a/b' "$work/out" chiaki-stream

# 10. fetch: a few plain files under the same provenance rules (the Remote
#     Play helpers). The newest acceptable artifact holding exactly the files
#     asked for wins; anything else is refused and the next one is tried.
hname=remote-play-helpers-0123456789abcdef
helpers=(chiaki-stream chiaki-regist BUILDINFO.txt)
hartifact() { # <artifact id> <run id> <created_at>
  printf '{"id":%s,"name":"%s","expired":false,"created_at":"%s","workflow_run":{"id":%s,"repository_id":1,"head_repository_id":1,"head_branch":"main"}}' \
    "$1" "$hname" "$3" "$2"
}
files_artifact() { # <artifact id> <source dir>: every entry of it, links kept as links
  rm -f "$work/files-$1.zip"
  (cd "$2" && zip -q -r -y "$work/files-$1.zip" -- *)
  fixture "repos/$repo/actions/artifacts/$1/zip" < "$work/files-$1.zip"
}
helper_set() { # <dir> <tag>: the three files, each saying where it came from
  mkdir -p "$1"
  for f in "${helpers[@]}"; do printf '%s from %s\n' "$f" "$2" > "$1/$f"; done
}
helper_set "$work/h-good" good
helper_set "$work/h-extra" extra
echo "not asked for" > "$work/h-extra/notes.txt"
helper_set "$work/h-short" short
rm "$work/h-short/chiaki-regist"
helper_set "$work/h-link" link
rm "$work/h-link/chiaki-stream"
ln -s /etc/passwd "$work/h-link/chiaki-stream"
helper_set "$work/h-nested/bin" nested
files_artifact 603 "$work/h-extra"
files_artifact 604 "$work/h-short"
files_artifact 605 "$work/h-link"
files_artifact 606 "$work/h-nested"
files_artifact 607 "$work/h-good"
printf 'this is not a zip\n' | fixture "repos/$repo/actions/artifacts/608/zip"
helper_set "$work/h-self" self
files_artifact 609 "$work/h-self"
{
  printf '{"total_count":9,"artifacts":['
  hartifact 609 "$self_run" 2026-11-10T10:00:00Z # this run, an earlier attempt
  printf ','
  hartifact 601 201 2026-11-10T09:00:00Z # a pull-request run
  printf ','
  hartifact 602 205 2026-11-10T08:00:00Z # a tag named "main"
  printf ','
  hartifact 608 206 2026-11-10T07:30:00Z # not a zip
  printf ','
  hartifact 603 206 2026-11-10T07:00:00Z # an extra file
  printf ','
  hartifact 604 206 2026-11-10T06:00:00Z # a file missing
  printf ','
  hartifact 605 206 2026-11-10T05:00:00Z # a link
  printf ','
  hartifact 606 206 2026-11-10T04:00:00Z # the files in a folder
  printf ','
  hartifact 607 207 2026-11-10T03:00:00Z # the one to take
  printf ']}\n'
} | fixture "repos/$repo/actions/artifacts?name=$hname&per_page=100"

s="$work/fetch"
: > "$fixtures/calls.log"
run_script fetch fetch "$hname" "$s/bin" "${helpers[@]}" || fail "fetch found nothing to take: $(cat "$s/log")"
for f in "${helpers[@]}"; do
  grep -qx "$f from good" "$s/bin/$f" 2> /dev/null || fail "fetch did not take $f from the acceptable artifact: $(cat "$s/log")"
done
[ "$(find "$s/bin" -mindepth 1 | wc -l | tr -d ' ')" = 3 ] || fail "fetch put more than the three files in place: $(ls -la "$s/bin")"
grep -q '^run=207$' "$s/output" || fail "fetch did not report the run its files came from: $(cat "$s/output" 2> /dev/null)"
for id in 601 602 609; do
  if grep -q "artifacts/$id/zip" "$fixtures/calls.log"; then
    fail "artifact $id was downloaded although its run does not qualify"
  fi
done
for id in 603 604 605 606 608; do
  grep -q "artifacts/$id/zip" "$fixtures/calls.log" || fail "artifact $id was never tried, so its refusal went untested"
done
[ "$(grep -c '::warning' "$s/log")" -ge 5 ] || fail "the five refused artifacts did not each warn: $(cat "$s/log")"
if [ -n "$(find "$s/tmp" -name 'go-build-cache.??????' -print -quit)" ]; then
  fail "fetch left its download directory behind"
fi

# Nothing acceptable: exit 1 and nothing in place, so the caller builds.
s="$work/fetch-none"
echo '{"total_count":0,"artifacts":[]}' | fixture "repos/$repo/actions/artifacts?name=remote-play-helpers-ffff&per_page=100"
if run_script fetch-none fetch remote-play-helpers-ffff "$s/bin" "${helpers[@]}"; then
  fail "fetch reported success with no artifact to take"
fi
[ ! -e "$s/bin" ] || [ -z "$(ls -A "$s/bin")" ] || fail "fetch without an artifact left files behind"

# The API failing is a build, not a failed release: down from the start, or
# answering the run lookup but not the artifact list.
s="$work/fetch-down"
if FAKE_GH_FAIL=1 run_script fetch-down fetch "$hname" "$s/bin" "${helpers[@]}"; then
  fail "fetch reported success while the API was down"
fi
grep -q '::warning' "$s/log" || fail "an API failure during fetch produced no warning"
s="$work/fetch-nolist"
if run_script fetch-nolist fetch remote-play-helpers-unlisted "$s/bin" "${helpers[@]}"; then
  fail "fetch reported success although the artifact list could not be read"
fi
grep -q 'could not list' "$s/log" || fail "a failed artifact list produced no warning: $(cat "$s/log")"

echo "go-build-cache: ok"
