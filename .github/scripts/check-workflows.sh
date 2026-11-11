#!/usr/bin/env bash
# The pinning rules CI enforces on its own workflow files.
#
#   * every action is a full commit SHA, not a tag;
#   * every container image is pulled by digest, not by tag;
#   * every downloaded archive is checksum-verified.
#
# These live in a script rather than in a `run:` block so `make check` exercises
# exactly what CI runs: a workflow edit that breaks a rule now fails before the
# push instead of after it. It checks the workflows of the repository it lives
# in, wherever it is invoked from.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

shopt -s nullglob
files=(.github/workflows/*.yml .github/workflows/*.yaml)
shopt -u nullglob
if [ "${#files[@]}" -eq 0 ]; then
  echo "check-workflows: no workflow files under $root/.github/workflows" >&2
  exit 2
fi

# A tag is mutable: `uses: owner/action@v7` lets the code CI runs change without
# a commit in this repository. Local actions (./...) are this repo's own code,
# and docker:// references are covered by the image digest check below.
unpinned=$(grep -nHE '^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+[^[:space:]#]+' "${files[@]}" \
  | tr -d '\r' \
  | grep -vE 'uses:[[:space:]]+(\./|docker://)' \
  | grep -vE '@[0-9a-f]{40}([[:space:]]+#.*)?$' || true)
if [ -n "$unpinned" ]; then
  echo "These actions are referenced by tag, not by commit SHA:"
  echo "$unpinned"
  echo "Pin each to a 40-character commit and keep the release in a trailing comment."
  exit 1
fi

# A container image is as mutable as an action tag unless it is pulled by digest:
# a job's `container: image:` (or the one-line `container: <image>`), a
# `uses: docker://<image>` step, and an image a step starts itself, which the
# workflow names in an env key ending in _IMAGE (docker-image.yml's
# TOOLCHAIN_IMAGE, which .github/scripts/trimui-build.sh also refuses unless it
# carries a digest). The value may be quoted.
unpinned_images=$(grep -nHE '^[[:space:]]*((-[[:space:]]+)?image|container|[A-Z0-9_]*_IMAGE):[[:space:]]+[^[:space:]#]+|^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+["'"'"']?docker://' "${files[@]}" \
  | tr -d '\r' \
  | grep -vE '@sha256:[0-9a-f]{64}["'"'"']?([[:space:]]+#.*)?$' || true)
if [ -n "$unpinned_images" ]; then
  echo "These container images are referenced by tag, not by digest:"
  echo "$unpinned_images"
  echo "Pin each to @sha256:<digest> and keep the tag in the same reference."
  exit 1
fi

# A pinned action still runs code this repository did not write, and so does a
# download without a digest. Three archive fetches in the old nightly.yml sat
# unverified until a manual pass found them, which is the argument for a check
# rather than a habit. The digest may sit on the fetch line or up to two lines
# below it; a fetch that publishes no digest says so with
# `# download-pin-ok: <reason>`.
unverified=$(awk '
  BEGIN { total = 0; n = 0 }
  {
    # Every line takes a position, comments included, so the window below counts
    # lines rather than statements. The waiver marker may be a comment or a
    # trailing note on the fetch itself.
    total++
    if ($0 ~ /download-pin-ok/) waived_at[total] = 1
    if ($0 ~ /^[[:space:]]*#/) next
    if ($0 ~ /(curl|wget)/ && $0 ~ /https?:\/\//) {
      n++
      at[n] = total
      where[n] = FILENAME ":" FNR
      text[n] = $0
      next
    }
    if ($0 ~ /(sha256sum -c|shasum -a 256)/) verified_at[total] = 1
  }
  END {
    bad = 0
    for (i = 1; i <= n; i++) {
      ok = 0
      for (j = at[i]; j <= at[i] + 2; j++) if (j in verified_at) ok = 1
      for (j = at[i] - 1; j <= at[i] + 2; j++) if (j in waived_at) ok = 1
      if (!ok) {
        print "  " where[i] ": " text[i]
        bad = 1
      }
    }
    exit bad
  }
' "${files[@]}" || true)
if [ -n "$unverified" ]; then
  echo "These workflow downloads are not checksum-verified:"
  echo "$unverified"
  echo "Verify each with sha256sum -c - (see docker-image.yml), or mark it"
  echo "with '# download-pin-ok: <reason>' when the fetch publishes no digest."
  exit 1
fi

# A workflow of this repository that another one calls (`uses: ./.github/...`)
# gets at most the permissions its caller job grants, and when one of its jobs
# asks for more, GitHub refuses the whole calling run before it starts rather
# than downgrading: a release run that never begins. So every such caller job
# must grant (itself, or through its workflow's top level) each permission the
# called workflow's jobs ask for.
#
# The parser reads the layout these files use (two-space indents, jobs at two,
# their keys at four, a permissions entry per line); it prints
#   TOP <name> <level> | TOPALL <read-all|write-all|{}>
#   JOB <job> | USES <job> <path> | PERMS <job> | PERM <job> <name> <level> | ALL <job> <value>
permission_lines() {
  tr -d '\r' < "$1" | awk '
    function entry(line, indent,    v, n) {
      v = line
      sub("^" indent, "", v)
      sub(/[[:space:]]+#.*$/, "", v)
      n = index(v, ":")
      if (n == 0) return ""
      name = substr(v, 1, n - 1)
      level = substr(v, n + 1)
      gsub(/[^a-z-]/, "", level)
      return name " " level
    }
    /^[[:space:]]*(#|$)/ { next }
    {
      match($0, /^ */)
      ind = RLENGTH
    }
    ind == 0 {
      block = ""
      in_jobs = ($0 ~ /^jobs:/)
      if ($0 ~ /^permissions:/) {
        v = $0
        sub(/^permissions:[[:space:]]*/, "", v)
        sub(/[[:space:]]+#.*$/, "", v)
        if (v != "") print "TOPALL " v
        else block = "top"
      }
      next
    }
    block == "top" && ind == 2 { if ((e = entry($0, "  ")) != "") print "TOP " e; next }
    in_jobs && ind == 2 && $0 ~ /^  [A-Za-z0-9_-]+:[[:space:]]*(#.*)?$/ {
      job = $0
      sub(/:.*/, "", job)
      gsub(/[[:space:]]/, "", job)
      print "JOB " job
      block = ""
      next
    }
    in_jobs && job != "" && ind == 4 {
      block = ""
      if ($0 ~ /^    uses:[[:space:]]+\.\//) {
        v = $0
        sub(/^    uses:[[:space:]]+/, "", v)
        sub(/[[:space:]]+#.*$/, "", v)
        print "USES " job " " v
      } else if ($0 ~ /^    permissions:/) {
        v = $0
        sub(/^    permissions:[[:space:]]*/, "", v)
        sub(/[[:space:]]+#.*$/, "", v)
        if (v != "") print "ALL " job " " v
        else { block = "job"; print "PERMS " job }
      }
      next
    }
    block == "job" && ind == 6 { if ((e = entry($0, "      ")) != "") print "PERM " job " " e; next }
    ind < 6 { block = "" }
  '
}

level_rank() {
  case "$1" in
    write | write-all) echo 2 ;;
    read | read-all) echo 1 ;;
    *) echo 0 ;;
  esac
}

# granted <lines> <job> <permission>: the level that job gets.
granted() {
  local lines=$1 job=$2 perm=$3 all
  if printf '%s\n' "$lines" | grep -qx "PERMS $job"; then
    printf '%s\n' "$lines" | awk -v j="$job" -v p="$perm" '$1 == "PERM" && $2 == j && $3 == p { print $4 }'
    return
  fi
  all=$(printf '%s\n' "$lines" | awk -v j="$job" '$1 == "ALL" && $2 == j { print $3 }')
  if [ -z "$all" ]; then
    if printf '%s\n' "$lines" | grep -q '^TOP '; then
      printf '%s\n' "$lines" | awk -v p="$perm" '$1 == "TOP" && $2 == p { print $3 }'
      return
    fi
    all=$(printf '%s\n' "$lines" | awk '$1 == "TOPALL" { print $2 }')
  fi
  case "$all" in read-all) echo read ;; write-all) echo write ;; esac
}

short_grants=""
for file in "${files[@]}"; do
  caller=$(permission_lines "$file")
  while read -r _ job path; do
    called="$root/${path#./}"
    if [ ! -f "$called" ]; then
      short_grants="$short_grants
  $file: job $job calls $path, which does not exist"
      continue
    fi
    lines=$(permission_lines "$called")
    # What the called workflow's jobs ask for: each job's own block, or the
    # called file's top level for a job without one.
    asks=$(for cjob in $(printf '%s\n' "$lines" | awk '$1 == "JOB" { print $2 }'); do
      for perm in actions attestations checks contents deployments discussions id-token issues \
        models packages pages pull-requests repository-projects security-events statuses; do
        level=$(granted "$lines" "$cjob" "$perm")
        if [ "$(level_rank "$level")" -gt 0 ]; then echo "$perm $level $cjob"; fi
      done
    done)
    while read -r perm level cjob; do
      [ -n "${perm:-}" ] || continue
      have=$(granted "$caller" "$job" "$perm")
      if [ "$(level_rank "$have")" -lt "$(level_rank "$level")" ]; then
        short_grants="$short_grants
  $file: job $job grants $perm: ${have:-none}, but $path job $cjob asks for $level"
      fi
    done <<< "$asks"
  done < <(printf '%s\n' "$caller" | grep '^USES ' || true)
done
if [ -n "$short_grants" ]; then
  echo "These calls to a workflow of this repository grant less than it asks for,"
  echo "which makes GitHub refuse the calling run before it starts:$short_grants"
  echo "Grant each permission in the calling job's own permissions: block."
  exit 1
fi

echo "All workflow actions are pinned by commit."
echo "All workflow container images are pinned by digest."
echo "All workflow downloads are checksum-verified."
echo "Every call to a workflow of this repository grants what it asks for."
