# The updater and TUF — design note (not yet adopted)

State: **documented, intentionally not implemented.** This page records how the
self-updater is protected today, what [The Update Framework](https://theupdateframework.io/)
(TUF) would add, when to reach for it, and the smallest slices if we do.
The working roadmap row is item 38 in [ROADMAP.md](../ROADMAP.md).

## Where the updater stands today

The update pipeline (`player/autoupdate.go`, `player/patch_flow.go`,
`player/patch_swap.go`) already has several of the layers TUF assumes you need:

- **Outer-manifest signing** — the outer manifest (`internal/releasesign`) is
  Ed25519-signed with a key id and a **validity window**; `Verify` rejects
  tampered payloads *and* stale manifests, so there is already a simple
  freshness check against replaying old-but-signed manifests.
- **Transactional swap** — staged into a side directory, verified, swapped, then
  journalled; a journal that records an interrupted swap drives rollback on the
  next boot.
- **Rollback under our control** — the newest backup set holds the previous
  binary; the recovery chord (L+R+Start) or the boot guard (three consecutive
  fast failed boots inside a 72 h update window) restores it
  (`player/safeboot.go`).
- **Preflight checks** — signature, free space, write permissions before the
  swap starts.

## What TUF adds on top

TUF is not a download protocol; it is *metadata about metadata*. Four roles
with separated keys:

| Role | Guards against |
|------|----------------|
| root | Compromise of any single other key (offline root key, rotation) |
| targets | Compromise of the file-server (targets hashes/sizes live in signed metadata, not on the server) |
| snapshot | Mixed-version attacks (a consistent set of files) |
| timestamp | Freeze attacks (fresh signed expiring metadata) |

Our current scheme signs each manifest with one key and a fixed validity
window. That is enough against tampered payloads and naive replay; it is
weaker against a compromised *server* that keeps serving the newest
still-valid old build indefinitely while the window runs, single-key exposure
with no rotation path, and separately signed artifacts that do not form a
consistent set (no snapshot role). TUF turns those properties into signed,
expiring, rotatable, role-separated metadata.

## Trigger for adopting (not "when we have time")

Reach for TUF when one of these becomes true:

1. An **external mirror** ships packages (mirrors then carry metadata, not new
   trust), or
2. The release key is held anywhere a compromise is plausible, or
3. The SD-card/`release.sh` path acquires a second publisher (key rotation
   becomes necessary to add one safely).

Until then, re-verifying every boot with go-tuf would add a dependency and a
metadata-publishing workflow to a multimedia handheld app with no attack model
changed — cost without payoff.

## If adopted: smallest slices in order

1. **Verification only.** `build-patch-repo` emits role metadata; the on-device
   verifier trusts a pinned root, checks targets hashes/sizes and then
   freshness. The existing Ed25519 package signatures stay (defence in depth),
   and the offline SD-card install path keeps working with *no network*: TUF verification of a local copy must never
   require live fresh timestamp metadata — that has to stay explicitly true,
   because the SD-card flow is the recovery path for a device that cannot
   update normally.
2. **Key rotation drill.** Document and actually exercise root rotation once
   with test keys before any production adoption is called done. An updatable
   root is the entire point.
3. **Timestamp/snapshot freshness** for online update checks only
   (`JUKAHUB_NO_AUTOUPDATE` environments keep slice 1 behaviour).

Deliberately out of scope at every stage: full TUF rollout for plugins and
patches (they reuse the package verifier and nothing else), and any CLI-facing
`go-tuf` churn — the app is the only client.

## Risks the note is honest about

- go-tuf API churn and CGO footprint have to fit a `player/` build that already
  vendors SDL2; measure before relying on it.
- Metadata expiry on a handheld with no RTC and no network: TTL checks must
  degrade to "verify, warn" offline, mirroring the clock-reset case the boot
  guard already handles.
- Two competing trust systems (our sigs, TUF targets) is more code to explain
  in [SECURITY.md](../SECURITY.md); only adopt if a row above triggers, and
  then state a retirement plan for the old signatures.
