# Feature backlog: the gaps, with the honest design

For current progress and the improvement priorities selected from the detailed
plans, see [PROJECT_STATUS.md](PROJECT_STATUS.md). This page is a design
reference for unadopted feature ideas, not a list of work in progress.

What is **not** built yet, and why the obvious version of it often cannot work on
this device. The items that used to be listed here as shipped have left this
page: the shipped register is
[MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25),
and accepted-but-unbuilt work is §5.6 there. An idea is tracked in exactly one
place, so this page holds only ideas that are neither shipped nor adopted.

Each section names what already exists, what the missing half would need, and
which part cannot work. Ideas with no design sketch at all live in
[FEATURE-SLATE.md](FEATURE-SLATE.md).

## 1. In-game OSD overlay preset and hotkeys

JukaHub cannot draw over an emulator: the emulator owns the framebuffer and
JukaHub is blocked waiting for it to exit, so an "in-game OSD from the hub"
would never appear. The working design is to configure the emulator's own OSD,
and the display half of that now ships: **Settings → Smart Widgets → In-game
OSD** cycles *leave as is / quiet / notifications / notifications + stats*, which
writes `video_font_enable`, `menu_enable_widgets`, `fps_show`,
`framecount_show` and `memory_show` to `retroarch.cfg` just before a RetroArch
launch — the same line-based edit with a one-time
`retroarch.cfg.jukahub-backup` the save-state switches use
([osdpreset.go](../player/osdpreset.go)). The recording half already ships
(RetroArch's `recording_output_directory` → the **Recordings** screen; see
[RECORDINGS.md](RECORDINGS.md) for why a hub-drawn overlay cannot exist).

Still open, and deliberately so: the hotkey bindings (`input_menu_toggle`,
`input_screenshot`, `input_toggle_fast_forward`, plus the enable-hotkey
modifier). The binds are numeric button codes, and a guess breaks controls on a
device whose mapping nobody measured, so the manager never writes an `input_*`
key — a test fails if one appears. What is left is a measured profile per
device, not more plumbing.

## 2. Simple mode with a PIN

- A persisted `simpleMode` switch that renders only an allowlist of scenes (Home,
  Media, Games, Settings → Simple) and hides the scene-switching chrome.
- Leaving needs a PIN typed on the on-screen keyboard; the PIN is stored hashed
  with the existing secret store, not in plain text in the config.
- It is a *focus* mode, not security: the SD card can be edited from a PC. Say so
  in the settings row so nobody trusts it as parental control.

Nothing of this exists; `safemode.go` is a different thing (it skips plugins,
updates and network work after a failed boot, with no PIN).

## 3. Box art: the quality of the match

The fetch, the per-title not-found cache and the completeness ring are shipped,
and so is the title-normalisation half of the matching problem: the lookup now
also tries the same title with its accent folded, its Roman numeral written as a
number (and back), `&` for `and`, a colon where the folder used a dash, and no
trailing period, plus a small alias table for names that differ by more than
spelling (`Zelda II` → `Zelda II - The Adventure of Link`).

What is still open is the manual half: a "choose image" action for the titles
normalisation cannot rescue, and an alias editor so a user can add their own
pair without a rebuild. Neither needs a new downloader.

Descriptions, genres, release dates and screenshots are a separate source
problem: libretro only publishes images, so that data needs a provider with an
account or key (ScreenScraper, TheGamesDB) behind the same downloader.

## 4. Checkpoints / quick switch

- Shipped: auto-save on exit and auto-load on the next launch, through
  RetroArch's own state switches ([GAME_LIBRARY.md](GAME_LIBRARY.md#resume-and-auto-save)).
- Open: named checkpoints and a real switcher (suspend one game, start another,
  come back). That needs two suspended emulator processes on a 1 GB device, and
  named per-game save slots need core-specific state handling.

The design options, the hard constraints and a phased plan are in
[CHECKPOINTS.md](CHECKPOINTS.md), which is the milestone's home and is explicitly
unimplemented. The plan tracks it as adopted work at
[MODERNIZATION §5.6](../player/MODERNIZATION.md#56-adopted-not-started).

## 5. Per-emulator rumble, and a vibration intensity step

Haptics ship: navigation, confirm, back, toggle and toast pulses drive the TrimUI
vendor GPIO motor (SDL rumble for an external pad), and the Vibration switch only
appears when a motor was found. Two pieces are still open:

- an intensity step, so the motor can be quieter at night;
- per-emulator rumble — a core runs in another process, so that needs a hook the
  launchers can call, not a change in the hub's own input path.

## 6. Curated storefront over the package mechanism

The mechanism is done: signed packages install through the transactional Patch
engine, with digest and signature checks and a rollback copy (tracked at
[MODERNIZATION §5.3](../player/MODERNIZATION.md#53-packages), and explained in
[PACKAGES.md](PACKAGES.md#curated-packages)).

What is missing is a curated *index* with compatibility gates (`min_firmware`,
device model, package dependencies) and a storefront scene: categories,
screenshots, install state. Reuse `ComponentList` rendering from Patch so the
store is not a second UI stack.
