#!/usr/bin/env bash
# Carry the Go build cache from one Windows release build to the next.
#
# A release compiles the Go standard library, every module and the cgo-heavy
# SDL bindings for windows/amd64. From an empty cache that is most of the job;
# with the cache an earlier release left behind, only the player compiles.
#
# Where that cache may come from is the point of this script. actions/cache
# would be simpler, but any workflow that runs on main can write an
# actions/cache entry, and a cache restored into a job whose output is
# published decides what code ends up in the release. This cache is a workflow
# artifact instead, and `restore` takes it only from an earlier run of the same
# workflow file, started by a push to main of this repository, whose commit is
# in the history being built now (a tag named "main" also reports "push" and
# "main", so the commit check is what rules it out). Such a run is exactly as
# trusted as the one about to use the cache. Pull-request runs may read it; the
# workflow never uploads one from them. Any job of an accepted run could have
# uploaded the artifact, but every job of the release workflow can already
# write the release itself, so that adds no one to trust.
#
#   go-build-cache.sh restore <name>
#       before the build: unpack the newest acceptable cache into GOCACHE.
#   go-build-cache.sh save <name> <dir> <build-log> <own-module>
#       after it: may write <dir>/<name>.tar.zst, and sets the step outputs
#       upload=true|false and archive=<path>.
#   go-build-cache.sh discard
#       after a failed build: if it ran on a restored cache, empty the cache
#       for a retry; exits 1 when nothing was restored, since a cold retry
#       would only repeat the failure.
#
# restore and save fail open: a missing, expired, unreadable or untrusted cache
# costs a cold build, never a release. To force one, delete the <name>
# artifacts on the repository's Actions page.
#
# restore backdates every entry it unpacks. The go command refreshes an entry's
# mtime when it uses it (its own cache trimming relies on that), so an entry
# still backdated after the build was not needed, and save drops it: the
# archive holds what the last build used, not every toolchain and module
# version ever built. save writes an archive when nothing was restored, or when
# `go build -v` (whose output is <build-log>; it names only the packages it
# actually compiled) compiled something outside <own-module>. A release that
# changed only the player's own code, the usual case, uploads nothing.
#
# Needs gh (GH_TOKEN with actions: read and contents: read), zstd, tar and
# unzip. Reads GITHUB_REPOSITORY, GITHUB_RUN_ID and GITHUB_SHA, keeps restore's
# result for save in $RUNNER_TEMP, and reports to GITHUB_OUTPUT and
# GITHUB_STEP_SUMMARY when they are set. GO_BUILD_CACHE_DIR replaces
# `go env GOCACHE` (the test uses it).
set -uo pipefail

# How many of the newest artifacts to consider before settling for a cold
# build, and the "not used by this build" stamp (2000-01-01 00:00).
CANDIDATES=10
BACKDATE=200001010000

usage() {
  echo "usage: $0 restore <name> | save <name> <dir> <build-log> <own-module> | discard" >&2
  exit 2
}

warn() { printf '::warning title=Go build cache::%s\n' "$*"; }
note() { printf 'go-build-cache: %s\n' "$*"; }

summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf -- '- %s\n' "$*" >> "$GITHUB_STEP_SUMMARY"
  fi
}

set_output() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"
  fi
}

tmp_root() { printf '%s' "${RUNNER_TEMP:-${TMPDIR:-/tmp}}"; }
state_file() { printf '%s/go-build-cache.state' "$(tmp_root)"; }

cache_dir() {
  if [ -n "${GO_BUILD_CACHE_DIR:-}" ]; then
    printf '%s\n' "$GO_BUILD_CACHE_DIR"
  else
    go env GOCACHE
  fi
}

# The cache's entries, relative to it: <2 hex>/<64 hex>-a for a cached build
# step and -d for its output. README, trim.txt and anything else Go keeps in
# the directory are not entries and never travel.
entries() {
  (cd "$1" && find . -mindepth 2 -maxdepth 2 -type f -path './[0-9a-f][0-9a-f]/*' \
    \( -name '*-a' -o -name '*-d' \) | sed 's|^\./||')
}

count_steps() {
  entries "$1" | grep -c -- '-a$' || true
}

write_state() { # <restored 0|1> <source run>
  printf 'restored=%s\nrun=%s\n' "$1" "$2" > "$(state_file)" 2> /dev/null || true
}

# Sets restored and from_run from what restore left behind; no state (restore
# never ran, or was killed before it finished) reads as "nothing restored".
read_state() {
  restored=0
  from_run=""
  local f k v
  f=$(state_file)
  [ -f "$f" ] || return 0
  while IFS='=' read -r k v; do
    case "$k" in
      restored) restored=$v ;;
      run) from_run=$v ;;
    esac
  done < "$f"
  case "$restored" in 1) ;; *) restored=0 ;; esac
}

restore() {
  local name=$1
  local repo=${GITHUB_REPOSITORY:-} self=${GITHUB_RUN_ID:-} head=${GITHUB_SHA:-}
  local cache self_wf candidates run art created work tool steps found=""

  # save reads this whatever happens below.
  write_state 0 ""

  if [ -z "$repo" ] || [ -z "$self" ] || [ -z "$head" ]; then
    warn "GITHUB_REPOSITORY, GITHUB_RUN_ID or GITHUB_SHA is not set; building without a cache"
    return 0
  fi
  for tool in gh zstd tar unzip; do
    if ! command -v "$tool" > /dev/null 2>&1; then
      warn "$tool is not installed; building without a cache"
      return 0
    fi
  done
  if ! cache=$(cache_dir) || [ -z "$cache" ]; then
    warn "could not locate the Go build cache; building without one"
    return 0
  fi

  # The same workflow file has the same id on every branch, so an artifact
  # from another workflow (ci.yml, nightly.yml, ...) never qualifies.
  if ! self_wf=$(gh api "repos/$repo/actions/runs/$self" --jq '.workflow_id') || [ -z "$self_wf" ]; then
    warn "could not look up this run's workflow; building without a cache"
    return 0
  fi

  # Newest first. The branch and fork tests here only narrow the list;
  # trusted_run decides.
  if ! candidates=$(gh api "repos/$repo/actions/artifacts?name=$name&per_page=100" --jq "
      [.artifacts[]
        | select(.name == \"$name\" and .expired == false
                 and .workflow_run != null
                 and .workflow_run.head_branch == \"main\"
                 and .workflow_run.head_repository_id == .workflow_run.repository_id)]
      | sort_by(.created_at) | reverse | .[:$CANDIDATES][]
      | \"\(.workflow_run.id) \(.id) \(.created_at)\""); then
    warn "could not list the build-cache artifacts; building without a cache"
    return 0
  fi

  if ! work=$(mktemp -d "$(tmp_root)/go-build-cache.XXXXXX"); then
    warn "no temporary directory; building without a cache"
    return 0
  fi
  while read -r run art created; do
    [ -n "${run:-}" ] || continue
    # A re-run attempt must not read what an earlier attempt of itself left.
    [ "$run" = "$self" ] && continue
    trusted_run "$repo" "$run" "$self_wf" "$head" || continue
    if unpack "$repo" "$run" "$art" "$name" "$cache" "$work"; then
      found="$run"
      break
    fi
    rm -rf "${work:?}"/*
  done << EOF
$candidates
EOF
  rm -rf "$work"

  if [ -z "$found" ]; then
    note "no usable cache from an earlier release build on main; this build starts cold"
    summary "Go build cache: none from an earlier release build, so everything compiles."
    return 0
  fi

  # Go trims entries unused for five days, at most once a day, going by the
  # stamp in trim.txt. A fresh stamp keeps it from deleting the backdated
  # entries before the build has had the chance to use them.
  date +%s > "$cache/trim.txt"
  (cd "$cache" && find . -mindepth 2 -maxdepth 2 -type f -path './[0-9a-f][0-9a-f]/*' \
    \( -name '*-a' -o -name '*-d' \) -exec touch -m -t "$BACKDATE" {} +)

  steps=$(count_steps "$cache")
  write_state 1 "$found"
  note "restored $steps cached build steps from run $found (uploaded $created) in ${SECONDS}s"
  summary "Go build cache: $steps cached build steps from the release build in run $found (uploaded $created)."
}

# An earlier run of this workflow file, started by a push to main of this
# repository, whose commit is an ancestor of (or the same as) the one being
# built. The last test is the one a tag named "main" fails: its runs report
# event push and head_branch main too, but its commit is not on main.
trusted_run() {
  local repo=$1 run=$2 self_wf=$3 head=$4 verdict sha status
  if ! verdict=$(gh api "repos/$repo/actions/runs/$run" \
    --jq '"\(.event) \(.head_branch) \(.workflow_id) \(.head_repository.full_name) \(.head_sha)"'); then
    note "could not look up run $run; skipping its cache"
    return 1
  fi
  sha=${verdict##* }
  if [ "${verdict% *}" != "push main $self_wf $repo" ]; then
    note "not using the cache from run $run (${verdict% *})"
    return 1
  fi
  if ! [[ $sha =~ ^[0-9a-f]{40}$ ]]; then
    note "run $run names no commit; skipping its cache"
    return 1
  fi
  if ! status=$(gh api "repos/$repo/compare/$sha...$head" --jq '.status'); then
    note "could not compare run $run's commit with this build's; skipping its cache"
    return 1
  fi
  case "$status" in
    ahead | identical) return 0 ;;
  esac
  note "not using the cache from run $run: its commit $sha is not in this build's history ($status)"
  return 1
}

# Downloads artifact <art>, checks it holds nothing but cache entries, and
# moves them into the cache. Returns non-zero, having said why, when this
# artifact must not be used.
unpack() {
  local repo=$1 run=$2 art=$3 name=$4 cache=$5 work=$6 bad stage d status=1
  if ! gh api "repos/$repo/actions/artifacts/$art/zip" > "$work/artifact.zip"; then
    warn "could not download the cache from run $run"
    return 1
  fi
  if ! unzip -p "$work/artifact.zip" "$name.tar.zst" > "$work/cache.tar.zst" 2> /dev/null; then
    warn "the cache artifact from run $run holds no $name.tar.zst"
    return 1
  fi
  if ! zstd -dq "$work/cache.tar.zst" -o "$work/cache.tar"; then
    warn "the cache from run $run does not decompress"
    return 1
  fi
  # save writes regular files named like cache entries and nothing else, so
  # anything else (a path outside the cache, a link, a device) means this is
  # not an archive save wrote.
  if ! tar -tf "$work/cache.tar" > "$work/names" || ! tar -tvf "$work/cache.tar" > "$work/listing"; then
    warn "the cache archive from run $run cannot be listed"
    return 1
  fi
  bad=$(grep -Ev '^[0-9a-f]{2}/[0-9a-f]{64}-[ad]$' "$work/names" | sed -n 1p)
  if [ -n "$bad" ]; then
    warn "the cache archive from run $run holds '$bad'"
    return 1
  fi
  bad=$(cut -c1 "$work/listing" | grep -v '^-$' | sed -n 1p)
  if [ -n "$bad" ]; then
    warn "the cache archive from run $run holds something other than regular files"
    return 1
  fi

  # Unpacked next to the cache (so on the same file system) and moved in by
  # rename, so a restore cut short leaves whole entries behind, never part of
  # one. -m: the files get the time of unpacking; restore backdates them next.
  if ! mkdir -p "$cache" || ! stage=$(mktemp -d "$cache.restore.XXXXXX"); then
    warn "no room next to the Go build cache"
    return 1
  fi
  if tar -xmf "$work/cache.tar" -C "$stage"; then
    status=0
    for d in "$stage"/*/; do
      [ -d "$d" ] || continue
      d=${d%/}
      if ! mkdir -p "$cache/${d##*/}" || ! mv -f "$d"/* "$cache/${d##*/}/"; then
        status=1
        break
      fi
    done
  fi
  rm -rf "$stage"
  if [ "$status" -ne 0 ]; then
    warn "could not unpack the cache from run $run"
  fi
  return "$status"
}

save() {
  local upload=false
  if pack "$@"; then
    upload=true
  fi
  set_output upload "$upload"
}

# Prunes the cache to what this build used and, when that is worth uploading,
# writes <out>/<name>.tar.zst. Returns zero only when it wrote the archive.
pack() {
  local name=$1 out=$2 log=$3 own=$4 cache kept archive size own_re compiled outside
  local restored from_run
  read_state
  if ! cache=$(cache_dir) || [ -z "$cache" ] || [ ! -d "$cache" ]; then
    warn "there is no Go build cache to save"
    return 1
  fi

  # Still backdated means the build did not use it.
  (cd "$cache" && find . -mindepth 2 -maxdepth 2 -type f -path './[0-9a-f][0-9a-f]/*' \
    \( -name '*-a' -o -name '*-d' \) -mtime +1 -exec rm -f {} +)
  kept=$(count_steps "$cache")
  if [ "$kept" -eq 0 ]; then
    warn "the build left no cache entries; nothing to save"
    return 1
  fi

  if [ "$restored" = 1 ]; then
    if [ ! -r "$log" ]; then
      note "no build log at $log, so no telling what was compiled; uploading the cache to be safe"
    else
      # go build -v prints one import path per package it compiled; cached
      # packages are not printed. Compiler chatter has spaces, colons or a
      # leading "#", which an import path never has.
      own_re=$(printf '%s' "$own" | sed 's/[][\.*^$()+?{}|]/\\&/g')
      compiled=$(grep -cE '^[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*$' "$log" || true)
      outside=$(grep -E '^[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*$' "$log" | grep -cEv "^$own_re(/|\$)" || true)
      if [ "$outside" -eq 0 ]; then
        note "the build compiled $compiled package(s), all in $own; the cache from run $from_run is still current"
        summary "Go build cache: only $own was compiled, so the cache from run $from_run stays as it is."
        return 1
      fi
      note "the build compiled $outside package(s) outside $own"
    fi
  fi

  archive="$out/$name.tar.zst"
  if ! mkdir -p "$out"; then
    warn "cannot create $out; the cache is not saved"
    return 1
  fi
  if ! entries "$cache" | LC_ALL=C sort | (cd "$cache" && tar -cf - -T -) | zstd -q -T0 -3 -c > "$archive"; then
    warn "could not write the cache archive; the cache is not saved"
    rm -f "$archive"
    return 1
  fi
  size=$(($(wc -c < "$archive") / 1048576))
  set_output archive "$archive"
  note "saved $kept cached build steps (${size} MiB) for the next release"
  summary "Go build cache: saved $kept cached build steps (${size} MiB) for the next release."
  return 0
}

# After a failed build on a restored cache: empty the cache for a cold retry,
# and forget the restore, so that save uploads what the retry builds and the
# cache that failed stops being the newest.
discard() {
  local cache restored from_run
  read_state
  if [ "$restored" != 1 ]; then
    note "the build did not use a restored cache; nothing to discard"
    exit 1
  fi
  if cache=$(cache_dir) && [ -n "$cache" ] && [ -d "$cache" ]; then
    (cd "$cache" && find . -mindepth 2 -maxdepth 2 -type f -path './[0-9a-f][0-9a-f]/*' \
      \( -name '*-a' -o -name '*-d' \) -exec rm -f {} +)
  fi
  write_state 0 ""
  note "emptied the Go build cache restored from run $from_run"
}

check_name() {
  case "$1" in
    '' | *[!A-Za-z0-9._-]*) usage ;;
  esac
}

case "${1:-}" in
  restore)
    [ $# -eq 2 ] || usage
    check_name "$2"
    restore "$2"
    ;;
  save)
    [ $# -eq 5 ] || usage
    check_name "$2"
    save "$2" "$3" "$4" "$5"
    ;;
  discard)
    [ $# -eq 1 ] || usage
    discard
    ;;
  *) usage ;;
esac
exit 0
