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
# a job's `container: image:`, and an image a step starts itself, which the
# workflow names in an env key ending in _IMAGE (docker-image.yml's
# TOOLCHAIN_IMAGE, which .github/scripts/trimui-build.sh also refuses unless it
# carries a digest).
unpinned_images=$(grep -nHE '^[[:space:]]*(image|[A-Z0-9_]*_IMAGE):[[:space:]]+[^[:space:]#]+' "${files[@]}" \
  | tr -d '\r' \
  | grep -vE '@sha256:[0-9a-f]{64}([[:space:]]+#.*)?$' || true)
if [ -n "$unpinned_images" ]; then
  echo "These container images are referenced by tag, not by digest:"
  echo "$unpinned_images"
  echo "Pin each to @sha256:<digest> and keep the tag in the same reference."
  exit 1
fi

# A pinned action still runs code this repository did not write, and so does a
# download without a digest. Three archive fetches in nightly.yml sat unverified
# until a manual pass found them, which is the argument for a check rather than
# a habit. The digest may sit on the fetch line or up to two lines below it; a
# fetch that publishes no digest says so with `# download-pin-ok: <reason>`.
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

echo "All workflow actions are pinned by commit."
echo "All workflow container images are pinned by digest."
echo "All workflow downloads are checksum-verified."
