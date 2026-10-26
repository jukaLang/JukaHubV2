# Feature backlog: the gaps, with the honest design

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
and the recording half of that already ships (RetroArch's
`recording_output_directory` → the **Recordings** screen; see
[RECORDINGS.md](RECORDINGS.md) for why a hub-drawn overlay cannot exist).

Still open: a JukaHub-managed overlay preset and the hotkey bindings
(`input_menu_toggle`, `input_screenshot`, `input_toggle_fast_forward`).
Deliberately not written yet — the binds are numeric button codes, and a guess
breaks controls on a device whose mapping nobody measured. The overlay also
needs art the repo does not ship. What is left is assets plus a profile per
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

The fetch, the per-title not-found cache and the completeness ring are shipped.
What is still open is how well a title matches: names the libretro set spells
differently (`Zelda II` vs `Zelda II - The Adventure of Link`) are recorded as
not found. A manual "choose image" action and a title-normalisation table are the
next step; neither needs a new downloader.

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
