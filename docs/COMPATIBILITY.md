# Performance & Compatibility Intelligence

The A133P in the Smart Pro is comfortable with PS1 and lighter systems. N64,
Dreamcast and PSP are game-dependent: some titles hold full speed, others need
frameskip, a lower render size or a different core. JukaHub's compatibility
panel turns that guesswork into three answers:

* **What is known about this ROM** — a community report, or an honest hardware
  tier when nobody has reported it yet.
* **What to try** — a one-press boost plan with concrete emulator settings.
* **Which core build to use** — the build JukaHub recommends and the builds that
  are actually installed on the device.

Open it from **Emulators → Smart Pro Compatibility**. Nothing on this screen
launches a game or changes a ROM; it prepares the emulator and records what it
did.

## The report catalog

Reports live in the app data folder:

```text
data/performance/
├── catalog.json      starter catalog (copied once, never overwritten)
├── import/*.json     community packs, loaded automatically
├── local/*.json      your overrides; a matching id wins
├── boost/*.json      recipes written by the panel
└── cores/            optional core-build pool
```

On the handheld that is `Apps/JukaHub/data/performance`. Drop a pack in
`import/` and press **Y** in the panel (or reopen it) — no restart is needed.

A file can hold one report:

```json
{
  "schemaVersion": 1,
  "id": "n64-goldeneye-007",
  "title": "GoldenEye 007",
  "system": "Nintendo 64",
  "aliases": ["Golden Eye"],
  "status": "performance-issues",
  "verifiedOnSmartPro": false,
  "core": "mupen64plus_next",
  "coreVersion": "2.5.9",
  "notes": "Four-player split-screen drops frames.",
  "settings": {
    "frameSkip": 1,
    "resolutionScale": 75,
    "audioQuality": "low",
    "retroArch": {
      "mupen64plus-169screensize": "640x360"
    }
  }
}
```

…or a bundle with a shared header:

```json
{
  "schemaVersion": 1,
  "updated": "2026-10-20",
  "entries": [
    { "id": "ps1-final-fantasy-vii", "title": "Final Fantasy VII", "system": "PlayStation", "status": "playable" }
  ]
}
```

Only the fields a report needs are required. Missing settings fall back to the
platform tier.

### Status values

| `status` | Meaning in the panel |
| --- | --- |
| `playable` | Runs at the baseline; no boost is offered first. |
| `audio-issues` | Sound crackles or drops; lower audio quality is suggested. |
| `performance-issues` | Slowdowns; frameskip and/or a lower render size are suggested. |
| `unplayable` | A report says it does not work; the plan is a best-effort experiment. |
| `unknown` | No verdict; the hardware tier is shown instead. |

`verifiedOnSmartPro` is a claim, not an automatic promotion. Set it to `true`
only when the game was actually tested on this hardware. The panel shows
**VERIFIED ON SMART PRO** for those reports. Without a report, PS1 and lighter
systems carry the **SMART PRO TIER** badge; N64, Dreamcast and PSP carry
**GAME-DEPENDENT** until a community report says otherwise.

### Matching

A ROM matches on its **system** plus its **normalised title**. Normalisation
strips the file extension, region/revision tags such as `(USA)` or `[!]`, and
punctuation. Alternate names must be listed in `aliases`; the panel does not
guess with fuzzy matching, so a report for the wrong game is never applied by
accident.

The systems scanned from `Roms/<system>/` are the same ones JukaHub's ROM
library uses: NES, SNES, Game Boy, GBC, GBA, Mega Drive, PlayStation, Nintendo
64, Saturn, Dreamcast, PSP and Nintendo DS.

## Boost plans

A plan is built from the report when one exists, otherwise from the hardware
tier. It contains a frameskip, a render scale and an audio quality, plus the
documented RetroArch core options for the recommended core. The panel writes:

1. `boost/<key>.json` — a readable recipe with the settings, core, ROM path and
   timestamp. This is always written, even when no emulator config is found.
2. When RetroArch's config folder exists, `config/<Core>/<Game>.opt` — a
   per-game core-option override. It is applied on the next load of that ROM.

JukaHub never edits a global emulator config. The override starts with a
`# JukaHub boost mode` header; **X** removes only overrides with that header and
the matching recipe, leaving a user-authored `.opt` alone.

The core options use libretro's documented keys, for example:

| Core | Option |
| --- | --- |
| PPSSPP | `ppsspp_frameskip`, `ppsspp_auto_frameskip`, `ppsspp_internal_resolution` |
| Flycast | `flycast_frame_skipping`, `flycast_skip_frame`, `flycast_internal_resolution`, `flycast_threaded_rendering` |
| Mupen64Plus-Next | `mupen64plus-169screensize`, `mupen64plus-43screensize` |
| PCSX-ReARMed | `pcsx_rearmed_frameskip` |

A report can add or correct keys in `settings.retroArch`; the panel treats the
catalog as the source of truth. Frameskip is only emitted for cores that expose
it (PPSSPP, Flycast and PCSX-ReARMed); N64 and DS plans lower the render size
instead of promising a setting their core does not have. The default plans are
deliberately conservative and never touch a setting known to break a game
outright.

**A** applies the selected game's plan. **X** reverts it. The recipe is
reviewable, so a user can see exactly what changed and copy it to another card.

## Core-build manager

The second tab lists the cores JukaHub can see:

* **ACTIVE** — a core binary in RetroArch's `cores/` folder.
* **POOL** — an extra build in `data/performance/cores/<core>/<version>/`.

The pool layout is intentionally simple:

```text
data/performance/cores/
└── ppsspp/
    ├── 1.17.1/ppsspp_libretro.so
    └── 1.18.0/ppsspp_libretro.so
```

Press **A** on a pool build to activate it. Close RetroArch first; JukaHub is
not the process that owns its loaded cores. JukaHub copies the current active
binary to `<name>.jukahub-<timestamp>.bak` beside it and installs the selected
build as `<core>_libretro.so` (or `.dll` on Windows). The previous core is never
deleted.

Each game's detail pane names the core its report was written for and whether
that core is **active**, **outdated**, only in the **pool**, or **missing**. A
recommendation therefore never points at a core the user does not have.

### Fetching newer builds

**U** (or **L1**) on the Core-builds tab checks the libretro aarch64 buildbot
and fills the pool with copies of any installed core whose build differs from
the one the index lists. The comparison is the CRC32 the index publishes, so a
core that already matches is left alone, and a download that does not match the
promised checksum is thrown away instead of installed.

Fetched builds land in `data/performance/cores/<core>/<date>/`, next to a
`source.json` that records the URL, checksum and fetch time. **Activating one is
still a separate press** in the same tab: the download can never replace the core
RetroArch is using, and activation keeps the previous binary as a `.bak`. This
channel is nightly, not stable — the buildbot publishes no stable aarch64 tree —
so treat a fetched build as a candidate to try, not as a guaranteed upgrade.

Standalone emulators (PPSSPP, mGBA, Flycast) are looked up separately, because
their upstream releases target desktop Linux rather than this device's userspace
and launcher scripts. When a release publishes an aarch64 archive it is fetched
to `data/emulator-updates/<app>/<tag>/` and reported, **not** unpacked over the
installed emulator: switching the launcher stays a manual job. Releases without
an aarch64 asset are reported as such rather than offering a desktop build.

## Controls

| Input | Games tab | Core-builds tab |
| --- | --- | --- |
| Up/Down | Move the selected game | Move the selected build |
| Left/Right or Tab | Switch tab | Switch tab |
| A | Apply the boost plan | Activate a pool build |
| X | Revert the selected plan | — |
| Y / R | Rescan ROMs, catalog and cores | Rescan |
| U / L1 | — | Check the buildbot for newer core builds |
| B / Esc / Backspace | Back to Emulators | Back to Emulators |

Mouse and touch select rows and tabs in the same way.

## Limits

* The catalog is community data. A report is a starting point, not a promise;
  revisions of a ROM can behave differently.
* The panel reads ROM names and the core folders. It does not launch games, so
  the boost override applies the next time the game is opened in RetroArch.
* Reports never execute code and are size-limited. Only import files you trust;
  a malformed report is skipped and reported instead of hiding the rest of a
  pack.
* The Smart Pro Tier is a hardware expectation based on the A133P and 1 GB of
  RAM. N64, Dreamcast, PSP and DS remain game-dependent regardless of the badge.
