# External dependencies: what is pinned, what floats, and how to roll back

JukaHub ships as one binary, but it stands on things it does not contain: the Go
toolchain that builds it, SDL2 and its siblings, the helper tools it downloads
at runtime, and the signed packages users install. This page records which of
those are **pinned**, which are **resolved to the latest release**, and what to
do when one of them changes underneath the project.

The rule behind the split: build inputs are pinned because a rebuild must
produce the same binary; runtime helpers float because their whole value is
tracking upstream (a stale `yt-dlp` stops working when a site changes); packages
are signed because they run with the app's privileges.

## 1. Pinned at build time

| Dependency | Pinned where | Notes |
|---|---|---|
| Go toolchain | `GO_FALLBACK` / `GO_MIN_MINOR` at the top of [player/build.bat](../player/build.bat) | `build.bat` installs that toolchain into `%LOCALAPPDATA%\JukaHub\tools` when the machine has nothing newer |
| w64devkit (GCC) | `W64DK_VER` in the same block | Needed for cgo; the 64-bit build is mandatory |
| SDL2 / SDL2_image / SDL2_ttf | `SDL2_VER`, `IMG_VER`, `TTF_VER` in the same block | Extracted into `player/.sdl2/x86_64-w64-mingw32`, so a version bump is "change the constant, delete that directory, rebuild" |
| Go modules | `player/go.mod` + `go.sum` | Dependabot keeps these current; `go.sum` is the integrity check |
| Linux libraries | the distribution packages listed in [BUILDING.md](BUILDING.md) | Not bundled on desktop |

Bumping a pinned input is a normal PR: change the constant, delete the local
`.sdl2` copy, run `player/build.bat nolaunch`, then `make check`. A render-loop
or startup change also wants the cold-boot benchmark
(`player/startup-bench.sh`) because the splash and first-frame budget are tuned
to these versions.

## 2. Resolved to the latest release at runtime

The app downloads a few tools when they are missing or the user asks for an
update. These deliberately track upstream:

| Tool | Source | Where it lands |
|---|---|---|
| `yt-dlp` | `yt-dlp/yt-dlp` latest release | the app's tools directory |
| `ffmpeg` / `ffprobe` | `btbn/ffmpeg-builds` latest release | the app's tools directory |
| Standalone emulator updates | an allowlist of emulator repositories (`player/emulatorupdates.go`) | next to the emulator install |
| Windows runtime (Wine/Hangover, DXVK) | `AndreRH/hangover`, `doitsujin/dxvk` latest release | the Windows-apps runtime directory |
| PortMaster | `PortsMaster/PortMaster-GUI` release asset | the ports directory |
| Toolbox tools (`rclone`, `jq`, `ripgrep`, `fd`, `pandoc`, `age`, `naabu`, `httpx`, `dnsx`) | each project's latest release, from `player/toolbox.go` | the app's tools directory, only when the user installs one; the three scanners also need a one-time acknowledgement before their first run |

Two guardrails apply to all of them:

1. **An allowlist, not a free-form repo argument.** `resolveToolURL` in
   [player/tools.go](../player/tools.go) refuses any repository outside
   `allowedToolRepos`, because the GitHub API can be used to probe release
   metadata and a future code path might over-trust the returned URL.
2. **Asset-name matching, pinned by tests.** A release asset is chosen by name
   (`yt_dlp_asset_test.go`, `install_tools_test.go`), so an upstream rename
   fails a unit test instead of producing a download error on a device. The
   toolbox matches on the stable parts of the name rather than a full file name
   (versions change every release), skips checksum/signature side-cars, and is
   pinned the same way (`toolbox_test.go`).

There is **no published-digest check** on these downloads today. Integrity rests
on HTTPS to github.com plus the allowlist, and the *result* is recorded: the
component report and the diagnostics bundle carry the SHA-256 of every installed
helper, so a bug report names the exact build. [UPDATER_TUF.md](UPDATER_TUF.md)
records what a stronger chain would add and which events would justify building
it; until then, do not describe helper downloads as signature-verified.

## 3. Signed packages

Packages installed through Patch are a different class: their manifests carry
Ed25519 signatures and per-file digests, installation happens through a
transactional engine ([player/patch_resolve.go](../player/patch_resolve.go))
with a backup set, and a failed transaction rolls back. `docs/PACKAGES.md`
documents the format; the repository index is refreshed through the same
verification.

## When upstream changes break something

Symptoms usually arrive as a runtime failure, not a build failure: searches or
downloads stop working (`yt-dlp`), a video plays without audio (`ffmpeg`), or an
emulator update reports no matching asset.

1. **Read the diagnostics bundle first** (Patch → Export diagnostics). It carries
   the helper digests, the last few hundred log lines and the paths that were
   probed, which usually distinguishes "upstream rename" from "device problem".
2. **Reinstall the helper** (`Patch → Verify & repair` installs missing tools).
   A failed download leaves the old tool in place; the app falls back to the
   system `PATH`.
3. **If the asset name changed**, update the wanted names in `player/tools.go`
   and its test — that is the intended fix, and the test is what makes it a
   reviewable change.
4. **If the tool's interface changed**, pin a version by changing the wanted
   asset name to the versioned asset upstream publishes, and add the new
   behaviour behind a capability check rather than a version string.

Rollback is cheap by construction: pinned build inputs roll back by reverting
the constant (and deleting `.sdl2`), packages roll back through Patch's
transactions, and helpers are re-downloaded or replaced with a system build.

## What is not covered yet

- **No upstream version watcher.** Dependabot covers Go modules, npm and GitHub
  Actions, not the GitHub-release assets above. A helper that breaks is noticed
  by users or by the test that pins its asset name.
- **No offline mirror.** Every download is from github.com at install time; a
  device that has never been online cannot install helpers or toolbox tools.
- **No upstream hash check on helper or toolbox downloads** (see above);
  [TOOLBOX.md](TOOLBOX.md#what-it-verifies-and-what-it-does-not) states the gap
  next to the feature it affects.
- **TUF metadata** is designed but not implemented — see
  [UPDATER_TUF.md](UPDATER_TUF.md) for the triggers that would start it.
