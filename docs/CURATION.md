# Community Curation Hub

The **Packages** store lists everything the JukaLang registry offers. The
Curation Hub is the smaller, device-specific layer above it: one-press setup for
a new card, UI profiles tuned for each panel, and a way to say which games and
ports need attention.

Open **Start → Curation Hub**.

## Essentials bundle

The first page is a checklist, not a downloader. It fills the gaps that make a
handheld frustrating an hour later:

| Step | What it checks | What **A** does |
|------|----------------|-----------------|
| Save folders ready for sync | `save-manager/sync/folders.json` exists | writes `.stfolder`/`.stignore` and the Syncthing folder list |
| A save snapshot exists | at least one snapshot | takes the first snapshot of every save folder |
| RetroArch cores present | `RetroArch/.retroarch/cores` (or the detected path) | reports where cores are expected |
| Display profile | a theme has been chosen | applies the curated palette **only when none is set** |
| Save & suspend at 3% | `lowBatteryGuard` | turns the low-battery guard on if it was disabled |

**Apply essentials** runs the applicable steps in order. It never overwrites a
theme, text size or setting you chose yourself, and it is safe to run again: a
step that is already satisfied is reported as skipped. The bundle is picked for
the detected panel — `Smart Pro Essentials` on the 16:9 devices, `Compact
Essentials` on the Brick, Brick Pro and Hammer.

Steps that need data (save folders, cores) explain why they cannot run instead
of failing silently.

## UI & theme profiles

A profile is a theme plus a text scale, saved as plain JSON:

```json
{
  "schemaVersion": 1,
  "id": "smartpro-crisp",
  "name": "Smart Pro · Crisp",
  "author": "JukaHub",
  "device": "smartpro",
  "theme": "hub",
  "textScale": 1.0,
  "notes": "Neutral palette at 100% text for the 1280×720 panel."
}
```

**Capture this setup** saves the theme and text size currently on screen.
**Y** exports the focused profile to `data/curation/export/`, so it can be
posted in a sharing thread or copied to another card. To install one, drop the
JSON (a profile, an array, or `{"profiles":[…]}`) into
`data/curation/import/` and press **A** on **Import folder**.

Validation is deliberately strict about the parts that could break the UI:

- `theme` must be one of JukaHub's built-in theme keys (`hub`, `midnight`,
  `oled`, `matrix`, `highcontrast`, …);
- `textScale` is clamped to 85–145%, the same range as Accessibility;
- a profile without a name is skipped, with the file named on the panel.

A profile tuned for another panel is allowed, with a note in the confirmation,
because theme and text size are portable. Only profiles saved in the hub's own
folder can be deleted; built-ins and imports stay put.

## Compatibility votes

**Compatibility votes** turns the ROM library and installed PortMaster titles
into a vote list. The ROM walk runs in the background; ports are listed first
while it finishes.

- **A** toggles a vote for the focused game or port.
- **Submit votes** writes `data/curation/export/votes-<date>.json` and, when a
  collector is configured, POSTs the same JSON to it.

Votes are stored locally in `data/curation/votes.json`, so they survive a reboot
and can be edited by hand. A build with no collector says so instead of
pretending to send anything; the exported file is the submission. The POST body
looks like:

```json
{
  "schemaVersion": 1,
  "device": "smartpro",
  "platform": "trimui-linux",
  "version": "v0.4.0",
  "generatedAt": "2026-10-21T18:20:00Z",
  "votes": [
    {"kind": "game", "key": "snes|super mario world", "title": "Super Mario World", "system": "SNES", "at": "2026-10-21T18:19:00Z"},
    {"kind": "port", "key": "portal.sh", "title": "Portal", "system": "PortMaster", "at": "2026-10-21T18:19:30Z"}
  ]
}
```

## Community feed

A remote feed can add bundles and profiles without an app update. Configure it
at build time:

```sh
go build -ldflags "-X main.curationFeedEndpoint=https://example.org/curation.json" .
```

or at runtime with `JUKAHUB_CURATION_FEED`. A feed is a JSON document:

```json
{
  "schemaVersion": 1,
  "updated": "2026-10-21",
  "bundles": [
    {
      "id": "retro-safety",
      "name": "Retro safety",
      "device": "any",
      "summary": "Save safety on any panel.",
      "steps": [{"kind": "saveSync"}, {"kind": "saveSnapshot"}]
    }
  ],
  "profiles": [
    {"id": "community-oled", "name": "Community OLED", "theme": "oled", "textScale": 1.15, "device": "smartpro"}
  ]
}
```

Allowed step kinds are `saveSync`, `saveSnapshot`, `cores`, `display` and
`battery`; a feed cannot ask the hub to write arbitrary files. Built-in bundles
and local profile files win over a feed with the same id, and a newer
`schemaVersion` is refused rather than half-read. With no feed configured the
page says **Built-in only** and everything else still works.

The vote collector uses the same pattern:
`-X main.curationVotesEndpoint=…` or `JUKAHUB_CURATION_VOTES_URL`.

## Where the data lives

| Path | Contents |
|------|----------|
| `Apps/JukaHub/data/curation/profiles/` | profiles saved from this device |
| `Apps/JukaHub/data/curation/import/` | profiles or bundles copied from elsewhere |
| `Apps/JukaHub/data/curation/export/` | shared profiles and vote bundles |
| `Apps/JukaHub/data/curation/votes.json` | local compatibility votes |
| `Apps/JukaHub/data/curation/README.txt` | the same layout, on the card |

On Windows the data folder is under `%APPDATA%\JukaHub`. Nothing here is
uploaded unless a feed or collector is configured in the build.

## Controls

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through rows |
| L1 / R1 | Page by five rows |
| A | Open, apply, capture or toggle a vote |
| X | Delete a saved profile (Profiles page) |
| Y | Export the focused profile, or submit votes |
| B / Esc | Back one page, then close |

Tapping a row activates it; tapping outside closes the panel.

## Limits

- The hub curates settings, not binaries. Shader packs, overlays and extra cores
  still come from RetroArch, PortMaster or the Packages store; the essentials
  checklist tells you when a core folder is missing instead of downloading one.
- Votes are one per target per device. They are a priority signal, not a review
  score, and nothing is sent from a build without a collector.
- Profiles carry theme and text size only. Wallpapers, element geometry and
  per-scene colours stay out of the exchange so a shared file cannot rearrange
  another person's home screen.
