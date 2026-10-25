# Checkpoints and quick switch — milestone design

**Status: design only. Nothing in this document is implemented.** The current
resume behaviour is RetroArch's auto state, described below; named checkpoints
and a game switcher are not in the app.

This page exists because "checkpoints / quick switch" is the largest item on the
[feature backlog](FEATURE-BACKLOG.md#5-game-switcher-with-quick-resume-first-half-shipped)
and the two obvious designs have very different costs on this hardware. It
records what the code already does, what each design would actually require, and
which parts cannot work — so the next person starts from measurements instead of
a sketch.

## What "checkpoint" means here

Three different features are usually bundled under that word. Keep them apart:

| Feature | Definition | Today |
|---|---|---|
| **Auto resume** | The emulator writes a state when it exits and loads it on the next launch. | Shipped for RetroArch launches. |
| **Named checkpoints** | The user pins a state ("Boss fight", "Before the credits") and can return to it later, beside the live save. | Not implemented. |
| **Quick switch** | Leave one game, start another, and come back to the first exactly where it was, without a console-style reload. | Not implemented. |

Auto resume is not a checkpoint: it is one slot per game, overwritten every
time the game exits, and it depends on the emulator shutting down cleanly.

## What the code already provides

- **Auto resume.** [savestate_resume.go](../player/savestate_resume.go) turns on
  `savestate_auto_save` and `savestate_auto_load` in `retroarch.cfg` before a
  launch when the resolved launcher actually runs RetroArch. The edit is
  line-based, keeps the original once as `retroarch.cfg.jukahub-backup`, and is
  skipped for standalone emulators. `savestateResume: false` opts out.
- **Recents and Continue.** [game_library.go](../player/game_library.go) writes
  `game-recents.json` (last twelve games, deduplicated, atomic write) and the
  Continue row relaunches through the same launcher resolution as the library.
  This is the seed of a switcher UI: it already knows *which* games are in play
  and how to start them.
- **Save snapshots.** [savemanager.go](../player/savemanager.go) copies emulator
  *save files* (not emulator state) into `save-manager/snapshots/<id>/` with a
  readable JSON manifest, verifies SHA-256 on restore, keeps ten snapshots, and
  can package one for a phone. Snapshotting is bounded (256 MiB per file,
  1 GiB total) and refuses folders that look like ISOs. This is the pattern a
  checkpoint store should copy: plain directories, hashes, atomic writes.
- **Low-battery protect.** [lowbattery.go](../player/lowbattery.go) saves, syncs
  and suspends the device at 3% while a game runs. It proves the device can
  suspend, but it suspends the whole device, not one emulator behind the hub.
- **Framebuffer ownership.** [gamesuspend.go](../player/gamesuspend.go) parks
  JukaHub's main loop while any screen-taking child is alive, because the child
  and the hub share one framebuffer. Any "switch back to the game" design has to
  fit this handshake rather than fight it.
- **Launch options and play time.** Per-game options are keyed by ROM path in
  `game-options.json` ([gameoptions.go](../player/gameoptions.go)), and play
  time is recorded in [playtime.go](../player/playtime.go). Both are already
  per-content metadata a checkpoint could hang off.

## Candidate designs

### A. Named state slots, RetroArch only (recommended first phase)

The hub asks RetroArch for a state, or works with the state files RetroArch
already writes, and lists them per game in a "Checkpoints" panel.

What it reuses: launcher resolution, the recents list, the save-manager
manifest/restore pattern, and the existing auto-save switches.

What it must decide, with the installed build in hand:

1. **Where the state files live.** RetroArch writes states beside the content or
   under `savestate_directory` in the cfg; JukaHub must read that key rather
   than assume a path, exactly as `resolveRetroArchSaveDir` already does for
   saves in [savemanager.go](../player/savemanager.go).
2. **How a named slot is addressed.** RetroArch already names states per
   content, and its slot mechanism is a config key (`savestate_slot`) plus
   manual save/load hotkeys. The design must verify against the shipped build
   whether a launcher can select a slot non-interactively, or whether the hub
   has to copy the state file on save and restore it on load. Do not promise a
   `--slot` flag until a device shows one; today the library only sets the two
   auto switches.
3. **When a checkpoint is taken.** Snapshotting a *running* RetroArch means
   either a hotkey JukaHub cannot press from outside, or a copy of the last
   auto state. The honest v1 is therefore "pin the state produced at the last
   exit, with a user-given name", not "snapshot the live frame".

Deletion and retention follow `save-manager`: named slots are directories with
a manifest, capped by count and size, restored only after the target file's
hash matches what was recorded.

**Switcher UI on top.** Once checkpoints exist, the "quick switch" that is
actually deliverable is a launch-time switch: quit the current game normally
(RetroArch writes its auto state), then start the chosen game from the recents
list. That is one process at a time, so it fits the framebuffer handshake and
the memory budget; it simply is not instantaneous.

### B. Suspend the emulator process (opt-in experiment, not default)

Keep one emulator alive and stopped (`SIGSTOP`), start another, and resume the
first with `SIGCONT` when the user switches back.

Why it is attractive: it preserves exact state with no state file, no SD write
and no emulator-specific state format.

Why it is hard here:

- **Memory.** The target has ~1 GB of RAM and one core. RetroArch with a
  libretro core can hold a large working set, and a stopped process is swapped
  out only if a swap file exists — writing it to the SD card is slower than a
  normal state save. Two live emulators plus the hub is not a safe default.
- **Process tree.** The stock firmware launches games through `Emus/<system>/launch.sh`,
  which usually execs or forks the emulator. Stopping only the shell leaves the
  emulator running; the hub would have to stop the whole process group and
  verify every member.
- **Framebuffer and audio.** A stopped process keeps the DRM/GPU file
  descriptors and the audio device open. The second game may fail to open audio,
  and the firmware's compositor may show a frozen frame if the first process is
  resumed while the second still owns the screen. The existing
  [gamesuspend.go](../player/gamesuspend.go) counter only knows "a child is
  alive"; it would need states for "alive but stopped".
- **Firmware behaviour.** The vendor's power handling and the launcher's own
  supervision may reap or re-parent stopped children. That has to be seen on
  both stock images before any code is written.
- **No standalone support.** mGBA, PPSSPP and DraStic are separate processes
  with their own renderers and handles; a generic stop/continue would hit the
  same problems with none of the RetroArch config knowledge.

**Decision:** treat B as a spike behind a hidden flag after Phase 1 ships, and
only if Phase 0 measures at least one full working set of headroom. Do not ship
it as the default switcher.

## Hard constraints

| Constraint | Consequence for this feature |
|---|---|
| ~1 GB RAM, one slow core | At most one live emulator; state files, not suspended processes, are the default unit. |
| SD card is the only storage and the slowest component | A checkpoint write must be bounded, atomic and off the render thread; no per-frame state copies. |
| The emulator owns the framebuffer | The hub cannot draw a checkpoint menu over a running game; any capture happens at launch/exit or through the emulator's own hotkeys. |
| Standalone emulators own their state formats | Refuse them by name instead of exporting something they cannot reload. |
| Launchers are shell scripts owned by the firmware | The hub may edit `retroarch.cfg` line-based (as it already does) but must not rewrite the launcher. |
| Hard power-off skips shutdown hooks | A checkpoint must never be the *only* copy of progress; the save manager snapshot stays the safety net. |

## Phased plan

**Phase 0 — measure (no UI).**
On both stock images, record: RetroArch's real `savestate_directory`, the size
of a typical auto state per core, the free RAM during a RetroArch session, and
whether the firmware keeps a stopped child alive for a minute. Results go into
this document. Nothing ships.

**Phase 1 — named checkpoints for RetroArch (the shippable feature).**
- Store: `save-manager/checkpoints/<game-slug>/<slot>/` with a manifest (ROM
  path, core, created time, bytes, SHA-256 of every file), following the
  save-manager snapshot format.
- Capture: pin the auto state after a clean exit, labelled by the user on the
  on-screen keyboard; refuse when the state file is missing or older than the
  session the user just played.
- Restore: before launching that game, verify the slot against its manifest and
  put the state where RetroArch will load it; leave the live auto state alone
  unless the user chooses to overwrite it.
- UI: a **Checkpoints** row in the library's options sheet, listing saved slots
  per game with restore/rename/delete. Reuse the save-manager panel layout.
- Gate: RetroArch only. For any other launcher the row explains that the
  emulator owns its states.

**Phase 2 — launch-time quick switch.**
- A "Switch game" action on the Continue row: quit the current game through the
  normal path (so the auto state is written), then launch the selected recent.
- No new storage format; it is Phase 1's restore path plus the existing recents.
- The switcher must refuse while a game is live and the hub has no exit path
  (for example when the launcher's shell does not forward a quit signal).

**Phase 3 — suspend/resume spike (only if Phase 0 says there is room).**
One suspended game, RetroArch only, opt-in, with a visible "this may fail on
your firmware" label. Success means: switch away, play, switch back, audio and
input still work, and no frame from the stopped process is ever presented.

**Explicitly out of scope:** snapshotting a standalone emulator, snapshotting a
running frame from outside the emulator, more than one suspended game, and any
promise of resume on a device where the app is killed by the firmware.

## What must be tested, and how

- **Slot naming and parsing** — unit tests over ROM paths with dots, spaces,
  non-ASCII titles and multi-disc `.m3u` names; a slot must never collide with
  another game or with the auto state.
- **Capture safety** — the state is copied to a temporary directory, hashed and
  renamed into place; a full card or a failed copy leaves the previous slot
  untouched. Mirror `createSaveSnapshot`'s behaviour and tests.
- **Restore ordering** — the slot is verified *before* the launcher starts, and a
  mismatch aborts the launch with a toast instead of loading a corrupt state.
- **Refusals** — standalone launchers, missing files, a state older than the
  recorded session, and read-only cards each produce a specific message.
- **Device behaviour** — a real session on the Smart Pro and the Brick: capture,
  quit, restore, and a hard power-off in between. Desktop tests cannot prove the
  state directory or the firmware's process handling.
- **Memory (Phase 3 only)** — the spike reports peak RSS and swap use, not a
  build that merely starts.

## Open questions

1. Does the shipped RetroArch build honour `savestate_directory` and a slot key
   in a way a launcher can drive without the emulator's own menu?
2. Is the auto state written before the process exits, or on a timer, and can
   the hub tell a complete state from a truncated one without a manifest?
3. Do the two stock firmwares re-parent or reap a `SIGSTOP`ped emulator?
4. How much does a checkpoint capture cost on the card at the sizes measured in
   Phase 0 — and is a checkpoint worth keeping once the save manager snapshot
   already covers the save files?

Until those are answered, the only checkpoint the app can honestly offer is the
RetroArch auto state it already turns on.
